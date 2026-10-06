package hex

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The integration audit page and API let platform admins see who called
// audited integrations, from which site, with what input and which records
// the results showed.

const (
	defaultAuditListLimit = 200
	maxAuditListLimit     = 1000
	maxAuditExportLimit   = 10000
	auditShownRecords     = 5
	auditShownInputSize   = 200
)

func (s *Server) registerIntegrationAuditRoutes() {
	if !s.integrationAuditEnabled() {
		return
	}
	s.mux.HandleFunc("GET /admin/integration-audit", s.adminIntegrationAuditPage)
	s.mux.HandleFunc("GET /api/hex/manage/integration-audit", s.manageIntegrationAudit)
}

// integrationAuditFilter reads the filter from query parameters: since and
// until as RFC 3339 times or dates (until includes the whole day), site,
// caller, endpoint ("<integration>.<endpoint>", or an integration name for
// all its endpoints), record and limit.
func (s *Server) integrationAuditFilter(ctx context.Context, values url.Values, defaultLimit, maxLimit int) (IntegrationAuditFilter, error) {
	filter := IntegrationAuditFilter{
		Site: strings.TrimSpace(values.Get("site")), Record: strings.TrimSpace(values.Get("record")), Limit: defaultLimit,
	}
	var err error
	if filter.Since, err = parseAuditTime(values.Get("since"), false); err != nil {
		return filter, fmt.Errorf("since: %w", err)
	}
	if filter.Until, err = parseAuditTime(values.Get("until"), true); err != nil {
		return filter, fmt.Errorf("until: %w", err)
	}
	if endpoint := strings.TrimSpace(values.Get("endpoint")); endpoint != "" {
		if !strings.Contains(endpoint, ".") {
			endpoint += ".*"
		}
		filter.Endpoint = endpoint
	}
	if filter.Caller, err = s.auditCallerKey(ctx, values.Get("caller")); err != nil {
		return filter, err
	}
	if value := values.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > maxLimit {
			return filter, fmt.Errorf("limit must be a number from 1 to %d", maxLimit)
		}
		filter.Limit = limit
	}
	return filter, nil
}

// parseAuditTime reads an RFC 3339 time or a date. A date that ends a range
// means the end of that day.
func parseAuditTime(value string, ending bool) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at.UTC(), nil
	}
	day, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return time.Time{}, errors.New("use a date such as 2026-10-06 or a time such as 2026-10-06T14:00:00Z")
	}
	if ending {
		return day.AddDate(0, 0, 1), nil
	}
	return day, nil
}

