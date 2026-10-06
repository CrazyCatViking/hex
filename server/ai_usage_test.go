package hex_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/crazycatviking/hex/server/providers/openai"
)

const accountingRequest = `{"model":"general","messages":[{"role":"user","content":[{"type":"text","text":"Hello, please answer this question."}]}]}`

func accountingServer(provider hex.AIProvider, store hex.AIUsageStore, limits hex.AILimits) *hex.Server {
	return hex.New(hex.Config{
		Identity: easyauth.Resolver{}, Access: memory.NewAccessStore(),
		IntegrationGrants: []hex.IntegrationGrant{{Principal: "*", Permissions: []string{"ai"}}},
		AI:                &hex.AIConfig{Provider: provider, Limits: limits}, AIUsage: store,
	})
}

type failingSettlementStore struct {
	*memory.AIUsageStore
	failures int
	lostAck  bool
	records  []hex.AIUsageRecord
}

func (s *failingSettlementStore) SettleAIUsage(ctx context.Context, record hex.AIUsageRecord) error {
	s.records = append(s.records, record)
	if s.failures > 0 {
		s.failures--
		return errors.New("accounting unavailable")
	}
	if err := s.AIUsageStore.SettleAIUsage(ctx, record); err != nil {
		return err
	}
	if s.lostAck {
		s.lostAck = false
		return errors.New("commit acknowledgement lost")
	}
	return nil
}

func TestAISettlementRetriesOriginalIDAndRetainsFailedSpend(t *testing.T) {
	for _, lostAck := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed settlement", true: "lost acknowledgement"}[lostAck], func(t *testing.T) {
			store := &failingSettlementStore{AIUsageStore: memory.NewAIUsageStore(), lostAck: lostAck}
			if !lostAck {
				store.failures = 3
			}
			provider := &scriptedProvider{turns: [][]hex.AIEvent{assistantTurn(hex.StopEndTurn, hex.AIContent{Type: hex.ContentText, Text: "Hello"})}}
			server := accountingServer(provider, store, hex.AILimits{SiteMonthly: 0.00003})
			status := 502
			if lostAck {
				status = 200
			}
			requestAs(t, server, roleHeaders("person"), "POST", "/api/sites/demo/ai/complete", []byte(accountingRequest), status)
			if len(store.records) < 2 {
				t.Fatal("settlement was not retried")
			}
			for _, record := range store.records {
				if record.ID != store.records[0].ID || !record.At.Equal(store.records[0].At) {
					t.Fatal("retry changed the original record identity")
				}
			}
			// Either durable settled spending or the unresolved hold must deny
			// another call, even through a separately constructed server.
			replica := accountingServer(provider, store, hex.AILimits{SiteMonthly: 0.00003})
			requestAs(t, replica, roleHeaders("person"), "POST", "/api/sites/demo/ai/complete", []byte(accountingRequest), 429)
			if len(provider.requests) != 1 {
				t.Fatal("unrecorded spending admitted another upstream call")
			}
			if err := store.AIUsageStore.SettleAIUsage(context.Background(), store.records[0]); err != nil {
				t.Fatal(err)
			}
			if err := store.AIUsageStore.SettleAIUsage(context.Background(), store.records[0]); err != nil {
				t.Fatal(err)
			}
			totals, err := store.AIUsageTotals(context.Background(), hex.AIUsageFilter{}, hex.GroupBySite)
			if err != nil || len(totals) != 1 || totals[0].Calls != 1 || totals[0].CostMicros != 35 {
				t.Fatalf("settlement was not exactly once: %+v %v", totals, err)
			}
		})
	}
}

func TestAIMissingUsageIsEstimatedButExplicitZeroIsKnown(t *testing.T) {
	for _, unknown := range []bool{true, false} {
		t.Run(map[bool]string{true: "unknown", false: "explicit zero"}[unknown], func(t *testing.T) {
			turn := assistantTurn(hex.StopEndTurn, hex.AIContent{Type: hex.ContentText, Text: "A fairly long answer."})
			turn[len(turn)-1].Usage = nil
			if !unknown {
				turn[len(turn)-1].Usage = &hex.AIUsage{}
			}
			provider := &scriptedProvider{turns: [][]hex.AIEvent{turn}}
			server, store := setupAI(t, provider, hex.AILimits{SiteMonthly: 1})
			response := requestAs(t, server, roleHeaders("person"), "POST", "/api/sites/demo/ai/complete", []byte(accountingRequest), 200)
			if strings.Contains(response.Body.String(), `"estimated":true`) != unknown {
				t.Fatalf("incorrect usage certainty: %s", response.Body.String())
			}
			totals, err := store.AIUsageTotals(context.Background(), hex.AIUsageFilter{}, hex.GroupBySite)
			if err != nil || len(totals) != 1 || totals[0].Calls != 1 {
				t.Fatalf("missing call: %+v %v", totals, err)
			}
			if unknown && (totals[0].InputTokens == 0 || totals[0].Estimated != 1 || totals[0].CostMicros != 30000) {
				t.Fatalf("unknown usage did not conservatively settle the ceiling: %+v", totals)
			}
			if !unknown && (totals[0].CostMicros != 0 || totals[0].Estimated != 0) {
				t.Fatalf("explicit zero was estimated: %+v", totals)
			}
		})
	}
}

