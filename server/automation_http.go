package hex

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"
)

const (
	schedulerInterval      = 30 * time.Second
	maxConcurrentRuns      = 4
	maxDueAutomations      = 20
	defaultRunHistoryLimit = 20
)

func (s *Server) registerAutomationRoutes() {
	if !s.automationsEnabled() {
		return
	}
	s.mux.HandleFunc("GET /api/hex/sites/{site}/automations", s.listAutomations)
	s.mux.HandleFunc("PUT /api/hex/sites/{site}/automations", s.putAutomations)
	s.mux.HandleFunc("POST /api/hex/sites/{site}/automations/test", s.testAutomation)
	s.mux.HandleFunc("POST /api/hex/sites/{site}/automations/{automation}/run", s.triggerAutomation)
	s.mux.HandleFunc("GET /api/hex/sites/{site}/automations/{automation}/runs", s.listAutomationRuns)
	s.mux.HandleFunc("GET /api/hex/sites/{site}/automation-runs/{run}", s.getAutomationRun)
}

// automationOwner authorizes management of a site's automations: owners
// and admins only.
func (s *Server) automationOwner(w http.ResponseWriter, r *http.Request) (*Identity, string, bool) {
	identity := s.requestIdentity(r)
	if s.config.Identity != nil && identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, "", false
	}
	site := r.PathValue("site")
	if !siteNamePattern.MatchString(site) {
		writeError(w, http.StatusBadRequest, "invalid site")
		return nil, "", false
	}
	if s.config.Identity != nil {
		owner, err := s.isSiteOwner(r.Context(), identity, site)
		if err != nil {
			writeServerError(w, err)
			return nil, "", false
		}
		if !owner {
			writeError(w, http.StatusForbidden, "only the site's owners manage its automations")
			return nil, "", false
		}
	}
	return identity, site, true
}

// AutomationStatus is a stored automation with its latest run.
type AutomationStatus struct {
	ScheduledAutomation
	LastRun *AutomationRun `json:"lastRun,omitempty"`
}

func (s *Server) listAutomations(w http.ResponseWriter, r *http.Request) {
	_, site, ok := s.automationOwner(w, r)
	if !ok {
		return
	}
	automations, err := s.config.Automations.ListSiteAutomations(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	result := make([]AutomationStatus, 0, len(automations))
	for _, automation := range automations {
		status := AutomationStatus{ScheduledAutomation: automation}
		runs, err := s.config.Automations.ListAutomationRuns(r.Context(), site, automation.Automation.Name, 1)
		if err != nil {
			writeServerError(w, err)
			return
		}
		if len(runs) > 0 {
			status.LastRun = &runs[0]
		}
		result = append(result, status)
	}
	writeJSON(w, http.StatusOK, result)
}

// putAutomations replaces the site's automations without republishing it.
func (s *Server) putAutomations(w http.ResponseWriter, r *http.Request) {
	identity, site, ok := s.automationOwner(w, r)
	if !ok {
		return
	}
	var automations []Automation
	if !readAutomationBody(w, r, &automations) {
		return
	}
	if err := ValidateAutomations(automations); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.replaceAutomations(r.Context(), site, automations, identity); err != nil {
		writeServerError(w, err)
		return
	}
	slog.Info("automations replaced", "site", site, "count", len(automations), "by", identityName(identity))
	s.listAutomations(w, r)
}

func readAutomationBody(w http.ResponseWriter, r *http.Request, value any) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "automation definitions are limited to 1 MiB")
		return false
	}
	if err := json.Unmarshal(data, value); err != nil {
		writeError(w, http.StatusBadRequest, "expected JSON automation definitions: "+err.Error())
		return false
	}
	return true
}

