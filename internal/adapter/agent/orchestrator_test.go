package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/verdu/alter/internal/application"
	"github.com/verdu/alter/internal/capability"
	"github.com/verdu/alter/internal/domain"
)

var fixedNow = time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)

// --- Test-only in-memory fakes ----------------------------------------------
// These exist solely to exercise the Orchestrator composite through the real
// AgentFlow pipeline (application.NewAgentFlow + NewCapabilityFlow over a test
// registry). They are never part of the runtime wiring (no real Pi transport
// exists yet). A shared `order` slice records the sequence of
// Agent -> Channel -> Event calls.

type fakeAgent struct {
	calls  []domain.AgentRequest
	result domain.AgentResult
	err    error
	order  *[]string
}

func (f *fakeAgent) Execute(_ context.Context, req domain.AgentRequest) (domain.AgentResult, error) {
	f.calls = append(f.calls, req)
	if f.order != nil {
		*f.order = append(*f.order, "agent")
	}
	return f.result, f.err
}

type fakeChannel struct {
	sent  []string
	err   error
	order *[]string
}

func (f *fakeChannel) Name() string { return "fake" }
func (f *fakeChannel) Send(_ context.Context, msg string) error {
	f.sent = append(f.sent, msg)
	if f.order != nil {
		*f.order = append(*f.order, "channel")
	}
	return f.err
}

type fakeEventStore struct {
	events []domain.Event
	err    error
	order  *[]string
}

func (f *fakeEventStore) Save(_ context.Context, e domain.Event) error {
	f.events = append(f.events, e)
	if f.order != nil {
		*f.order = append(*f.order, "event")
	}
	return f.err
}
func (f *fakeEventStore) ListByType(context.Context, domain.EventType) ([]domain.Event, error) {
	return nil, nil
}

// fakeSearcher records the queries/options it receives and returns canned
// results, so tests can assert retrieval behavior without a real index.
type fakeSearcher struct {
	results []domain.SearchResult
	err     error
	queries []string
	opts    []domain.SearchOptions
}