type blockingAIProvider struct {
	scriptedProvider
	started chan struct{}
	release chan struct{}
}

func (p *blockingAIProvider) Stream(ctx context.Context, request hex.AIRequest) (hex.AIStream, error) {
	close(p.started)
	select {
	case <-p.release:
		return p.scriptedProvider.Stream(ctx, request)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestAIAdmissionAcrossServersCountsInFlightSpend(t *testing.T) {
	provider := &blockingAIProvider{scriptedProvider: scriptedProvider{turns: [][]hex.AIEvent{assistantTurn(hex.StopEndTurn)}}, started: make(chan struct{}), release: make(chan struct{})}
	store := memory.NewAIUsageStore()
	limits := hex.AILimits{SiteMonthly: 0.001}
	first := accountingServer(provider, store, limits)
	second := accountingServer(provider, store, limits)
	request := httptest.NewRequest("POST", "/api/sites/demo/ai/complete", strings.NewReader(accountingRequest))
	request.Header = roleHeaders("alice")
	request.Header.Set("X-Hex-Request", "1")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		first.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first call did not start")
	}
	requestAs(t, second, roleHeaders("bob"), "POST", "/api/sites/demo/ai/complete", []byte(accountingRequest), 429)
	close(provider.release)
	<-done
	if response.Code != 200 {
		t.Fatalf("first call failed: %s", response.Body.String())
	}
}

func TestAIUsageReservationsBoundOvercommitAndSurviveMonthRollover(t *testing.T) {
	ctx := context.Background()
	store := memory.NewAIUsageStore()
	at := time.Now().UTC().AddDate(0, -1, 0)
	check := []hex.AIBudgetCheck{{Filter: hex.AIUsageFilter{Site: "demo", Since: time.Now().UTC()}, LimitMicros: 100}}
	var mu sync.Mutex
	var admitted []hex.AIUsageReservation
	var wait sync.WaitGroup
	for index := 0; index < 30; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			hold := hex.AIUsageReservation{ID: time.Unix(int64(index), 0).String(), At: at, Site: "demo", Caller: "user:a", CostMicros: 30}
			if err := store.ReserveAIUsage(ctx, hold, check); err == nil {
				mu.Lock()
				admitted = append(admitted, hold)
				mu.Unlock()
			} else {
				var exceeded *hex.AIBudgetExceededError
				if !errors.As(err, &exceeded) {
					t.Errorf("unexpected admission error: %v", err)
				}
			}
		}(index)
	}
	wait.Wait()
	if len(admitted) != 4 {
		t.Fatalf("expected bounded final-call overshoot (120 held), admitted %d calls", len(admitted))
	}
	// A failed or mismatched settlement cannot free the hold.
	bad := hex.AIUsageRecord{ID: admitted[0].ID, At: at, Site: "other", Caller: "user:a", CostMicros: 30}
	if err := store.SettleAIUsage(ctx, bad); err == nil {
		t.Fatal("mismatched settlement accepted")
	}
	if err := store.ReserveAIUsage(ctx, hex.AIUsageReservation{ID: "blocked", At: at, Site: "demo", CostMicros: 30}, check); err == nil {
		t.Fatal("failed settlement released the reservation")
	}
}

