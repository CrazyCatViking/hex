package hex

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const libraryPageSize = 12

type scopeLink struct {
	Label  string
	URL    string
	Active bool
}

type paginationView struct {
	Total    int
	Page     int
	Pages    int
	Start    int
	End      int
	Previous string
	Next     string
}

func paginateCards(cards []siteCard, query url.Values, path string, size int) ([]siteCard, paginationView) {
	pages := max(1, (len(cards)+size-1)/size)
	page, _ := strconv.Atoi(query.Get("page"))
	page = min(max(1, page), pages)
	start := (page - 1) * size
	end := min(start+size, len(cards))
	pagination := paginationView{Total: len(cards), Page: page, Pages: pages, Start: min(start+1, len(cards)), End: end}
	if page > 1 {
		pagination.Previous = portalPageURL(path, query, "page", strconv.Itoa(page-1))
	}
	if page < pages {
		pagination.Next = portalPageURL(path, query, "page", strconv.Itoa(page+1))
	}
	return cards[start:end], pagination
}

func portalPageURL(path string, query url.Values, key, value string) string {
	copy := make(url.Values, len(query))
	for key, values := range query {
		copy[key] = slices.Clone(values)
	}
	copy.Set(key, value)
	return path + "?" + copy.Encode()
}

func cardsInScope(cards []siteCard, scope string) []siteCard {
	result := make([]siteCard, 0, len(cards))
	for _, card := range cards {
		matches := false
		switch scope {
		case "all":
			matches = true
		case "owned":
			matches = card.PersonalOwner
		case "shared":
			matches = card.Shared
		case "everyone":
			matches = card.Company
		case "pinned":
			matches = card.Pinned
		case "recent":
			matches = card.Recent >= 0 && card.CanPin
		}
		if matches {
			result = append(result, card)
		}
	}
	if scope == "recent" {
		slices.SortFunc(result, func(a, b siteCard) int { return a.Recent - b.Recent })
	}
	return result
}

func (s *Server) libraryCatalog(ctx context.Context, cards []siteCard, query url.Values) catalogView {
	view := catalogView{Search: strings.TrimSpace(query.Get("search")), Sort: query.Get("sort"), Scope: query.Get("scope"), Kind: query.Get("kind"), Mode: query.Get("mode")}
	if !slices.Contains([]string{"all", "owned", "shared", "everyone", "pinned", "recent"}, view.Scope) {
		view.Scope = "all"
	}
	if !slices.Contains([]string{"recent", "name", "relevance", "popular", "visitors", "visited"}, view.Sort) {
		view.Sort = "relevance"
	}
	if view.Mode != "list" {
		view.Mode = "grid"
	}
	view.SortChoices = append(choices(view.Sort, [2]string{"relevance", "Best match"}), s.cardSortChoices(view.Sort)...)
	view.TrafficEnabled = s.cardTrafficEnabled()
	view.TrafficFilter = query.Get("traffic")
	if view.TrafficFilter == "" {
		view.TrafficFilter = "all"
	}
	view.TrafficChoices = cardTrafficChoices(view.TrafficFilter)
	view.Filtered = view.Search != "" || view.Scope != "all" || view.Kind != ""
	for _, scope := range [][2]string{{"all", "All accessible"}, {"pinned", "Pinned"}, {"recent", "Recently opened"}, {"shared", "Shared with you"}, {"owned", "Owned by you"}, {"everyone", "Company-wide"}} {
		linkQuery := url.Values{}
		for key, values := range query {
			linkQuery[key] = slices.Clone(values)
		}
		linkQuery.Del("page")
		view.Scopes = append(view.Scopes, scopeLink{Label: scope[1], URL: portalPageURL("/library", linkQuery, "scope", scope[0]), Active: scope[0] == view.Scope})
	}
	for _, card := range cardsInScope(cards, view.Scope) {
		if matchesLibrarySearch(card, view.Search) && matchesFilter(card, view.Kind, "") {
			view.Sites = append(view.Sites, card)
		}
	}
	if trafficRequested(view.Sort, view.TrafficFilter) {
		view.TrafficUnavailable = !s.fillCardTraffic(ctx, view.Sites)
		view.Sites = slices.DeleteFunc(view.Sites, func(card siteCard) bool { return !matchesTraffic(card, view.TrafficFilter) })
	}
	if view.Scope != "recent" || query.Has("sort") {
		sortSiteCards(view.Sites, view.Sort)
	}
	if view.Search != "" && view.Sort == "relevance" {
		slices.SortStableFunc(view.Sites, func(a, b siteCard) int { return searchRank(b, view.Search) - searchRank(a, view.Search) })
	}
	view.Sites, view.Pagination = paginateCards(view.Sites, query, "/library", libraryPageSize)
	return view
}

func matchesLibrarySearch(card siteCard, search string) bool {
	text := strings.ToLower(strings.Join([]string{card.Name, card.Title, card.Description, card.CreatedBy, card.PublishedBy}, " "))
	for _, term := range strings.Fields(strings.ToLower(search)) {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

func searchRank(card siteCard, search string) int {
	title, search := strings.ToLower(card.Title), strings.ToLower(search)
	if title == search || card.Name == search {
		return 3
	}
	if strings.HasPrefix(title, search) || strings.HasPrefix(card.Name, search) {
		return 2
	}
	if strings.Contains(title, search) {
		return 1
	}
	return 0
}

func (s *Server) libraryPage(w http.ResponseWriter, r *http.Request) {
	if !s.platformRequest(r) {
		http.NotFound(w, r)
		return
	}
	identity := s.requestIdentity(r)
	catalog, err := s.catalogView(r.Context(), identity, r.URL.Query())
	if err != nil {
		writeServerError(w, err)
		return
	}
	s.writePortalPage(w, "library", struct {
		Chrome
		Catalog catalogView
	}{s.chromeFor(identity, "home"), catalog})
}