func (f *fakeSearcher) Search(_ context.Context, query string, opts domain.SearchOptions) ([]domain.SearchResult, error) {
	f.queries = append(f.queries, query)
	f.opts = append(f.opts, opts)
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

// fakeIndexer records index writes so tests can assert agent-result memory sync.
type fakeIndexer struct {
	events []domain.Event
	err    error
}

func (f *fakeIndexer) Reset(context.Context) error                  { return nil }
func (f *fakeIndexer) IndexTask(context.Context, domain.Task) error { return nil }
func (f *fakeIndexer) RemoveTask(context.Context, string) error     { return nil }
func (f *fakeIndexer) IndexEvent(_ context.Context, e domain.Event) error {
	f.events = append(f.events, e)
	return f.err
}

// echoCapabilityHandler is a test-only capability baked into the harness
// registry: it counts invocations and returns a fixed JSON-able result so tests
// can observe a plan being executed end to end.
type echoCapabilityHandler struct {
	calls *int
}

func (h echoCapabilityHandler) Execute(context.Context, json.RawMessage) (capability.CapabilityResult, error) {
	*h.calls++
	return capability.CapabilityResult{Data: map[string]any{"ok": true}}, nil
}

// --- Harness ----------------------------------------------------------------

// newHarness builds the Orchestrator over a real AgentFlow whose registry
// carries no capabilities (conversation path). See newSeededHarness.
func newHarness(t *testing.T, a *fakeAgent, ch *fakeChannel, ev *fakeEventStore, opts ...Option) *Orchestrator {
	t.Helper()
	return newSeededHarness(t, a, ch, ev, nil, opts...)
}

// newSeededHarness builds the Orchestrator over a real AgentFlow (Agent →
// CapabilityFlow → PlanExecutor → Dispatcher) with a registry seeded by the
// given callback, so tests can register the capabilities the plan path needs.
func newSeededHarness(t *testing.T, a *fakeAgent, ch *fakeChannel, ev *fakeEventStore, seed func(*capability.Registry), opts ...Option) *Orchestrator {
	t.Helper()
	reg := capability.NewRegistry()
	if seed != nil {
		seed(reg)
	}
	flow := application.NewAgentFlow(a, application.NewCapabilityFlow(capability.NewPlanExecutor(capability.NewDispatcher(reg))))
	discard := log.New(io.Discard, "", 0)
	all := append([]Option{
		WithNow(func() time.Time { return fixedNow }),
		WithID(func() string { return "evt-1" }),
		WithLogger(discard),
	}, opts...)
	return NewOrchestrator(flow, ch, ev, all...)
}

func fixedTriggerTask() (domain.Trigger, domain.Task) {
	return domain.Trigger{ID: "trg-1", TaskID: "t-1"},
		domain.Task{ID: "t-1", Title: "ta", Status: domain.TaskStatusPending}
}

func TestAgentRetryableError(t *testing.T) {
	a := &fakeAgent{err: errors.New("provider timeout")}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	o := newHarness(t, a, ch, ev)
	trg, task := fixedTriggerTask()

	err := o.Execute(context.Background(), trg, task)
	if err == nil {
		t.Fatal("expected retryable agent error to propagate")
	}
	if len(a.calls) != 1 {
		t.Errorf("agent must run, got %d calls", len(a.calls))
	}
	if len(ch.sent) != 0 {
		t.Errorf("no Send expected on agent failure, got %d sends", len(ch.sent))
	}
	if len(ev.events) != 0 {
		t.Errorf("no Event expected on agent failure, got %d events", len(ev.events))
	}
}

func TestAgentPermanentError(t *testing.T) {
	permanent := errors.New("subscription expired")
	a := &fakeAgent{err: permanent}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	o := newHarness(t, a, ch, ev)
	trg, task := fixedTriggerTask()

	err := o.Execute(context.Background(), trg, task)
	if !errors.Is(err, permanent) {
		t.Errorf("expected the agent error to propagate, got %v", err)
	}
	if len(ch.sent) != 0 {
		t.Errorf("no Send expected on permanent failure, got %d sends", len(ch.sent))
	}
	if len(ev.events) != 0 {
		t.Errorf("no Event expected on permanent failure, got %d events", len(ev.events))
	}
}

func TestAgentOKChannelError(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: "hello"}}
	ch := &fakeChannel{err: errors.New("telegram down")}
	ev := &fakeEventStore{}
	o := newHarness(t, a, ch, ev)
	trg, task := fixedTriggerTask()

	err := o.Execute(context.Background(), trg, task)
	if err == nil {
		t.Fatal("expected channel error to propagate")
	}
	if len(a.calls) != 1 {
		t.Errorf("agent must run, got %d calls", len(a.calls))
	}
	if len(ch.sent) != 1 {
		t.Errorf("Send must be attempted, got %d sends", len(ch.sent))
	}
	if len(ev.events) != 0 {
		t.Errorf("no Event expected before a confirmed delivery, got %d events", len(ev.events))
	}
}

func TestAgentOKChannelOKEventError(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: "hello"}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{err: errors.New("event store down")}
	o := newHarness(t, a, ch, ev)
	trg, task := fixedTriggerTask()

	err := o.Execute(context.Background(), trg, task)
	if err != nil {
		// Delivery already confirmed: an Event failure must NOT make Execute fail,
		// otherwise the Scheduler would retry an already-delivered notification.
		t.Fatalf("Execute must return nil despite Event.Save error, got %v", err)
	}
	if len(a.calls) != 1 {
		t.Errorf("agent must run, got %d calls", len(a.calls))
	}
	if len(ch.sent) != 1 {
		t.Errorf("delivery must be confirmed, got %d sends", len(ch.sent))
	}
	if len(ev.events) != 1 {
		t.Errorf("Event.Save must be attempted, got %d saves", len(ev.events))
	}
}

