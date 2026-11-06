package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Integration events let automations react to things that happen in a
// third-party system, such as a Slack message or a new Sentry issue. An
// integration declares its event types next to its endpoints, and the host
// feeds events in with Server.PublishIntegrationEvent, typically from a
// verified webhook. They then travel the ordinary durable event pipeline:
// sites subscribe an automation to the producer "integration:<name>", and
// each event is delivered at least once with retries, filters and replay.
//
// Nobody can publish an integration event through the API. Subscribing
// needs the event's platform permission granted to "site:<name>" (or "*")
// in Config.IntegrationGrants, a site approval when the integration
// RequiresApproval, and, under version 2 site policies, an "integration"
// read grant on "<integration>.<event>" for the subscribing automation.
// Every delivery checks all three again, and Visible can narrow further
// which sites see which events (for example, only channels one of the
// site's owners can read).

// IntegrationEvent is one versioned event type an integration produces.
// Schema validates every published payload and is immutable per version:
// publish breaking changes as a new version. Permission defaults to
// "<integration>.events.<name>". Visible, when set, reports whether a
// subscribing automation may receive a particular event. Its call names the
// site and automation, with no Identity, and SiteOwners returns the site's
// owners. It runs once per matching subscription before the event is
// stored, so it should answer quickly (cache lookups) and fail closed.
type IntegrationEvent struct {
	Name        string
	Version     int
	Description string
	Permission  string
	Schema      json.RawMessage
	Visible     func(ctx context.Context, call IntegrationCall, data json.RawMessage) bool
}

