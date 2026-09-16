package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/service"
)

// fixedNow is the deterministic clock injected into the handler for resolution
// tests: a 30m relative reminder fires at 2026-01-15T10:30:00Z.
var fixedNow = time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)

// captureTriggerRepo embeds the shared no-op TriggerRepository and records every
// trigger persisted through it, so tests can assert the Task + Trigger
// composition produced by ReminderService.CreateOneShot.
type captureTriggerRepo struct {
	stubTriggerRepo
	created []domain.Trigger
}

func (c *captureTriggerRepo) Create(_ context.Context, t domain.Trigger) error {
	c.created = append(c.created, t)
	return nil
}

// failingTriggerRepo forces Trigger creation failures, mirroring the trigger
// failure path of ReminderService.CreateOneShot (partial outcome: task
// persisted, trigger not).
type failingTriggerRepo struct {
	stubTriggerRepo
	err error
}

func (f *failingTriggerRepo) Create(context.Context, domain.Trigger) error { return f.err }

// buildReminderServiceSharingTasks builds a ReminderService whose TriggerService
// resolves tasks through the same in-memory repository as the TaskService —
// mirroring the production wiring, where both services share the SQLite
// repository. This differs from buildReminderService (which uses a separate
// empty task repo) and is required for flows that actually create triggers.
func buildReminderServiceSharingTasks(ts *service.TaskService, taskRepo *mockTaskRepo, triggers domain.TriggerRepository) *service.ReminderService {
	triggerSvc := service.NewTriggerService(triggers, taskRepo)
	return service.NewReminderService(ts, triggerSvc)
}