func TestHappyFlowOrderAndEvent(t *testing.T) {
	order := []string{}
	a := &fakeAgent{
		result: domain.AgentResult{Response: "here is your reminder"},
		order:  &order,
	}
	ch := &fakeChannel{order: &order}
	ev := &fakeEventStore{order: &order}
	o := newHarness(t, a, ch, ev)
	trg, task := fixedTriggerTask()

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	wantOrder := []string{"agent", "channel", "event"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", order, wantOrder)
		}
	}

	if len(a.calls) != 1 || a.calls[0].Task.ID != "t-1" {
		t.Errorf("agent must receive the task context")
	}
	if a.calls[0].Instruction == "" {
		t.Error("Instruction must be derived by the orchestrator")
	}

	if len(ch.sent) != 1 || ch.sent[0] != "here is your reminder" {
		t.Errorf("channel must deliver the agent response, got %v", ch.sent)
	}

	if len(ev.events) != 1 {
		t.Fatalf("expected 1 agent result event, got %d", len(ev.events))
	}
	e := ev.events[0]
	if e.Type != domain.EventAgentResult {
		t.Errorf("event type = %q, want %q", e.Type, domain.EventAgentResult)
	}
	if e.Payload["task_id"] != "t-1" {
		t.Errorf("event payload task_id = %v, want t-1", e.Payload["task_id"])
	}
	if e.Payload["trigger_id"] != "trg-1" {
		t.Errorf("event payload trigger_id = %v, want trg-1", e.Payload["trigger_id"])
	}
	if e.Payload["response"] != "here is your reminder" {
		t.Errorf("event payload response = %v, want the agent response", e.Payload["response"])
	}
	if !e.CreatedAt.Equal(fixedNow) {
		t.Errorf("event CreatedAt = %v, want %v", e.CreatedAt, fixedNow)
	}
}

// --- AgentFlow integration: conversation, plan, classification errors --------

// TestAgentFlowConversationResponse verifies that a conversational Pi response
// reaches the Channel unmodified and is audited with exactly the delivered text.
func TestAgentFlowConversationResponse(t *testing.T) {
	response := "  tienes 3 tareas pendientes  "
	a := &fakeAgent{result: domain.AgentResult{Response: response}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	o := newHarness(t, a, ch, ev)
	trg, task := fixedTriggerTask()

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ch.sent) != 1 || ch.sent[0] != response {
		t.Errorf("channel delivery = %q, want the unmodified conversational response %q", ch.sent, response)
	}
	if len(ev.events) != 1 {
		t.Fatalf("events = %d, want 1", len(ev.events))
	}
	if got := ev.events[0].Payload["response"]; got != response {
		t.Errorf("audit response = %q, want %q (audit mirrors delivery)", got, response)
	}
}

// TestAgentFlowPlanExecutesCapability verifies that a Plan response is executed
// through the capability layer and its results are delivered and audited at the
// same point.
func TestAgentFlowPlanExecutesCapability(t *testing.T) {
	var capabilityCalls int
	a := &fakeAgent{result: domain.AgentResult{Response: `{"calls":[{"capability":"echo","args":{}}],"clarification":null}`}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	o := newSeededHarness(t, a, ch, ev, func(reg *capability.Registry) {
		reg.Register(
			capability.Capability{Name: "echo", Parameters: []byte(`{"type":"object","properties":{}}`)},
			echoCapabilityHandler{calls: &capabilityCalls},
		)
	})
	trg, task := fixedTriggerTask()

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if capabilityCalls != 1 {
		t.Errorf("capability calls = %d, want 1", capabilityCalls)
	}
	want := `[{"data":{"ok":true}}]`
	if len(ch.sent) != 1 || ch.sent[0] != want {
		t.Errorf("channel delivery = %q, want the plan results %q", ch.sent, want)
	}
	if len(ev.events) != 1 {
		t.Fatalf("events = %d, want 1", len(ev.events))
	}
	if got := ev.events[0].Payload["response"]; got != want {
		t.Errorf("audit response = %q, want %q (audit mirrors delivery)", got, want)
	}
}

// TestAgentFlowInvalidPlanErrorPropagates verifies that a classification error
// inside the flow (an invalid plan) propagates before any delivery or audit.
func TestAgentFlowInvalidPlanErrorPropagates(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: `{"calls":[`}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	o := newHarness(t, a, ch, ev)
	trg, task := fixedTriggerTask()

	err := o.Execute(context.Background(), trg, task)
	if err == nil {
		t.Fatal("expected the invalid-plan flow error to propagate")
	}
	if !errors.Is(err, capability.ErrInvalidPlan) {
		t.Errorf("errors.Is(err, ErrInvalidPlan) = false, err = %v", err)
	}
	if len(ch.sent) != 0 {
		t.Errorf("no delivery expected on flow failure, got %d sends", len(ch.sent))
	}
	if len(ev.events) != 0 {
		t.Errorf("no Event expected on flow failure, got %d events", len(ev.events))
	}
}

