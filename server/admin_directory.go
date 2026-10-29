package hex

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type adminDirectoryView struct {
	Chrome
	Query       string
	Sort        string
	SortChoices []choice
	Sites       []adminSiteRow
	Pagination  paginationView
}

type adminShellView struct {
	Chrome
	Title   string
	LoadURL string
}

func adminPageNumber(values url.Values) (int, error) {
	page := 1
	if value := values.Get("page"); value != "" {
		var err error
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 || page > 1000000 {
			return 0, errAdminPage
		}
	}
	return page, nil
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.adminCaller(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	if site := r.PathValue("site"); site != "" {
		values.Set("site", site)
	}
	if _, err := analyticsQuery(values, time.Now()); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if _, err := adminPageNumber(values); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if r.URL.Path == "/admin/sites" || s.config.Analytics == nil {
		s.adminDirectoryPage(w, r, identity)
		return
	}
	if values.Get("fragment") == "1" {
		s.adminReportPage(w, r)
		return
	}
	title := "Platform overview"
	if r.URL.Path == "/admin/users" {
		title = "People"
	}
	if site := r.PathValue("site"); site != "" {
		title = site
	}
	values.Set("fragment", "1")
	s.writePortalPage(w, "admin-shell", adminShellView{Chrome: s.chromeFor(identity, "admin"), Title: title, LoadURL: r.URL.Path + "?" + values.Encode()})
}

func (s *Server) adminDirectoryPage(w http.ResponseWriter, r *http.Request, identity *Identity) {
	rows, _, err := s.adminInventory(r.Context())
	if err != nil {
		writeServerError(w, err)
		return
	}
	values := r.URL.Query()
	query := strings.TrimSpace(values.Get("q"))
	rows = slices.DeleteFunc(rows, func(row adminSiteRow) bool {
		return !containsFold(row.Name+" "+row.Title+" "+row.CreatedBy+" "+row.Owners, query)
	})
	sort := values.Get("sort")
	slices.SortFunc(rows, func(a, b adminSiteRow) int {
		if sort == "recent" && !a.Timestamp.Equal(b.Timestamp) {
			if a.Timestamp.After(b.Timestamp) {
				return -1
			}
			return 1
		}
		if order := strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	view := adminDirectoryView{Chrome: s.chromeFor(identity, "admin"), Query: query, Sort: sort}
	view.SortChoices = choices(sort, [2]string{"name", "Name A–Z"}, [2]string{"recent", "Recently updated"})
	cards := make([]siteCard, len(rows))
	for index, row := range rows {
		cards[index] = row.siteCard
	}
	_, view.Pagination = paginateCards(cards, values, "/admin/sites", libraryPageSize)
	start := (view.Pagination.Page - 1) * libraryPageSize
	view.Sites = rows[start:min(len(rows), start+libraryPageSize)]
	s.labelAdminOwners(r.Context(), view.Sites)
	// Admin inventory must not fetch app assets merely to display an icon.
	for index := range view.Sites {
		view.Sites[index].IconURL = ""
	}
	s.writePortalPage(w, "admin-directory", view)
}

func (s *Server) labelAdminOwners(ctx context.Context, rows []adminSiteRow) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	var principals []string
	for _, row := range rows {
		principals = append(principals, row.ownerPrincipals...)
	}
	slices.Sort(principals)
	labels := make(map[string]string)
	for _, label := range s.describePrincipals(ctx, slices.Compact(principals)) {
		labels[label.Principal] = label.Name
	}
	for index := range rows {
		names := []string{}
		for _, principal := range rows[index].ownerPrincipals {
			name := labels[principal]
			if name == "" {
				name = principal
			}
			names = append(names, name)
		}
		rows[index].Owners = strings.Join(names, ", ")
	}
}

func (s *Server) adminDetailInventory(ctx context.Context, site string) ([]adminSiteRow, adminInventoryCounts, error) {
	var metadata *SiteMetadata
	if reader, ok := s.config.Sites.(SiteMetadataReader); ok {
		var err error
		metadata, err = reader.ReadSiteMetadata(ctx, site)
		if err != nil && err != ErrNotFound {
			return nil, adminInventoryCounts{}, err
		}
	}
	access, exists, err := s.sitePolicy(ctx, site)
	if err != nil {
		return nil, adminInventoryCounts{}, err
	}
	siteURL, err := s.siteURL(site)
	if err != nil {
		return nil, adminInventoryCounts{}, err
	}
	row := adminSiteRow{siteCard: s.cardForSite(site, siteURL, metadata, time.Now()), Published: metadata != nil, ownerPrincipals: access.Owners}
	row.IconURL = ""
	row.Access, row.AccessTone = audience(access, exists)
	if metadata != nil {
		row.Created = analyticsDate(metadata.CreatedAt)
		row.CreatedAt = metadata.CreatedAt
		if metadata.CreatedBy != nil {
			row.CreatorID = metadata.CreatedBy.ID
		}
	}
	return []adminSiteRow{row}, adminInventoryCounts{}, nil
}