func TestAIUsageCallerNameUsesTimestampThenID(t *testing.T) {
	store := memory.NewAIUsageStore()
	at := time.Now().UTC()
	for _, record := range []hex.AIUsageRecord{
		{ID: "z", At: at, Caller: "user:a", CallerName: "Newest tie winner"},
		{ID: "a", At: at, Caller: "user:a", CallerName: "Tie loser"},
		{ID: "zz", At: at.Add(-time.Hour), Caller: "user:a", CallerName: "Late import of older name"},
	} {
		if err := store.RecordAIUsage(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	totals, err := store.AIUsageTotals(context.Background(), hex.AIUsageFilter{}, hex.GroupByCaller)
	if err != nil || len(totals) != 1 || totals[0].Name != "Newest tie winner" {
		t.Fatalf("name was selected by insertion order: %+v %v", totals, err)
	}
}

func TestAIReservationMetadataCannotBeMutatedThroughListing(t *testing.T) {
	ctx := context.Background()
	store := memory.NewAIUsageStore()
	price := &hex.AIPrice{Input: 1, Output: 2}
	hold := hex.AIUsageReservation{ID: "snapshot", At: time.Now().UTC(), Site: "demo", Price: price, CostMicros: 50}
	if err := store.ReserveAIUsage(ctx, hold, nil); err != nil {
		t.Fatal(err)
	}
	price.Input = 999
	listed, err := store.ListAIReservations(ctx, hex.AIUsageFilter{Site: "demo"})
	if err != nil || len(listed) != 1 || listed[0].Price.Input != 1 || !listed[0].EstimatedUsage.Estimated {
		t.Fatalf("stored pricing was mutated or unknown usage represented as known zero: %+v %v", listed, err)
	}
	listed[0].Price.Input = 500
	listed, err = store.ListAIReservations(ctx, hex.AIUsageFilter{})
	if err != nil || listed[0].Price.Input != 1 {
		t.Fatal("listing mutated the durable reservation")
	}
}

type pricedCatalogProvider struct {
	scriptedProvider
	model         hex.AIModel
	catalogFailed bool
}

func (p *pricedCatalogProvider) Models(context.Context) ([]hex.AIModel, error) {
	if p.catalogFailed {
		return nil, errors.New("catalog failed after model call")
	}
	return []hex.AIModel{p.model}, nil
}

func (p *pricedCatalogProvider) Stream(ctx context.Context, request hex.AIRequest) (hex.AIStream, error) {
	p.catalogFailed = true
	return p.scriptedProvider.Stream(ctx, request)
}

type observedReservationStore struct {
	*memory.AIUsageStore
	reservations []hex.AIUsageReservation
}

func (s *observedReservationStore) ReserveAIUsage(ctx context.Context, hold hex.AIUsageReservation, checks []hex.AIBudgetCheck) error {
	s.reservations = append(s.reservations, hold)
	return s.AIUsageStore.ReserveAIUsage(ctx, hold, checks)
}

func TestAIReservationUsesAllPriceCategoriesAndSettlementSnapshotsPrice(t *testing.T) {
	zero := 0.0
	for _, test := range []struct {
		name    string
		price   hex.AIPrice
		ceiling int64
		cost    int64
	}{
		{"cache read is highest", hex.AIPrice{Input: 1, CachedInput: 10, CacheWrite: 4, Output: 2}, 1040, 124},
		{"cache write is highest", hex.AIPrice{Input: 1, CachedInput: 3, CacheWrite: 10, Output: 2}, 1040, 128},
		{"explicit free cache", hex.AIPrice{Input: 1, CachedInputOverride: &zero, CacheWriteOverride: &zero, Output: 2}, 140, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			turn := assistantTurn(hex.StopEndTurn)
			turn[len(turn)-1].Usage = &hex.AIUsage{InputTokens: 2, CachedInputTokens: 8, CacheWriteTokens: 10, OutputTokens: 1}
			provider := &pricedCatalogProvider{
				scriptedProvider: scriptedProvider{turns: [][]hex.AIEvent{turn}},
				model:            hex.AIModel{ID: "general", ContextTokens: 100, MaxOutputTokens: 20, Price: &test.price},
			}
			store := &observedReservationStore{AIUsageStore: memory.NewAIUsageStore()}
			server := accountingServer(provider, store, hex.AILimits{PlatformMonthly: 1})
			requestAs(t, server, roleHeaders("person"), "POST", "/api/sites/demo/ai/complete", []byte(accountingRequest), 200)
			if len(store.reservations) != 1 || store.reservations[0].CostMicros != test.ceiling {
				t.Fatalf("reservation missed a pricing category: %+v", store.reservations)
			}
			spent, err := store.SumAICost(context.Background(), hex.AIUsageFilter{})
			if err != nil || spent != test.cost {
				t.Fatalf("settlement fetched the failed catalog or mispriced usage: %d %v", spent, err)
			}
		})
	}
}

func TestAIBudgetEnforcementRequiresPriceAndModelBounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		model hex.AIModel
	}{
		{"unknown price", hex.AIModel{ID: "general", ContextTokens: 100}},
		{"unknown context bound", hex.AIModel{ID: "general", Price: &hex.AIPrice{Input: 1}}},
		{"invalid cache price", hex.AIModel{ID: "general", ContextTokens: 100, Price: &hex.AIPrice{Input: 1, CacheWrite: -1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &pricedCatalogProvider{model: test.model}
			store := &observedReservationStore{AIUsageStore: memory.NewAIUsageStore()}
			server := accountingServer(provider, store, hex.AILimits{SiteMonthly: 1})
			requestAs(t, server, roleHeaders("person"), "POST", "/api/sites/demo/ai/complete", []byte(accountingRequest), 503)
			if len(provider.requests) != 0 || len(store.reservations) != 0 {
				t.Fatal("unbounded/unpriced call contacted upstream or reserved funds")
			}
		})
	}
}

