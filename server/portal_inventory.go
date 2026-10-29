package hex

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Only descriptive inventory is cached. Every request reads current permissions
// before projecting it for a viewer, including warm searches and pinned apps.
const portalInventoryTTL = 5 * time.Second

type portalInventoryCache struct {
	mu         sync.Mutex
	flight     singleflight.Group
	sites      []Site
	expires    time.Time
	generation uint64
}

func (s *Server) invalidatePortalInventory() {
	s.portalIcons.mu.Lock()
	clear(s.portalIcons.entries)
	s.portalIcons.mu.Unlock()
	c := &s.portalInventory
	c.mu.Lock()
	c.generation++
	c.expires = time.Time{}
	c.mu.Unlock()
}

func (s *Server) portalSites(ctx context.Context) ([]Site, error) {
	if s.config.Sites == nil {
		return nil, nil
	}
	c := &s.portalInventory
	c.mu.Lock()
	if time.Now().Before(c.expires) {
		sites := c.sites
		c.mu.Unlock()
		return sites, nil
	}
	generation := c.generation
	c.mu.Unlock()
	// A canceled browser request must not poison another viewer's shared refresh.
	result := c.flight.DoChan(fmt.Sprint(generation), func() (any, error) {
		c.mu.Lock()
		if c.generation == generation && time.Now().Before(c.expires) {
			sites := c.sites
			c.mu.Unlock()
			return sites, nil
		}
		c.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		sites, err := s.readPortalInventory(ctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if c.generation == generation && cacheablePortalInventory(sites) {
			c.sites = sites
			c.expires = time.Now().Add(portalInventoryTTL)
		}
		c.mu.Unlock()
		return sites, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.([]Site), nil
	}
}

func cacheablePortalInventory(sites []Site) bool {
	if len(sites) > 10000 {
		return false
	}
	bytes := 0
	for _, site := range sites {
		bytes += 256 + len(site.Name) + len(site.URL)
		if metadata := site.Metadata; metadata != nil {
			bytes += len(metadata.Title) + len(metadata.Description) + len(metadata.Author) + len(metadata.Kind)
			for _, person := range []*Person{metadata.CreatedBy, metadata.PublishedBy} {
				if person != nil {
					bytes += len(person.ID) + len(person.Name) + len(person.Email)
				}
			}
		}
		if bytes > 8<<20 {
			return false
		}
	}
	return true
}

func (s *Server) readPortalInventory(ctx context.Context) ([]Site, error) {
	names, err := s.config.Sites.ListSites(ctx)
	if err != nil {
		return nil, err
	}
	names = slices.Clone(names)
	slices.Sort(names)
	names = slices.DeleteFunc(slices.Compact(names), func(name string) bool { return !siteNamePattern.MatchString(name) })
	sites := make([]Site, len(names))
	reader, _ := s.config.Sites.(SiteMetadataReader)
	// Bound remote metadata reads, rather than serializing 100+ network trips or
	// starting a goroutine for every published site.
	var workers sync.WaitGroup
	jobs := make(chan int)
	errors := make([]error, len(names))
	for range min(8, len(names)) {
		workers.Go(func() {
			for index := range jobs {
				name := names[index]
				sites[index].Name = name
				sites[index].URL, errors[index] = s.siteURL(name)
				if errors[index] == nil && reader != nil {
					sites[index].Metadata, errors[index] = reader.ReadSiteMetadata(ctx, name)
				}
			}
		})
	}
	for index := range names {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	for index, err := range errors {
		if err != nil {
			return nil, fmt.Errorf("read portal inventory for %s: %w", names[index], err)
		}
	}
	return sites, ctx.Err()
}

func (s *Server) portalCards(ctx context.Context, identity *Identity, all bool) ([]siteCard, error) {
	sites, err := s.portalSites(ctx)
	if err != nil {
		return nil, err
	}
	policies, err := s.portalPolicies(ctx, sites)
	if err != nil {
		return nil, err
	}
	prefs, err := s.portalPreferences(ctx, identity)
	if err != nil {
		return nil, err
	}
	grants, err := s.activeAdminAccess(ctx, identity, "")
	if err != nil {
		return nil, err
	}
	now := time.Now()
	cards := make([]siteCard, 0, len(sites))
	for _, site := range sites {
		access, exists := policies[site.Name]
		owned := exists && identity.matchesAny(access.Owners)
		policy := access
		if !exists {
			policy = MigrateSiteAccess(access)
		}
		policy = adminApplicationPolicy(policy, identity, grants, site.Name)
		role := s.siteRole(identity, policy, true)
		authorization := siteAuthorization{identity: identity, access: policy, role: role}
		admitted := authorization.applicationAdmitted()
		personal := owned || admitted && exists && audienceIsShared(access)
		discoverable := site.Metadata == nil || site.Metadata.Kind != KindArtifact && (site.Metadata.Discoverable == nil || *site.Metadata.Discoverable)
		if !all && !personal && !(admitted && discoverable) && !(admitted && slices.Contains(prefs.Pins, site.Name)) {
			continue
		}
		card := s.cardForSite(site.Name, site.URL, site.Metadata, now)
		card.Owned = s.manageEnabled() && (owned || hasAdminScope(grants, site.Name, adminManage))
		card.PersonalOwner = owned
		card.Shared = personal && !owned
		card.Company = admitted && discoverable && (access.Version == 2 && access.Admission.SignedIn || access.Version != 2 && len(access.Viewers) == 0)
		card.CanPin = s.preferencesEnabled(identity) && admitted
		card.Pinned = card.CanPin && slices.Contains(prefs.Pins, site.Name)
		card.Recent = slices.Index(prefs.Recent, site.Name)
		card.Analytics = card.Owned && s.config.Analytics != nil
		card.Access, card.AccessTone = audience(access, exists)
		cards = append(cards, card)
	}
	return cards, nil
}

func (s *Server) portalPolicies(ctx context.Context, sites []Site) (map[string]SiteAccess, error) {
	policies := make(map[string]SiteAccess, len(sites))
	if s.config.Access == nil || len(sites) == 0 {
		return policies, nil
	}
	batch, batched := s.config.Access.(SiteAccessBatchReader)
	if batched {
		names := make([]string, len(sites))
		for index, site := range sites {
			names[index] = site.Name
		}
		var err error
		policies, err = batch.GetSiteAccessBatch(ctx, names)
		if err != nil {
			return nil, err
		}
	}
	for _, site := range sites {
		var access SiteAccess
		var exists bool
		var err error
		if batched {
			access, exists = policies[site.Name]
			if exists {
				access, exists, err = s.validateSitePolicy(ctx, site.Name, access)
			}
		} else {
			access, exists, err = s.sitePolicy(ctx, site.Name)
		}
		if err != nil {
			return nil, err
		}
		if exists {
			policies[site.Name] = access
		}
	}
	return policies, nil
}

func audienceIsShared(access SiteAccess) bool {
	if access.Version == 2 {
		return !access.Admission.SignedIn
	}
	return len(access.Viewers) > 0
}