// TestAgentFlowCapabilityErrorPropagates verifies that an execution error inside
// the flow (an unknown capability) propagates before any delivery or audit.
func TestAgentFlowCapabilityErrorPropagates(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: `{"calls":[{"capability":"missing","args":{}}],"clarification":null}`}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	o := newHarness(t, a, ch, ev)
	trg, task := fixedTriggerTask()

	err := o.Execute(context.Background(), trg, task)
	if err == nil {
		t.Fatal("expected the capability error to propagate")
	}
	if !errors.Is(err, capability.ErrUnknownCapability) {
		t.Errorf("errors.Is(err, ErrUnknownCapability) = false, err = %v", err)
	}
	if len(ch.sent) != 0 {
		t.Errorf("no delivery expected on capability failure, got %d sends", len(ch.sent))
	}
	if len(ev.events) != 0 {
		t.Errorf("no Event expected on capability failure, got %d events", len(ev.events))
	}
}

// --- Semantic search: context enrichment and memory indexing -----------------

func TestSearchEnrichesInstruction(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: "ok"}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	src := &fakeSearcher{results: []domain.SearchResult{
		{Ref: domain.Ref{Kind: domain.RefKindTask, ID: "other-1"}, Text: "buy milk tomorrow morning", Score: 0.9},
		{Ref: domain.Ref{Kind: domain.RefKindEvent, ID: "evt-past"}, Text: "the dentist is booked for Friday", Score: 0.7},
	}}
	o := newHarness(t, a, ch, ev, WithSearcher(src))
	trg, task := fixedTriggerTask()
	// Give the task a description so the query carries it.
	task.Title = "buy milk"
	task.Description = "urgent"

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// The query derives from title+description, and the fired task itself is
	// excluded; limit stays 0 (adapter default).
	if len(src.queries) != 1 || src.queries[0] != "buy milk urgent" {
		t.Errorf("query = %q, want the task title+description", src.queries)
	}
	opts := src.opts[0]
	if opts.Limit != 0 {
		t.Errorf("Limit = %d, want 0 (adapter default)", opts.Limit)
	}
	if len(opts.Exclude) != 1 || opts.Exclude[0] != (domain.Ref{Kind: domain.RefKindTask, ID: task.ID}) {
		t.Errorf("Exclude = %+v, want the fired task itself", opts.Exclude)
	}

	// The instruction carries the neutral user message, the default, and
	// labeled, bounded context.
	instr := a.calls[0].Instruction
	if !strings.HasPrefix(instr, "User message: ") {
		t.Errorf("instruction lacks the neutral user message prefix: %q", instr)
	}
	if !strings.Contains(instr, "Related context:") {
		t.Errorf("instruction lacks context: %q", instr)
	}
	if !strings.Contains(instr, "Task: buy milk tomorrow morning") {
		t.Errorf("instruction lacks the task fragment: %q", instr)
	}
	if !strings.Contains(instr, "Previous agent response: the dentist is booked for Friday") {
		t.Errorf("instruction lacks the agent-memory fragment: %q", instr)
	}
}

func TestSearchErrorFallsBackToDefault(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: "ok"}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	src := &fakeSearcher{err: errors.New("embedder down")}
	o := newHarness(t, a, ch, ev, WithSearcher(src))
	trg, task := fixedTriggerTask()

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := "User message: " + defaultInstruction(task)
	if got := a.calls[0].Instruction; got != want {
		t.Errorf("instruction = %q, want %q", got, want)
	}
}

