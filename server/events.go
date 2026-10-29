package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"
)

const maxEventBytes = 64 << 10
const maxEventSubscribers = 32
const maxDeliveryAttempts = 8

type EventType struct {
	Name        string          `json:"name"`
	Version     int             `json:"version"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

type EventSubscription struct {
	ID         string           `json:"id"`
	Site       string           `json:"site"`
	Principal  string           `json:"principal"`
	Producer   string           `json:"producer"`
	Event      string           `json:"event"`
	Version    int              `json:"version"`
	Automation string           `json:"automation"`
	Revision   string           `json:"revision"`
	Generation string           `json:"generation"`
	Disabled   bool             `json:"disabled,omitempty"`
	Where      *RecordCondition `json:"where,omitempty"`
}

type eventTopic struct {
	EventType
	Subscriptions map[string]EventSubscription `json:"subscriptions"`
}

type DurableEvent struct {
	ID        string          `json:"id"`
	Site      string          `json:"site"`
	Name      string          `json:"name"`
	Version   int             `json:"version"`
	Actor     string          `json:"actor"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"createdAt"`
}

type EventDelivery struct {
	Subscription EventSubscription `json:"subscription"`
	Status       string            `json:"status"`
	Attempts     int               `json:"attempts"`
	NextAttempt  time.Time         `json:"nextAttempt"`
	Lease        string            `json:"lease,omitempty"`
	LeaseUntil   time.Time         `json:"leaseUntil,omitzero"`
	RunID        string            `json:"runId,omitempty"`
	Error        string            `json:"error,omitempty"`
}

type eventRecord struct {
	DurableEvent
	Fingerprint string                   `json:"fingerprint"`
	Pending     bool                     `json:"pending"`
	Deliveries  map[string]EventDelivery `json:"deliveries"`
}

type publishEventRequest struct {
	Version        int             `json:"version"`
	Data           json.RawMessage `json:"data"`
	IdempotencyKey string          `json:"idempotencyKey"`
}

func topicKey(site, name string, version int) string {
	return internalKey(site, name, fmt.Sprint(version))
}

func (s *Server) eventsEnabled() bool { return s.siteAPIEnabled() && s.automationsEnabled() }

func (s *Server) readEventTopic(ctx context.Context, site, name string, version int) (eventTopic, error) {
	document, err := s.config.Database.Get(ctx, systemSite, "event-types", topicKey(site, name, version))
	if err != nil {
		return eventTopic{}, err
	}
	var topic eventTopic
	err = json.Unmarshal(document.Data, &topic)
	return topic, err
}

func (s *Server) eventSubscriberAllowed(ctx context.Context, subscription EventSubscription) error {
	principal, err := s.sitePrincipal(ctx, subscription.Site)
	if err != nil {
		return err
	}
	if principal.ID != subscription.Principal {
		return ErrForbidden
	}
	access, exists, err := s.sitePolicy(ctx, subscription.Producer)
	if err != nil {
		return err
	}
	authorization := siteAuthorization{identity: principal, access: access, role: roleViewer, apiOnly: true}
	if !exists || !authorization.permits("event", subscription.Event, "subscribe") {
		return ErrForbidden
	}
	return nil
}

func (s *Server) publishEvent(ctx context.Context, site, name string, authorization siteAuthorization, request publishEventRequest, dryRun bool) (DurableEvent, error) {
	if !namePattern.MatchString(name) || request.Version < 1 || len(request.IdempotencyKey) < 1 || len(request.IdempotencyKey) > 128 || len(request.Data) > maxEventBytes {
		return DurableEvent{}, &ActionError{Message: "events require a name, positive version, 1–128 byte idempotencyKey and data of at most 64 KiB"}
	}
	if !authorization.permits("event", name, "publish") {
		return DurableEvent{}, ErrForbidden
	}
	topic, err := s.readEventTopic(ctx, site, name, request.Version)
	if err != nil {
		return DurableEvent{}, err
	}
	schema, err := compileActionSchema(topic.Schema)
	if err != nil {
		return DurableEvent{}, err
	}
	if err := validateActionJSON(schema, request.Data); err != nil {
		return DurableEvent{}, &actionInputError{err: err}
	}
	id := internalKey(site, name, fmt.Sprint(request.Version), authorization.creator(), request.IdempotencyKey)
	event := DurableEvent{ID: id, Site: site, Name: name, Version: request.Version, Actor: authorization.creator(), Data: request.Data, CreatedAt: time.Now().UTC()}
	if dryRun {
		return event, nil
	}
	record := eventRecord{DurableEvent: event, Fingerprint: internalKey(string(request.Data)), Deliveries: map[string]EventDelivery{}}
	for key, subscription := range topic.Subscriptions {
		if subscription.Disabled || !subscription.Where.Matches(Document{Data: request.Data}) {
			continue
		}
		if err := s.eventSubscriberAllowed(ctx, subscription); errors.Is(err, ErrForbidden) {
			continue
		} else if err != nil {
			return DurableEvent{}, err
		}
		record.Deliveries[key] = EventDelivery{Subscription: subscription, Status: "pending", NextAttempt: event.CreatedAt}
	}
	record.Pending = len(record.Deliveries) > 0
	_, err = s.mutateSystem(ctx, "events", id, func(current *Document) (json.RawMessage, error) {
		if current != nil {
			var previous eventRecord
			if err := json.Unmarshal(current.Data, &previous); err != nil {
				return nil, err
			}
			if previous.Fingerprint != record.Fingerprint {
				return nil, errInvocationConflict
			}
			event = previous.DurableEvent
			return current.Data, nil
		}
		// The event and its complete fan-out are committed in one guarded mutation.
		return json.Marshal(record)
	})
	return event, err
}

func decodeEventRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxActionInputBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(w, 400, "invalid event request")
		return false
	}
	return true
}

func eventOwner(w http.ResponseWriter, r *http.Request) bool {
	if requestAuthorization(r).role != roleOwner {
		writeError(w, 403, "site ownership is required")
		return false
	}
	return true
}

func (s *Server) eventTypeHTTP(w http.ResponseWriter, r *http.Request) {
	if !eventOwner(w, r) {
		return
	}
	var definition EventType
	if !decodeEventRequest(w, r, &definition) {
		return
	}
	definition.Name = r.PathValue("name")
	if !namePattern.MatchString(definition.Name) || definition.Version < 1 || len(definition.Description) > 500 {
		writeError(w, 400, "invalid event definition")
		return
	}
	if _, err := compileActionSchema(definition.Schema); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	_, err := s.mutateSystem(r.Context(), "event-types", topicKey(r.PathValue("site"), definition.Name, definition.Version), func(current *Document) (json.RawMessage, error) {
		topic := eventTopic{EventType: definition, Subscriptions: map[string]EventSubscription{}}
		if current != nil {
			if err := json.Unmarshal(current.Data, &topic); err != nil {
				return nil, err
			}
			var oldSchema, newSchema any
			if err := json.Unmarshal(topic.Schema, &oldSchema); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(definition.Schema, &newSchema); err != nil {
				return nil, err
			}
			oldJSON, _ := json.Marshal(oldSchema)
			newJSON, _ := json.Marshal(newSchema)
			if string(oldJSON) != string(newJSON) {
				return nil, &ActionError{Message: "event schemas are immutable; publish a new version"}
			}
			topic.EventType = definition
		}
		return json.Marshal(topic)
	})
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	writeJSON(w, 200, definition)
}

func (s *Server) eventSubscriptionHTTP(w http.ResponseWriter, r *http.Request) {
	if !eventOwner(w, r) {
		return
	}
	var request struct {
		Producer   string           `json:"producer"`
		Event      string           `json:"event"`
		Version    int              `json:"version"`
		Automation string           `json:"automation"`
		Disabled   bool             `json:"disabled,omitempty"`
		Where      *RecordCondition `json:"where,omitempty"`
	}
	if !decodeEventRequest(w, r, &request) {
		return
	}
	site, id := r.PathValue("site"), r.PathValue("id")
	if !siteNamePattern.MatchString(request.Producer) || !namePattern.MatchString(request.Event) || !namePattern.MatchString(id) || request.Version < 1 {
		writeError(w, 400, "invalid subscription")
		return
	}
	if err := validateRecordCondition(request.Where); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	definitions, err := s.config.Automations.ListSiteAutomations(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	index := slices.IndexFunc(definitions, func(a ScheduledAutomation) bool { return a.Automation.Name == request.Automation })
	if index < 0 {
		writeError(w, 400, "subscription automation must be deployed first")
		return
	}
	principal, err := s.sitePrincipal(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	generation, err := newID()
	if err != nil {
		writeServerError(w, err)
		return
	}
	subscription := EventSubscription{ID: id, Site: site, Principal: principal.ID, Producer: request.Producer, Event: request.Event, Version: request.Version, Automation: request.Automation, Revision: definitions[index].Revision, Generation: generation, Disabled: request.Disabled, Where: request.Where}
	if !subscription.Disabled {
		if err := s.eventSubscriberAllowed(r.Context(), subscription); err != nil {
			writeSiteCallError(w, err)
			return
		}
	}
	_, err = s.mutateSystem(r.Context(), "event-types", topicKey(request.Producer, request.Event, request.Version), func(current *Document) (json.RawMessage, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		var topic eventTopic
		if err := json.Unmarshal(current.Data, &topic); err != nil {
			return nil, err
		}
		key := internalKey(site, id)
		if _, exists := topic.Subscriptions[key]; !exists && len(topic.Subscriptions) >= maxEventSubscribers {
			return nil, &ActionError{Message: "event type already has 32 subscriptions"}
		}
		topic.Subscriptions[key] = subscription
		return json.Marshal(topic)
	})
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	writeJSON(w, 200, subscription)
}

func (s *Server) publishEventHTTP(w http.ResponseWriter, r *http.Request) {
	var request publishEventRequest
	if !decodeEventRequest(w, r, &request) {
		return
	}
	event, err := s.publishEvent(r.Context(), r.PathValue("site"), r.PathValue("name"), requestAuthorization(r), request, false)
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	writeJSON(w, 202, event)
}

func (s *Server) inspectEventHTTP(w http.ResponseWriter, r *http.Request) {
	if !eventOwner(w, r) {
		return
	}
	document, err := s.config.Database.Get(r.Context(), systemSite, "events", r.PathValue("id"))
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	var record eventRecord
	if err := json.Unmarshal(document.Data, &record); err != nil {
		writeServerError(w, err)
		return
	}
	site := r.PathValue("site")
	if record.Site != site {
		for key, delivery := range record.Deliveries {
			if delivery.Subscription.Site != site {
				delete(record.Deliveries, key)
			} else if err := s.eventSubscriberAllowed(r.Context(), delivery.Subscription); err != nil {
				writeSiteCallError(w, err)
				return
			}
		}
		if len(record.Deliveries) == 0 {
			writeSiteCallError(w, ErrNotFound)
			return
		}
	}
	writeJSON(w, 200, record)
}

func (s *Server) replayEventHTTP(w http.ResponseWriter, r *http.Request) {
	if !eventOwner(w, r) {
		return
	}
	site := r.PathValue("site")
	key := internalKey(site, r.PathValue("subscription"))
	document, err := s.config.Database.Get(r.Context(), systemSite, "events", r.PathValue("id"))
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	var snapshot eventRecord
	if err := json.Unmarshal(document.Data, &snapshot); err != nil {
		writeServerError(w, err)
		return
	}
	if _, ok := snapshot.Deliveries[key]; !ok {
		writeSiteCallError(w, ErrNotFound)
		return
	}
	topic, err := s.readEventTopic(r.Context(), snapshot.Site, snapshot.Name, snapshot.Version)
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	subscription, ok := topic.Subscriptions[key]
	if !ok || subscription.Disabled {
		writeSiteCallError(w, ErrForbidden)
		return
	}
	if err := s.eventSubscriberAllowed(r.Context(), subscription); err != nil {
		writeSiteCallError(w, err)
		return
	}
	_, err = s.mutateSystem(r.Context(), "events", r.PathValue("id"), func(current *Document) (json.RawMessage, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		var record eventRecord
		if err := json.Unmarshal(current.Data, &record); err != nil {
			return nil, err
		}
		delivery, ok := record.Deliveries[key]
		if !ok {
			return nil, ErrNotFound
		}
		if delivery.Status != "dead" {
			return nil, &ActionError{Message: "only dead deliveries can be replayed"}
		}
		delivery.Status = "pending"
		delivery.Subscription = subscription
		delivery.Attempts = 0
		delivery.NextAttempt = time.Now().UTC()
		delivery.Error = ""
		record.Deliveries[key] = delivery
		record.Pending = true
		return json.Marshal(record)
	})
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"queued": true})
}

func (s *Server) registerEventRoutes() {
	if !s.eventsEnabled() {
		return
	}
	s.mux.HandleFunc("PUT /api/sites/{site}/events/types/{name}", s.siteScoped(s.eventTypeHTTP))
	s.mux.HandleFunc("PUT /api/sites/{site}/events/subscriptions/{id}", s.siteScoped(s.eventSubscriptionHTTP))
	s.mux.HandleFunc("POST /api/sites/{site}/events/publish/{name}", s.siteScoped(s.publishEventHTTP))
	s.mux.HandleFunc("GET /api/sites/{site}/events/records/{id}", s.siteScoped(s.inspectEventHTTP))
	s.mux.HandleFunc("POST /api/sites/{site}/events/records/{id}/replay/{subscription}", s.siteScoped(s.replayEventHTTP))
}

func (s *Server) runEventDispatcher(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	after := ""
	for {
		next, err := s.dispatchEventPage(ctx, after)
		if err != nil && ctx.Err() == nil {
			slog.Error("dispatch events", "error", err)
		}
		after = next
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// DispatchEvents processes a bounded page; useful to hosts and deterministic
// recovery tests. RunBackground continually scans all pending pages.
func (s *Server) DispatchEvents(ctx context.Context) error {
	_, err := s.dispatchEventPage(ctx, "")
	return err
}

func (s *Server) dispatchEventPage(ctx context.Context, after string) (string, error) {
	database, ok := s.config.Database.(GuardedDatabase)
	if !ok || !s.eventsEnabled() {
		return "", ErrAuthorizationUnsupported
	}
	documents, err := database.ListMatching(ctx, systemSite, "events", ListOptions{After: after, Limit: 32}, &RecordCondition{Field: "data.pending", Op: "eq", Value: json.RawMessage(`true`)})
	if err != nil {
		return after, err
	}
	for _, document := range documents {
		var record eventRecord
		if err := json.Unmarshal(document.Data, &record); err != nil {
			return after, err
		}
		for key := range record.Deliveries {
			if err := s.deliverEvent(ctx, record.ID, key); err != nil {
				return after, err
			}
		}
		after = document.ID
	}
	if len(documents) < 32 {
		after = ""
	}
	return after, nil
}

func (s *Server) deliverEvent(ctx context.Context, id, key string) error {
	select {
	case s.automationSlots <- struct{}{}:
		defer func() { <-s.automationSlots }()
	default:
		return nil
	}
	lease, err := newID()
	if err != nil {
		return err
	}
	var record eventRecord
	var delivery EventDelivery
	claimed := false
	_, err = s.mutateSystem(ctx, "events", id, func(current *Document) (json.RawMessage, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		if err := json.Unmarshal(current.Data, &record); err != nil {
			return nil, err
		}
		delivery = record.Deliveries[key]
		now := time.Now().UTC()
		if (delivery.Status != "pending" && delivery.Status != "running") || delivery.NextAttempt.After(now) || delivery.LeaseUntil.After(now) {
			return current.Data, nil
		}
		delivery.Attempts++
		delivery.Status = "running"
		delivery.Lease = lease
		delivery.LeaseUntil = now.Add(automationRunTimeout + time.Minute)
		record.Deliveries[key] = delivery
		claimed = true
		return json.Marshal(record)
	})
	if err != nil || !claimed {
		return err
	}
	var executionError error
	if delivery.Attempts > maxDeliveryAttempts {
		executionError = errors.New("delivery exhausted its attempts after lease recovery")
	} else {
		executionError = s.executeEventDelivery(ctx, record.DurableEvent, &delivery)
	}
	finish, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = s.mutateSystem(finish, "events", id, func(current *Document) (json.RawMessage, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		var latest eventRecord
		if err := json.Unmarshal(current.Data, &latest); err != nil {
			return nil, err
		}
		if latest.Deliveries[key].Lease != lease {
			return current.Data, nil
		}
		delivery.Lease = ""
		delivery.LeaseUntil = time.Time{}
		delivery.Status = "succeeded"
		delivery.Error = ""
		if executionError != nil {
			delivery.Error = boundedScriptText(executionError.Error(), 8192)
			delivery.Status = "pending"
			delivery.NextAttempt = time.Now().UTC().Add(time.Second * time.Duration(1<<min(delivery.Attempts, 10)))
			if delivery.Attempts >= maxDeliveryAttempts || errors.Is(executionError, ErrForbidden) || errors.Is(executionError, ErrNotFound) {
				delivery.Status = "dead"
			}
		}
		latest.Deliveries[key] = delivery
		latest.Pending = false
		for _, entry := range latest.Deliveries {
			if entry.Status == "pending" || entry.Status == "running" {
				latest.Pending = true
			}
		}
		return json.Marshal(latest)
	})
	return err
}

func (s *Server) executeEventDelivery(ctx context.Context, event DurableEvent, delivery *EventDelivery) error {
	subscription := delivery.Subscription
	topic, err := s.readEventTopic(ctx, event.Site, event.Name, event.Version)
	if err != nil {
		return err
	}
	current, ok := topic.Subscriptions[internalKey(subscription.Site, subscription.ID)]
	if !ok || current.Disabled || current.Generation != subscription.Generation {
		return ErrForbidden
	}
	if err := s.eventSubscriberAllowed(ctx, subscription); err != nil {
		return err
	}
	definitions, err := s.config.Automations.ListSiteAutomations(ctx, subscription.Site)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(definitions, func(a ScheduledAutomation) bool { return a.Automation.Name == subscription.Automation })
	if index < 0 {
		return ErrNotFound
	}
	definition := definitions[index]
	if definition.Automation.Disabled || definition.Revision != subscription.Revision {
		return ErrForbidden
	}
	runID, err := newID()
	if err != nil {
		return err
	}
	run := AutomationRun{ID: runID, Site: subscription.Site, Automation: subscription.Automation, Trigger: "event", Status: RunRunning, StartedAt: time.Now().UTC(), Revision: definition.Revision, Event: &event}
	delivery.RunID = runID
	if err := s.config.Automations.RecordAutomationRun(ctx, run); err != nil {
		return err
	}
	s.runAutomation(ctx, subscription.Site, definition.Automation, &run)
	if run.Status != RunSucceeded {
		return errors.New(run.Error)
	}
	return nil
}