// registerCreateReminder is a shorthand used by dispatcher tests: it registers
// the create_reminder capability with its shipped schema (title required).
func registerCreateReminder(t *testing.T, ts *service.TaskService, taskRepo *mockTaskRepo, opts ...Option) *Registry {
	t.Helper()
	r := NewRegistry()
	r.Register(
		Capability{
			Name:        "create_reminder",
			Description: "create a reminder task",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"title":         {"type": "string"},
					"relative":      {"type": "string"},
					"absolute_time": {"type": "string"},
					"absolute_date": {"type": "string"},
					"recurrence":    {"type": "object"}
				},
				"required": ["title"]
			}`),
		},
		NewCreateReminderHandler(buildReminderServiceSharingTasks(ts, taskRepo, &stubTriggerRepo{}), opts...),
	)
	return r
}

// ---------------------------------------------------------------------------
// Constructor guard
// ---------------------------------------------------------------------------

func TestNewCreateReminderHandlerPanicsOnNilService(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil ReminderService")
		}
	}()
	NewCreateReminderHandler(nil)
}

// ---------------------------------------------------------------------------
// One-shot relative success
// ---------------------------------------------------------------------------

// TestCreateReminderRelativePersistsTaskAndTrigger verifies the full delegation
// for the one-shot relative case with a fixed clock: the task is persisted
// through the TaskService, the trigger through the TriggerService, and the
// resolved fire time (Now + relative, computed by service.ResolveTime — never by
// the handler) reaches the trigger as the canonical RFC3339 "at" value.
func TestCreateReminderRelativePersistsTaskAndTrigger(t *testing.T) {
	ts, taskRepo := buildTaskService()
	triggerRepo := &captureTriggerRepo{}
	rs := buildReminderServiceSharingTasks(ts, taskRepo, triggerRepo)
	h := NewCreateReminderHandler(rs, WithNow(func() time.Time { return fixedNow }), WithTimezone(time.UTC))

	result, err := h.Execute(context.Background(), json.RawMessage(`{"title":"comprar SSD","relative":"30m"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ctr, ok := result.Data.(CreateReminderResult)
	if !ok {
		t.Fatalf("Data is %T, want CreateReminderResult", result.Data)
	}
	task := ctr.Task

	if task.ID == "" {
		t.Error("expected a generated task ID")
	}
	if task.Title != "comprar SSD" {
		t.Errorf("Title = %q, want %q", task.Title, "comprar SSD")
	}
	if task.Status != "pending" {
		t.Errorf("Status = %q, want %q", task.Status, "pending")
	}
	if task.CreatedAt == "" || task.UpdatedAt == "" {
		t.Error("timestamps must be present")
	}

	// The task is persisted through the shared TaskService.
	if len(taskRepo.tasks) != 1 || taskRepo.tasks[0].ID != task.ID {
		t.Errorf("task must be persisted in the shared repository, got %d tasks", len(taskRepo.tasks))
	}

	// The trigger is persisted through the TriggerService with the resolved
	// fire time (fixed clock + 30m) as its canonical "at" value.
	if len(triggerRepo.created) != 1 {
		t.Fatalf("triggers created = %d, want 1", len(triggerRepo.created))
	}
	tr := triggerRepo.created[0]
	if tr.TaskID != task.ID {
		t.Errorf("trigger TaskID = %q, want %q", tr.TaskID, task.ID)
	}
	if tr.Type != domain.TriggerTypeAt {
		t.Errorf("trigger Type = %q, want %q", tr.Type, domain.TriggerTypeAt)
	}
	if !tr.Enabled {
		t.Error("trigger must be enabled")
	}
	if want := "2026-01-15T10:30:00Z"; tr.Value != want {
		t.Errorf("trigger Value = %q, want %q (resolved by service.ResolveTime)", tr.Value, want)
	}
}

// ---------------------------------------------------------------------------
// One-shot absolute success
// ---------------------------------------------------------------------------

// absoluteTriggerValue executes a one-shot reminder against a
// captureTriggerRepo with the standard deterministic clock (fixedNow, UTC) and
// returns the canonical RFC3339 "at" value persisted by the shared
// ReminderService. Extra options are applied after the defaults, so a test can
// override the clock and/or the timezone (e.g. WithTimezone(loc)).
func absoluteTriggerValue(t *testing.T, args string, opts ...Option) string {
	t.Helper()
	ts, taskRepo := buildTaskService()
	triggerRepo := &captureTriggerRepo{}
	rs := buildReminderServiceSharingTasks(ts, taskRepo, triggerRepo)
	allOpts := append([]Option{
		WithNow(func() time.Time { return fixedNow }),
		WithTimezone(time.UTC),
	}, opts...)
	h := NewCreateReminderHandler(rs, allOpts...)

	if _, err := h.Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute(%s): %v", args, err)
	}
	if len(triggerRepo.created) != 1 {
		t.Fatalf("triggers created = %d, want 1", len(triggerRepo.created))
	}
	return triggerRepo.created[0].Value
}

// TestCreateReminderAbsoluteNoDateDefaultsToToday verifies the no-date default:
// an absolute_time without absolute_date resolves to today (fixedNow is 10:00;
// the requested 10:30 has not passed).
func TestCreateReminderAbsoluteNoDateDefaultsToToday(t *testing.T) {
	if got, want := absoluteTriggerValue(t, `{"title":"comprar SSD","absolute_time":"10:30"}`), "2026-01-15T10:30:00Z"; got != want {
		t.Errorf("trigger Value = %q, want %q", got, want)
	}
}

func TestCreateReminderAbsoluteToday(t *testing.T) {
	if got, want := absoluteTriggerValue(t, `{"title":"comprar SSD","absolute_time":"10:30","absolute_date":"today"}`), "2026-01-15T10:30:00Z"; got != want {
		t.Errorf("trigger Value = %q, want %q", got, want)
	}
}

func TestCreateReminderAbsoluteTomorrow(t *testing.T) {
	if got, want := absoluteTriggerValue(t, `{"title":"comprar SSD","absolute_time":"10:30","absolute_date":"tomorrow"}`), "2026-01-16T10:30:00Z"; got != want {
		t.Errorf("trigger Value = %q, want %q", got, want)
	}
}