func TestAIToolLoopAcquiresAdmissionForEachRound(t *testing.T) {
	provider := &scriptedProvider{turns: [][]hex.AIEvent{
		assistantTurn(hex.StopToolUse, hex.AIContent{Type: hex.ContentToolCall, ToolCallID: "call", Name: "unknown", Input: []byte(`{}`)}),
	}}
	store := &observedReservationStore{AIUsageStore: memory.NewAIUsageStore()}
	server := accountingServer(provider, store, hex.AILimits{SiteMonthly: 0.00003})
	requestAs(t, server, roleHeaders("person"), "POST", "/api/sites/demo/ai/complete", []byte(accountingRequest), 429)
	if len(provider.requests) != 1 || len(store.reservations) != 2 {
		t.Fatalf("tool round bypassed admission: calls=%d admissions=%d", len(provider.requests), len(store.reservations))
	}
}

type firstCallFailureProvider struct {
	scriptedProvider
	failure error
}

func (p *firstCallFailureProvider) Stream(ctx context.Context, request hex.AIRequest) (hex.AIStream, error) {
	if p.failure != nil {
		err := p.failure
		p.failure = nil
		return nil, err
	}
	return p.scriptedProvider.Stream(ctx, request)
}

func TestAIStreamStartFailureDistinguishesNoSpendFromAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		status    int
		ambiguous bool
	}{
		{"preflight failure", &hex.AINoSpendError{Err: errors.New("credentials unavailable before sending")}, 502, false},
		{"definite provider rejection", &hex.AIError{Status: 400, Message: "invalid model request"}, 400, false},
		{"ambiguous failure after sending", errors.New("lost response after sending request"), 502, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &firstCallFailureProvider{
				scriptedProvider: scriptedProvider{turns: [][]hex.AIEvent{assistantTurn(hex.StopEndTurn)}},
				failure:          test.err,
			}
			store := memory.NewAIUsageStore()
			server := accountingServer(provider, store, hex.AILimits{SiteMonthly: 0.001})
			requestAs(t, server, roleHeaders("person"), "POST", "/api/sites/demo/ai/complete", []byte(accountingRequest), test.status)
			spend, err := store.AIBudgetSpend(context.Background(), hex.AIUsageFilter{Site: "demo"})
			want := int64(0)
			if test.ambiguous {
				want = 30000
			}
			if err != nil || spend.SettledMicros != want || spend.ReservedMicros != 0 || spend.Reservations != 0 {
				t.Fatalf("incorrect failure spending: %+v %v", spend, err)
			}
			status := 200
			if test.ambiguous {
				status = 429
			}
			requestAs(t, server, roleHeaders("person"), "POST", "/api/sites/demo/ai/complete", []byte(accountingRequest), status)
		})
	}
}

func TestAIRawOpenAIUsageCannotSilentlyBecomeZero(t *testing.T) {
	for _, test := range []struct {
		name    string
		usage   string
		unknown bool
	}{
		{"missing", "", true},
		{"empty object", `,"usage":{}`, true},
		{"partial", `,"usage":{"prompt_tokens":0}`, true},
		{"reported zero", `,"usage":{"prompt_tokens":0,"completion_tokens":0}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hello from upstream"},"finish_reason":"stop"}]`+test.usage+"}\n\ndata: [DONE]\n\n")
				if err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			provider, err := openai.New(openai.Config{
				BaseURL: upstream.URL, APIKey: "test-key",
				Models: []openai.Model{{AIModel: hex.AIModel{ID: "general", ContextTokens: 8000, MaxOutputTokens: 4000,
					Price: &hex.AIPrice{Input: 1, Output: 5}}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			store := memory.NewAIUsageStore()
			server := accountingServer(provider, store, hex.AILimits{SiteMonthly: 1})
			response := requestAs(t, server, roleHeaders("person"), "POST", "/api/sites/demo/ai/complete", []byte(accountingRequest), 200)
			if strings.Contains(response.Body.String(), `"estimated":true`) != test.unknown {
				t.Fatalf("raw upstream counts misrepresented: %s", response.Body.String())
			}
			spent, err := store.SumAICost(context.Background(), hex.AIUsageFilter{})
			want := int64(0)
			if test.unknown {
				want = 30000
			}
			if err != nil || spent != want {
				t.Fatalf("raw missing usage bypassed accounting: %d %v", spent, err)
			}
		})
	}
}