// auditCallerKey turns the caller filter into a stable key. "user:" and
// "automation:" keys are used as given, the exact name or email of a
// remembered person becomes their key, and anything else is a user ID.
func (s *Server) auditCallerKey(ctx context.Context, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "user:") || strings.HasPrefix(value, "automation:") {
		return value, nil
	}
	if s.config.People == nil {
		return "user:" + value, nil
	}
	people, err := s.config.People.FindPeople(ctx, value, 20)
	if err != nil {
		return "", fmt.Errorf("find people: %w", err)
	}
	var matches []string
	for _, person := range people {
		if strings.EqualFold(person.Name, value) || strings.EqualFold(person.Email, value) {
			matches = append(matches, "user:"+person.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "user:" + value, nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%d people match %q; filter by their user:<id> instead", len(matches), value)
	}
}

func (s *Server) manageIntegrationAudit(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.adminCaller(w, r)
	if !ok {
		return
	}
	filter, err := s.integrationAuditFilter(r.Context(), r.URL.Query(), defaultAuditListLimit, maxAuditListLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	records, err := s.config.IntegrationAudit.ListIntegrationAudit(r.Context(), filter)
	if err != nil {
		writeServerError(w, err)
		return
	}
	slog.Info("integration audit read", "by", identityName(identity), "count", len(records))
	writeJSON(w, http.StatusOK, records)
}

type integrationAuditView struct {
	Chrome
	Analytics bool
	AISpend   bool
	Since     string
	Until     string
	Site      string
	Caller    string
	Endpoint  string
	Record    string
	Endpoints []choice
	Rows      []integrationAuditRow
	Limited   bool
	Limit     int
	CSVLink   string
	ErrorText string
}

type integrationAuditRow struct {
	At          string
	ISO         string
	Who         string
	WhoDetail   string
	Site        string
	Endpoint    string
	Records     []string
	MoreRecords int
	Cached      bool
	Failed      bool
	Input       string
}

func (s *Server) adminIntegrationAuditPage(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.adminCaller(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	if values.Get("format") == "csv" {
		s.exportIntegrationAudit(w, r, identity)
		return
	}

	view := integrationAuditView{
		Chrome: s.chromeFor(identity, "admin"), Analytics: s.config.Analytics != nil,
		AISpend: s.aiAccountingEnabled() && s.manageEnabled(),
		Since:   values.Get("since"), Until: values.Get("until"), Site: values.Get("site"),
		Caller: values.Get("caller"), Endpoint: values.Get("endpoint"), Record: values.Get("record"),
		Endpoints: s.auditEndpointChoices(values.Get("endpoint")),
	}
	exported := url.Values{"format": {"csv"}}
	for _, name := range []string{"since", "until", "site", "caller", "endpoint", "record"} {
		if value := values.Get(name); value != "" {
			exported.Set(name, value)
		}
	}
	view.CSVLink = "/admin/integration-audit?" + exported.Encode()

	filter, err := s.integrationAuditFilter(r.Context(), values, defaultAuditListLimit, maxAuditListLimit)
	if err != nil {
		view.ErrorText = err.Error()
		s.writePortalPage(w, "admin-integration-audit", view)
		return
	}
	records, err := s.config.IntegrationAudit.ListIntegrationAudit(r.Context(), filter)
	if err != nil {
		writeServerError(w, err)
		return
	}
	slog.Info("integration audit read", "by", identityName(identity), "count", len(records))
	for _, record := range records {
		view.Rows = append(view.Rows, newIntegrationAuditRow(record))
	}
	view.Limit = filter.Limit
	view.Limited = len(records) == filter.Limit
	s.writePortalPage(w, "admin-integration-audit", view)
}

// auditEndpointChoices offers each audited integration and its endpoints.
func (s *Server) auditEndpointChoices(selected string) []choice {
	values := [][2]string{{"", "All endpoints"}}
	for _, integration := range s.config.Integrations.all() {
		if !integration.integration.Audit {
			continue
		}
		name := integration.integration.Name
		values = append(values, [2]string{name + ".*", integration.integration.Title + ": every endpoint"})
		for _, endpoint := range integration.sortedEndpoints() {
			values = append(values, [2]string{endpoint.qualifiedName(), integration.integration.Title + ": " + endpoint.endpoint.Name})
		}
	}
	return choices(selected, values...)
}

func newIntegrationAuditRow(record IntegrationAuditRecord) integrationAuditRow {
	row := integrationAuditRow{
		At: record.At.UTC().Format("2006-01-02 15:04:05"), ISO: record.At.UTC().Format(time.RFC3339),
		Site: record.Site, Endpoint: record.Integration + "." + record.Endpoint,
		Cached: record.Cached, Failed: record.Failed, Input: truncateText(string(record.Input), auditShownInputSize),
	}
	row.Who, row.WhoDetail = auditCallerLabel(record)
	row.Records = record.Records
	if len(row.Records) > auditShownRecords {
		row.MoreRecords = len(row.Records) - auditShownRecords
		row.Records = row.Records[:auditShownRecords]
	}
	return row
}

func auditCallerLabel(record IntegrationAuditRecord) (string, string) {
	if automation, isAutomation := strings.CutPrefix(record.Caller, "automation:"); isAutomation {
		_, name, _ := strings.Cut(automation, "/")
		return name, "Automation"
	}
	if record.CallerName != "" {
		return record.CallerName, record.Caller
	}
	return strings.TrimPrefix(record.Caller, "user:"), ""
}

func truncateText(value string, size int) string {
	runes := []rune(value)
	if len(runes) <= size {
		return value
	}
	return string(runes[:size]) + "…"
}

func (s *Server) exportIntegrationAudit(w http.ResponseWriter, r *http.Request, identity *Identity) {
	filter, err := s.integrationAuditFilter(r.Context(), r.URL.Query(), maxAuditExportLimit, maxAuditExportLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	records, err := s.config.IntegrationAudit.ListIntegrationAudit(r.Context(), filter)
	if err != nil {
		writeServerError(w, err)
		return
	}
	slog.Info("integration audit exported", "by", identityName(identity), "count", len(records))

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="hex-integration-audit.csv"`)
	writer := csv.NewWriter(w)
	rows := [][]string{{"at_utc", "site", "integration", "endpoint", "caller", "caller_name", "cached", "failed", "records", "input"}}
	for _, record := range records {
		rows = append(rows, []string{
			record.At.UTC().Format(time.RFC3339), spreadsheetText(record.Site), spreadsheetText(record.Integration), spreadsheetText(record.Endpoint),
			spreadsheetText(record.Caller), spreadsheetText(record.CallerName), strconv.FormatBool(record.Cached), strconv.FormatBool(record.Failed),
			spreadsheetText(strings.Join(record.Records, " ")), spreadsheetText(string(record.Input)),
		})
	}
	if err := writer.WriteAll(rows); err != nil {
		slog.Error("write integration audit CSV", "error", err)
	}
}