func TestCreateReminderAbsoluteExplicitDate(t *testing.T) {
	if got, want := absoluteTriggerValue(t, `{"title":"comprar SSD","absolute_time":"09:00","absolute_date":"2026-03-01"}`), "2026-03-01T09:00:00Z"; got != want {
		t.Errorf("trigger Value = %q, want %q", got, want)
	}
}

// TestCreateReminderAbsolutePastTimeShiftsToTomorrow pins the existing
// ResolveTime semantics for a time that has already passed today without a
// date (defaults to today, then shifts to tomorrow).
func TestCreateReminderAbsolutePastTimeShiftsToTomorrow(t *testing.T) {
	// fixedNow is 10:00; 09:00 has already passed.
	if got, want := absoluteTriggerValue(t, `{"title":"comprar SSD","absolute_time":"09:00"}`), "2026-01-16T09:00:00Z"; got != want {
		t.Errorf("trigger Value = %q, want %q", got, want)
	}
}

// TestCreateReminderAbsoluteExplicitPastDateIsHonored pins the existing
// ResolveTime semantics for explicit dates: a YYYY-MM-DD date is honored
// verbatim (no today→tomorrow shift), even when it lies in the past.
func TestCreateReminderAbsoluteExplicitPastDateIsHonored(t *testing.T) {
	if got, want := absoluteTriggerValue(t, `{"title":"comprar SSD","absolute_time":"10:30","absolute_date":"2026-01-01"}`), "2026-01-01T10:30:00Z"; got != want {
		t.Errorf("trigger Value = %q, want %q", got, want)
	}
}

// TestCreateReminderAbsoluteWithInjectedTimezone proves the handler resolves in
// the injected timezone, not in UTC: at 21:00Z (18:00 in Buenos Aires, UTC-3)
// the requested 20:00 has already passed today in UTC (shifted to tomorrow)
// but is still ahead locally in ART (resolved today) — the timezone drives the
// today/tomorrow decision. The explicit-date case pins the offset end to end
// (09:00 ART = 12:00Z).
func TestCreateReminderAbsoluteWithInjectedTimezone(t *testing.T) {
	ba, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	// Same clock, injected timezone: 20:00 ART has not passed locally.
	if got, want := absoluteTriggerValue(t,
		`{"title":"comprar SSD","absolute_time":"20:00"}`,
		WithNow(func() time.Time { return time.Date(2026, 1, 15, 21, 0, 0, 0, time.UTC) }),
		WithTimezone(ba)), "2026-01-15T23:00:00Z"; got != want {
		t.Errorf("ART trigger Value = %q, want %q (resolved in Buenos Aires)", got, want)
	}

	// Same clock in UTC: 20:00 has passed, shifted to tomorrow.
	if got, want := absoluteTriggerValue(t,
		`{"title":"comprar SSD","absolute_time":"20:00"}`,
		WithNow(func() time.Time { return time.Date(2026, 1, 15, 21, 0, 0, 0, time.UTC) })),
		"2026-01-16T20:00:00Z"; got != want {
		t.Errorf("UTC trigger Value = %q, want %q (shifted to tomorrow)", got, want)
	}

	// Explicit date resolved in ART carries the UTC-3 offset.
	if got, want := absoluteTriggerValue(t,
		`{"title":"comprar SSD","absolute_time":"09:00","absolute_date":"2026-03-01"}`,
		WithTimezone(ba)), "2026-03-01T12:00:00Z"; got != want {
		t.Errorf("explicit-date trigger Value = %q, want %q (resolved in Buenos Aires)", got, want)
	}
}