// IntegrationEventInfo is an event type's discoverable contract.
type IntegrationEventInfo struct {
	Name        string          `json:"name"`
	Version     int             `json:"version"`
	Description string          `json:"description"`
	Permission  string          `json:"permission"`
	Producer    string          `json:"producer"`
	Allowed     bool            `json:"allowed"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

const integrationProducerPrefix = "integration:"

type registeredEvent struct {
	integration *registeredIntegration
	event       IntegrationEvent
	schema      *jsonschema.Schema
}

func (e *registeredEvent) permission() string {
	if e.event.Permission != "" {
		return e.event.Permission
	}
	return e.integration.integration.Name + ".events." + e.event.Name
}

// qualifiedName is the resource version 2 site policies grant, in the same
// "<integration>.<name>" namespace as endpoints, which is why event and
// endpoint names of one integration must differ.
func (e *registeredEvent) qualifiedName() string {
	return e.integration.integration.Name + "." + e.event.Name
}

func (e *registeredEvent) producer() string {
	return integrationProducerPrefix + e.integration.integration.Name
}

func eventKey(name string, version int) string { return fmt.Sprintf("%s@%d", name, version) }

// integrationProducerName returns the integration an event producer names,
// such as "slack" for "integration:slack".
func integrationProducerName(producer string) (string, bool) {
	name, ok := strings.CutPrefix(producer, integrationProducerPrefix)
	return name, ok && integrationNamePattern.MatchString(name)
}

func validEventProducer(producer string) bool {
	if _, ok := integrationProducerName(producer); ok {
		return true
	}
	return siteNamePattern.MatchString(producer)
}

func compileIntegrationEvent(integration *registeredIntegration, event IntegrationEvent) (*registeredEvent, error) {
	if !integrationNamePattern.MatchString(event.Name) {
		return nil, fmt.Errorf("invalid event name %q; use lowercase letters, digits and -", event.Name)
	}
	label := fmt.Sprintf("event %s version %d", event.Name, event.Version)
	if event.Version < 1 {
		return nil, fmt.Errorf("event %s needs a positive version", event.Name)
	}
	description := strings.TrimSpace(event.Description)
	if description == "" || len(description) > maxIntegrationDescriptionSize {
		return nil, fmt.Errorf("%s needs a description of at most %d characters", label, maxIntegrationDescriptionSize)
	}
	if event.Permission != "" && !permissionPattern.MatchString(event.Permission) {
		return nil, fmt.Errorf("%s: invalid permission %q", label, event.Permission)
	}
	if len(event.Schema) == 0 {
		return nil, fmt.Errorf("%s needs a schema", label)
	}
	schema, err := compileActionSchema(event.Schema)
	if err != nil {
		return nil, fmt.Errorf("%s schema: %w", label, err)
	}
	event.Schema = slices.Clone(event.Schema)
	return &registeredEvent{integration: integration, event: event, schema: schema}, nil
}

func (r *IntegrationRegistry) event(integrationName, name string, version int) *registeredEvent {
	integration := r.integration(integrationName)
	if integration == nil {
		return nil
	}
	return integration.events[eventKey(name, version)]
}

func (i *registeredIntegration) sortedEvents() []*registeredEvent {
	result := make([]*registeredEvent, 0, len(i.events))
	for _, event := range i.integration.Events {
		result = append(result, i.events[eventKey(event.Name, event.Version)])
	}
	return result
}

func eventInfo(event *registeredEvent, allowed bool) IntegrationEventInfo {
	return IntegrationEventInfo{
		Name: event.event.Name, Version: event.event.Version, Description: event.event.Description,
		Permission: event.permission(), Producer: event.producer(), Allowed: allowed, Schema: event.event.Schema,
	}
}

// forbiddenError is a denial with a reason the site owner can act on.
type forbiddenError struct{ message string }

func (e *forbiddenError) Error() string        { return e.message }
func (e *forbiddenError) Is(target error) bool { return target == ErrForbidden }

// integrationSubscriberAllowed checks that a subscription's automation may
// receive an integration event: the platform grant, the site approval and,
// for version 2 policies, the site's own integration grant.
func (s *Server) integrationSubscriberAllowed(ctx context.Context, integration string, subscription EventSubscription) error {
	event := s.config.Integrations.event(integration, subscription.Event, subscription.Version)
	if event == nil {
		return &forbiddenError{fmt.Sprintf("integration %s has no event %s version %d", integration, subscription.Event, subscription.Version)}
	}
	authorization, err := s.automationAuthorization(ctx, subscription.Site, subscription.Automation)
	if err != nil {
		return err
	}
	caller := integrationCaller{site: subscription.Site, automation: subscription.Automation, authorization: &authorization}
	if !s.granted(caller, event.permission()) {
		return &forbiddenError{fmt.Sprintf("site:%s needs the %s platform permission; ask a platform admin", subscription.Site, event.permission())}
	}
	if authorization.access.Version == 2 && !authorization.permits("integration", event.qualifiedName(), "read") {
		return &forbiddenError{fmt.Sprintf("the site's permission policy must grant automation:%s integration read on %s", subscription.Automation, event.qualifiedName())}
	}
	if event.integration.integration.RequiresApproval {
		status, err := s.approvalStatus(ctx, subscription.Site, integration)
		if err != nil {
			return err
		}
		if status != ApprovalApproved {
			return &forbiddenError{fmt.Sprintf("%s needs a platform admin to approve %s", event.integration.integration.Title, subscription.Site)}
		}
	}
	return nil
}

// eventAllowedForSite reports whether the site's automations hold the
// platform permission for the event, for discovery. Version 2 grants are
// per automation and are checked when subscribing.
func (s *Server) eventAllowedForSite(site string, approved bool, event *registeredEvent) bool {
	return approved && s.granted(integrationCaller{site: site, automation: "*"}, event.permission())
}

// ensureIntegrationTopic stores the event type the first time it is used,
// so subscriptions have a topic to live in, and returns its current state.
func (s *Server) ensureIntegrationTopic(ctx context.Context, event *registeredEvent) (eventTopic, error) {
	producer := event.producer()
	definition := EventType{Name: event.event.Name, Version: event.event.Version, Description: event.event.Description, Schema: event.event.Schema}
	var topic eventTopic
	_, err := s.mutateSystem(ctx, "event-types", topicKey(producer, definition.Name, definition.Version), func(current *Document) (json.RawMessage, error) {
		if current == nil {
			topic = eventTopic{Site: producer, EventType: definition, Subscriptions: map[string]EventSubscription{}}
			return json.Marshal(topic)
		}
		// Decode into a fresh value: json.RawMessage fields reuse their
		// backing array, which would overwrite the registered schema.
		topic = eventTopic{}
		if err := json.Unmarshal(current.Data, &topic); err != nil {
			return nil, err
		}
		if topic.Subscriptions == nil {
			topic.Subscriptions = map[string]EventSubscription{}
		}
		same, err := sameJSON(topic.Schema, definition.Schema)
		if err != nil {
			return nil, err
		}
		if !same {
			return nil, fmt.Errorf("the schema of %s %s version %d changed; integrations must publish schema changes as a new version", producer, definition.Name, definition.Version)
		}
		if topic.Description == definition.Description {
			return current.Data, nil
		}
		topic.Description = definition.Description
		return json.Marshal(topic)
	})
	return topic, err
}

func sameJSON(a, b json.RawMessage) (bool, error) {
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &right); err != nil {
		return false, err
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON), nil
}

// ErrEventsUnavailable reports that the platform runs without durable
// events (they need the site API and automations).
var ErrEventsUnavailable = errors.New("durable events are not enabled on this platform")

// PublishIntegrationEvent records an event an integration observed and
// queues it for every subscribed automation that may receive it. data is
// a json.RawMessage or any value that marshals to JSON; it must match the
// event's schema. idempotencyKey (1–128 bytes) identifies the occurrence,
// such as the provider's own event ID, so provider retries publish once.
//
// Events nobody receives are not stored. The returned event has an empty
// ID in that case, and repeated calls are then not deduplicated.
func (s *Server) PublishIntegrationEvent(ctx context.Context, integration, name string, version int, idempotencyKey string, data any) (DurableEvent, error) {
	if !s.eventsEnabled() {
		return DurableEvent{}, ErrEventsUnavailable
	}
	event := s.config.Integrations.event(integration, name, version)
	if event == nil {
		return DurableEvent{}, fmt.Errorf("integration %s has no event %s version %d", integration, name, version)
	}
	if len(idempotencyKey) < 1 || len(idempotencyKey) > 128 {
		return DurableEvent{}, errors.New("integration events need an idempotency key of 1–128 bytes")
	}
	payload, err := event.validate(data)
	if err != nil {
		return DurableEvent{}, err
	}
	topic, err := s.ensureIntegrationTopic(ctx, event)
	if err != nil {
		return DurableEvent{}, err
	}
	producer := event.producer()
	published := DurableEvent{
		ID:   internalKey(producer, name, fmt.Sprint(version), producer, idempotencyKey),
		Site: producer, Name: name, Version: version, Actor: producer, Data: payload, CreatedAt: time.Now().UTC(),
	}
	visible := event.event.Visible
	return s.fanOutEvent(ctx, topic, published, true, func(subscription EventSubscription) bool {
		return visible == nil || visible(ctx, s.integrationCall(subscription.Site, nil, subscription.Automation), payload)
	})
}

// ValidateEvent checks data against an event's schema and size limit, as
// PublishIntegrationEvent does; hosts use it to test their webhook adapters.
func (r *IntegrationRegistry) ValidateEvent(integration, name string, version int, data any) error {
	event := r.event(integration, name, version)
	if event == nil {
		return fmt.Errorf("integration %s has no event %s version %d", integration, name, version)
	}
	_, err := event.validate(data)
	return err
}

func (e *registeredEvent) validate(data any) (json.RawMessage, error) {
	payload, ok := data.(json.RawMessage)
	if !ok {
		var err error
		if payload, err = json.Marshal(data); err != nil {
			return nil, fmt.Errorf("encode event data: %w", err)
		}
	}
	if len(payload) > maxEventBytes {
		return nil, fmt.Errorf("event data is %d bytes; at most %d are allowed", len(payload), maxEventBytes)
	}
	if err := validateActionJSON(e.schema, payload); err != nil {
		return nil, fmt.Errorf("%s version %d does not match its schema: %w", e.qualifiedName(), e.event.Version, err)
	}
	return payload, nil
}
