package capability

import (
	"context"
	"encoding/json"
	"errors"
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

// TestCreateReminderRejectsUnimplementedCases verifies that the fields of the
// other two contract cases (one-shot absolute and recurring) are rejected with
// ErrInvalidArgs: standalone, and combined with relative (the contract keeps
// the three cases mutually exclusive).
func TestCreateReminderRejectsUnimplementedCases(t *testing.T) {
	ts, _ := buildTaskService()
	h := NewCreateReminderHandler(buildReminderService(ts), WithNow(func() time.Time { return fixedNow }))

	cases := []struct {
		name string
		args string
	}{
		{"relative_plus_absolute_time", `{"title":"x","relative":"30m","absolute_time":"20:00"}`},
		{"relative_plus_absolute_date", `{"title":"x","relative":"30m","absolute_date":"tomorrow"}`},
		{"relative_plus_recurrence", `{"title":"x","relative":"30m","recurrence":{"freq":"daily","time":"09:00"}}`},
		{"absolute_time_only", `{"title":"x","absolute_time":"20:00"}`},
		{"absolute_date_only", `{"title":"x","absolute_date":"tomorrow"}`},
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
	// success path is asserted; the resolved fire time is time-dependent).
	d := NewDispatcher(reg)
	raw, err := d.Dispatch(context.Background(), "create_reminder", json.RawMessage(`{"title":"x","relative":"30m"}`))
	if err != nil {
		t.Fatalf("Dispatch relative reminder through the shipped registry must succeed: %v", err)
	}
	if _, ok := raw.Data.(CreateReminderResult); !ok {
		t.Fatalf("unexpected result type %T", raw.Data)
	}
}