// TestCreateReminderAbsoluteAcceptedTimeFormats pins the parseable time formats
// shared with the NL contract (service.ParseHHMM): "10h" implies :00 and
// "10.30" normalizes to 10:30, both resolving to the same-day wall clock.
func TestCreateReminderAbsoluteAcceptedTimeFormats(t *testing.T) {
	cases := []struct {
		name string
		time string
		want string
	}{
		{"hour_only", "10h", "2026-01-15T10:00:00Z"},
		{"dot_separator", "10.30", "2026-01-15T10:30:00Z"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := fmt.Sprintf(`{"title":"x","absolute_time":%q}`, tc.time)
			if got := absoluteTriggerValue(t, args); got != tc.want {
				t.Errorf("trigger Value = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// One-shot absolute validation (ErrInvalidArgs)
// ---------------------------------------------------------------------------

// TestCreateReminderAbsoluteRejectsInvalidArgs covers the semantic validation
// of the one-shot absolute case: absolute_date without absolute_time, malformed
// absolute_time values, unsupported absolute_date values, and the cross-case
// combinations with relative and recurrence.
func TestCreateReminderAbsoluteRejectsInvalidArgs(t *testing.T) {
	ts, _ := buildTaskService()
	h := NewCreateReminderHandler(buildReminderService(ts), WithNow(func() time.Time { return fixedNow }), WithTimezone(time.UTC))

	cases := []struct {
		name string
		args string
	}{
		{"absolute_date_without_time", `{"title":"x","absolute_date":"tomorrow"}`},
		{"invalid_hour", `{"title":"x","absolute_time":"25:00"}`},
		{"invalid_minute", `{"title":"x","absolute_time":"20:99"}`},
		{"bare_number_ambiguous", `{"title":"x","absolute_time":"20"}`},
		{"invalid_date", `{"title":"x","absolute_time":"20:00","absolute_date":"ayer"}`},
		{"invalid_date_format", `{"title":"x","absolute_time":"20:00","absolute_date":"2026/03/01"}`},
		{"relative_plus_absolute", `{"title":"x","relative":"30m","absolute_time":"20:00"}`},
		{"recurrence_plus_absolute", `{"title":"x","absolute_time":"20:00","recurrence":{"freq":"daily","time":"09:00"}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Execute(context.Background(), json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidArgs) {
				t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Handler semantic validation (ErrInvalidArgs)
// ---------------------------------------------------------------------------

func TestCreateReminderRelativeRejectsMissingOrInvalidRelative(t *testing.T) {
	ts, _ := buildTaskService()
	h := NewCreateReminderHandler(buildReminderService(ts), WithNow(func() time.Time { return fixedNow }))

	cases := []struct {
		name string
		args string
	}{
		{"missing_relative", `{"title":"x"}`},
		{"empty_relative", `{"title":"x","relative":""}`},
		{"whitespace_relative", `{"title":"x","relative":"   "}`},
		{"invalid_relative", `{"title":"x","relative":"manana"}`},
		{"zero_relative", `{"title":"x","relative":"0s"}`},
		{"negative_relative", `{"title":"x","relative":"-30m"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Execute(context.Background(), json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidArgs) {
				t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
			}
		})
	}
}

// TestCreateReminderRejectsInvalidCombinationsAndRecurrence verifies that the
// cross-case combinations of the contract are rejected with ErrInvalidArgs:
// relative combined with any absolute field, recurrence combined with any other
// field, and recurrence on its own (the recurring case is declared by the
// contract but not implemented in this step). The absolute-only inputs that
// used to be rejected as unimplemented are now valid and covered by the
// one-shot absolute tests below.
func TestCreateReminderRejectsInvalidCombinationsAndRecurrence(t *testing.T) {
	ts, _ := buildTaskService()
	h := NewCreateReminderHandler(buildReminderService(ts), WithNow(func() time.Time { return fixedNow }))

	cases := []struct {
		name string
		args string
	}{
		{"relative_plus_absolute_time", `{"title":"x","relative":"30m","absolute_time":"20:00"}`},
		{"relative_plus_absolute_date", `{"title":"x","relative":"30m","absolute_date":"tomorrow"}`},
		{"relative_plus_recurrence", `{"title":"x","relative":"30m","recurrence":{"freq":"daily","time":"09:00"}}`},
		{"absolute_plus_recurrence", `{"title":"x","absolute_time":"20:00","recurrence":{"freq":"daily","time":"09:00"}}`},
		{"recurrence_only", `{"title":"x","recurrence":{"freq":"weekly","time":"09:00","weekdays":[1]}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Execute(context.Background(), json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidArgs) {
				t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Schema validation through the dispatcher (contract)
// ---------------------------------------------------------------------------

func TestCreateReminderSchemaRejectsMissingTitle(t *testing.T) {
	ts, taskRepo := buildTaskService()
	d := NewDispatcher(registerCreateReminder(t, ts, taskRepo))

	_, err := d.Dispatch(context.Background(), "create_reminder", json.RawMessage(`{"relative":"30m"}`))
	if err == nil {
		t.Fatal("expected error for missing required title")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

func TestCreateReminderSchemaRejectsNonStringFields(t *testing.T) {
	ts, taskRepo := buildTaskService()
	d := NewDispatcher(registerCreateReminder(t, ts, taskRepo))

	cases := []struct {
		name string
		args string
	}{
		{"non_string_title", `{"title":5,"relative":"30m"}`},
		{"non_string_relative", `{"title":"x","relative":30}`},
		{"non_object_recurrence", `{"title":"x","recurrence":"daily"}`},
		{"unknown_property", `{"title":"x","relative":"30m","when":"20:00"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Dispatch(context.Background(), "create_reminder", json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidArgs) {
				t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Service failures propagate
// ---------------------------------------------------------------------------

// TestCreateReminderTaskFailurePropagates proves that a TaskService failure
// surfaces through the handler (wrapped by the dispatcher as
// ErrExecutionFailed) instead of being swallowed: nothing is persisted.
func TestCreateReminderTaskFailurePropagates(t *testing.T) {
	repo := &failingCreateRepo{mockTaskRepo: &mockTaskRepo{}, err: errInner}
	ts := service.NewTaskService(repo, &stubTriggerRepo{}, &stubEventStore{})
	h := NewCreateReminderHandler(buildReminderService(ts), WithNow(func() time.Time { return fixedNow }))

	_, err := h.Execute(context.Background(), json.RawMessage(`{"title":"x","relative":"30m"}`))
	if err == nil {
		t.Fatal("expected error from failing task repository")
	}
	if !errors.Is(err, errInner) {
		t.Errorf("errors.Is(err, errInner) = false: the inner error chain is lost, err = %v", err)
	}
}

// TestCreateReminderTriggerFailurePropagates pins the documented partial-outcome
// semantics of ReminderService.CreateOneShot through the capability: when the
// trigger creation fails the error propagates (the reminder is not reported as
// created) even though the task was already persisted.
func TestCreateReminderTriggerFailurePropagates(t *testing.T) {
	ts, taskRepo := buildTaskService()
	triggerRepo := &failingTriggerRepo{err: errInner}
	rs := buildReminderServiceSharingTasks(ts, taskRepo, triggerRepo)
	h := NewCreateReminderHandler(rs, WithNow(func() time.Time { return fixedNow }))

	_, err := h.Execute(context.Background(), json.RawMessage(`{"title":"x","relative":"30m"}`))
	if err == nil {
		t.Fatal("expected error from failing trigger repository")
	}
	if !errors.Is(err, errInner) {
		t.Errorf("errors.Is(err, errInner) = false: the inner error chain is lost, err = %v", err)
	}
	if len(taskRepo.tasks) != 1 {
		t.Errorf("task must remain persisted on trigger failure (partial outcome), got %d tasks", len(taskRepo.tasks))
	}
}

// ---------------------------------------------------------------------------
// Stable JSON contract
// ---------------------------------------------------------------------------

// TestCreateReminderStableJSONShape pins the external JSON shape: exactly the
// "data" key with a single "task" object carrying the stable TaskView keys
// (Source is internal and never exposed).
func TestCreateReminderStableJSONShape(t *testing.T) {
	ts, taskRepo := buildTaskService()
	d := NewDispatcher(registerCreateReminder(t, ts, taskRepo,
		WithNow(func() time.Time { return fixedNow }), WithTimezone(time.UTC)))

	result, err := d.Dispatch(context.Background(), "create_reminder", json.RawMessage(`{"title":"comprar SSD","relative":"30m"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var outer map[string]any
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if len(outer) != 1 {
		t.Fatalf("outer keys = %v, want exactly [data] (no error field)", mapKeys(outer))
	}

	data, ok := outer["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is %T, want object", outer["data"])
	}
	task, ok := data["task"].(map[string]any)
	if !ok {
		t.Fatalf("data.task is %T, want object", data["task"])
	}

	wantTaskKeys := []string{"id", "title", "description", "status", "priority", "created_at", "updated_at"}
	if len(task) != len(wantTaskKeys) {
		t.Fatalf("task keys = %v, want exactly %v", mapKeys(task), wantTaskKeys)
	}
	for _, k := range wantTaskKeys {
		if _, ok := task[k]; !ok {
			t.Errorf("missing task key %q", k)
		}
	}
	if task["title"] != "comprar SSD" || task["status"] != "pending" {
		t.Errorf("unexpected task fields: %v", task)
	}
	for _, k := range []string{"created_at", "updated_at"} {
		s, ok := task[k].(string)
		if !ok {
			t.Fatalf("%s is %T, want string", k, task[k])
		}
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			t.Errorf("%s = %q is not RFC3339: %v", k, s, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func TestRegisterShippedCapabilitiesRegistersCreateReminder(t *testing.T) {
	ts, taskRepo := buildTaskService()
	reg := NewRegistry()
	RegisterShippedCapabilities(reg, ts, buildReminderServiceSharingTasks(ts, taskRepo, &stubTriggerRepo{}))

	if !reg.Has("create_reminder") {
		t.Fatal("registry must contain create_reminder after RegisterShippedCapabilities")
	}
	capDef, handler, err := reg.Get("create_reminder")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if handler == nil {
		t.Fatal("create_reminder handler must be wired")
	}
	if capDef.Description == "" {
		t.Error("create_reminder must carry a Description for the planner context")
	}
	if err := validateArgs(capDef.Parameters, []byte(`{"title":"x","relative":"30m"}`)); err != nil {
		t.Errorf("Parameters must accept relative args, got: %v", err)
	}

	// The registered handler is dispatchable with the default clock (only the
	// success path is asserted): the resolved fire time is time-dependent for
	// relative, and an absolute time with an explicit date always resolves
	// deterministically — today/tomorrow fallback applies only to no-date.
	d := NewDispatcher(reg)
	raw, err := d.Dispatch(context.Background(), "create_reminder", json.RawMessage(`{"title":"x","relative":"30m"}`))
	if err != nil {
		t.Fatalf("Dispatch relative reminder through the shipped registry must succeed: %v", err)
	}
	if _, ok := raw.Data.(CreateReminderResult); !ok {
		t.Fatalf("unexpected result type %T", raw.Data)
	}

	raw, err = d.Dispatch(context.Background(), "create_reminder", json.RawMessage(`{"title":"x","absolute_time":"10:30","absolute_date":"tomorrow"}`))
	if err != nil {
		t.Fatalf("Dispatch absolute reminder through the shipped registry must succeed: %v", err)
	}
	if _, ok := raw.Data.(CreateReminderResult); !ok {
		t.Fatalf("unexpected result type %T", raw.Data)
	}
}