func TestSearchUnavailableFallsBackSilently(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: "ok"}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	src := &fakeSearcher{err: domain.ErrSemanticUnavailable}
	o := newHarness(t, a, ch, ev, WithSearcher(src))
	trg, task := fixedTriggerTask()

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("execute again: %v", err)
	}
	want := "User message: " + defaultInstruction(task)
	if got := a.calls[0].Instruction; got != want {
		t.Errorf("instruction = %q, want %q", got, want)
	}
}

func TestNoSearcherNoContext(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: "ok"}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	o := newHarness(t, a, ch, ev)
	trg, task := fixedTriggerTask()

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := "User message: " + defaultInstruction(task)
	if got := a.calls[0].Instruction; got != want {
		t.Errorf("instruction = %q, want %q", got, want)
	}
}

func TestEmptySearchResultsNoContext(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: "ok"}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	src := &fakeSearcher{} // no results
	o := newHarness(t, a, ch, ev, WithSearcher(src))
	trg, task := fixedTriggerTask()

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := "User message: " + defaultInstruction(task)
	if got := a.calls[0].Instruction; got != want {
		t.Errorf("instruction = %q, want %q", got, want)
	}
}

// TestInstructionIsNeutralForPlanner verifies that the Orchestrator instruction
// no longer prepends the old reminderPrompt (whose notification-only mandates
// blocked Plan JSON) and still carries the task/trigger context: the neutral
// "User message: " framing plus exactly the default instruction derived from
// the task. The plannerPrompt appended later by PiAgent remains the single
// authority deciding between a conversational reply and a Plan.
func TestInstructionIsNeutralForPlanner(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: "ok"}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	o := newHarness(t, a, ch, ev)
	trg, task := fixedTriggerTask()
	task.Title = "comprar leche"

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("execute: %v", err)
	}

	instr := a.calls[0].Instruction
	want := "User message: " + defaultInstruction(task)
	if instr != want {
		t.Errorf("instruction = %q, want %q", instr, want)
	}

	// The task context survives: the default instruction embeds the task title.
	if !strings.Contains(instr, task.Title) {
		t.Errorf("instruction lacks the task context (title %q): %q", task.Title, instr)
	}

	// The reminderPrompt mandates that blocked planning must never appear.
	if strings.Contains(instr, "Reply ONLY with the notification text") {
		t.Error("instruction still carries the reminderPrompt 'Reply ONLY' mandate")
	}
	if strings.Contains(instr, "Do not execute actions") {
		t.Error("instruction still carries the reminderPrompt 'Do not execute actions' mandate")
	}
}

func TestIndexesAgentResultAfterDelivery(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: "remember this"}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	idx := &fakeIndexer{}
	o := newHarness(t, a, ch, ev, WithIndexer(idx))
	trg, task := fixedTriggerTask()

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(ev.events) != 1 {
		t.Fatalf("audit event saves = %d, want 1", len(ev.events))
	}
	if len(idx.events) != 1 {
		t.Fatalf("indexed events = %d, want 1", len(idx.events))
	}
	got := idx.events[0]
	if got.Type != domain.EventAgentResult || got.Payload["response"] != "remember this" {
		t.Errorf("indexed event = %+v, want the confirmed agent.result", got)
	}
	if !got.CreatedAt.Equal(fixedNow) {
		t.Errorf("indexed event CreatedAt = %v, want %v", got.CreatedAt, fixedNow)
	}
}

func TestIndexEventFailureNeverBreaksFlow(t *testing.T) {
	a := &fakeAgent{result: domain.AgentResult{Response: "ok"}}
	ch := &fakeChannel{}
	ev := &fakeEventStore{}
	idx := &fakeIndexer{err: errors.New("embedder down")}
	o := newHarness(t, a, ch, ev, WithIndexer(idx))
	trg, task := fixedTriggerTask()

	if err := o.Execute(context.Background(), trg, task); err != nil {
		t.Fatalf("execute must succeed despite index failure: %v", err)
	}
	if len(ev.events) != 1 {
		t.Errorf("delivery/audit must be unaffected by index failure")
	}
}