// testAutomation runs a definition sent in the request, by default as a dry
// run, so authors can try changes before deploying them.
func (s *Server) testAutomation(w http.ResponseWriter, r *http.Request) {
	identity, site, ok := s.automationOwner(w, r)
	if !ok {
		return
	}
	var automation Automation
	if !readAutomationBody(w, r, &automation) {
		return
	}
	if err := validateAutomation(automation); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dryRun := r.URL.Query().Get("dryRun") != "false"
	run, err := s.startRun(site, automation, TriggerTest, dryRun, identity)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) triggerAutomation(w http.ResponseWriter, r *http.Request) {
	identity, site, ok := s.automationOwner(w, r)
	if !ok {
		return
	}
	automations, err := s.config.Automations.ListSiteAutomations(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	index := slices.IndexFunc(automations, func(entry ScheduledAutomation) bool {
		return entry.Automation.Name == r.PathValue("automation")
	})
	if index < 0 {
		writeError(w, http.StatusNotFound, "automation not found")
		return
	}
	dryRun := r.URL.Query().Get("dryRun") == "true"
	run, err := s.startRun(site, automations[index].Automation, TriggerManual, dryRun, identity)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// startRun records a running run and performs it in the background, so
// long runs outlive the request; callers poll the run.
func (s *Server) startRun(site string, automation Automation, trigger string, dryRun bool, identity *Identity) (AutomationRun, error) {
	id, err := newID()
	if err != nil {
		return AutomationRun{}, err
	}
	run := AutomationRun{
		ID: id, Site: site, Automation: automation.Name, Trigger: trigger, DryRun: dryRun,
		Status: RunRunning, StartedAt: time.Now().UTC(), StartedBy: personOf(identity),
		Steps: []AutomationStepRun{},
	}
	s.recordRun(run)
	started := run
	s.automationRuns.Add(1)
	go func() {
		defer s.automationRuns.Done()
		s.runAutomation(context.Background(), site, automation, &run)
	}()
	return started, nil
}

func (s *Server) listAutomationRuns(w http.ResponseWriter, r *http.Request) {
	_, site, ok := s.automationOwner(w, r)
	if !ok {
		return
	}
	limit := defaultRunHistoryLimit
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 50 {
			writeError(w, http.StatusBadRequest, "limit must be 1–50")
			return
		}
		limit = parsed
	}
	runs, err := s.config.Automations.ListAutomationRuns(r.Context(), site, r.PathValue("automation"), limit)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getAutomationRun(w http.ResponseWriter, r *http.Request) {
	_, site, ok := s.automationOwner(w, r)
	if !ok {
		return
	}
	run, err := s.config.Automations.GetAutomationRun(r.Context(), site, r.PathValue("run"))
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// runAutomationScheduler runs scheduled automations until ctx ends, then
// waits for runs in progress. Each due occurrence is claimed in the store, so
// several instances never run it twice. Missed occurrences, for example
// during a restart, run once and are not caught up.
func (s *Server) runAutomationScheduler(ctx context.Context) {
	defer s.automationRuns.Wait()
	slots := make(chan struct{}, maxConcurrentRuns)
	ticker := time.NewTicker(schedulerInterval)
	defer ticker.Stop()
	for {
		s.startDueAutomations(ctx, slots)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) startDueAutomations(ctx context.Context, slots chan struct{}) {
	now := time.Now().UTC()
	due, err := s.config.Automations.DueAutomations(ctx, now, maxDueAutomations)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("find due automations", "error", err)
		}
		return
	}
	for _, entry := range due {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		next := nextRun(entry.Automation, time.Now().UTC())
		won, err := s.config.Automations.ClaimAutomation(ctx, entry.Site, entry.Automation.Name, entry.Revision, entry.NextRun, next)
		if err != nil {
			<-slots
			slog.Error("claim automation", "site", entry.Site, "automation", entry.Automation.Name, "error", err)
			continue
		}
		if !won {
			<-slots
			continue
		}
		run, err := s.startScheduledRun(entry, slots)
		if err != nil {
			<-slots
			slog.Error("start automation", "site", entry.Site, "automation", entry.Automation.Name, "error", err)
			continue
		}
		slog.Info("automation started", "site", entry.Site, "automation", entry.Automation.Name, "run", run)
	}
}

func (s *Server) startScheduledRun(entry ScheduledAutomation, slots chan struct{}) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	run := AutomationRun{
		ID: id, Site: entry.Site, Automation: entry.Automation.Name, Trigger: TriggerSchedule,
		Status: RunRunning, StartedAt: time.Now().UTC(), Steps: []AutomationStepRun{},
	}
	s.recordRun(run)
	s.automationRuns.Add(1)
	go func() {
		defer s.automationRuns.Done()
		defer func() { <-slots }()
		s.runAutomation(context.Background(), entry.Site, entry.Automation, &run)
	}()
	return id, nil
}
