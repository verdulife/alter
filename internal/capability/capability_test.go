package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/service"
)

// errNotFound is a test-local sentinel for missing tasks.
var errNotFound = errors.New("task not found")

// errInner is a test-local sentinel used to verify that handler errors keep
// their full chain after the ErrExecutionFailed wrap.
var errInner = errors.New("inner handler failure")

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// mockTaskRepo is an in-memory TaskRepository for testing.
type mockTaskRepo struct {
	tasks []domain.Task
}

func (m *mockTaskRepo) Create(_ context.Context, task domain.Task) error {
	m.tasks = append(m.tasks, task)
	return nil
}

func (m *mockTaskRepo) GetByID(_ context.Context, id string) (domain.Task, error) {
	for _, t := range m.tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return domain.Task{}, errNotFound
}

func (m *mockTaskRepo) Update(_ context.Context, task domain.Task) error {
	for i, t := range m.tasks {
		if t.ID == task.ID {
			m.tasks[i] = task
			return nil
		}
	}
	return errNotFound
}

func (m *mockTaskRepo) Delete(_ context.Context, id string) error {
	for i, t := range m.tasks {
		if t.ID == id {
			m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
			return nil
		}
	}
	return errNotFound
}

func (m *mockTaskRepo) List(_ context.Context) ([]domain.Task, error) {
	out := make([]domain.Task, len(m.tasks))
	copy(out, m.tasks)
	return out, nil
}

// stubTriggerRepo satisfies domain.TriggerRepository with no-ops.
type stubTriggerRepo struct{}

func (stubTriggerRepo) Create(context.Context, domain.Trigger) error { return nil }
func (stubTriggerRepo) GetByID(context.Context, string) (domain.Trigger, error) {
	return domain.Trigger{}, nil
}
func (stubTriggerRepo) GetByTaskID(context.Context, string) ([]domain.Trigger, error) {
	return nil, nil
}
func (stubTriggerRepo) Update(context.Context, domain.Trigger) error         { return nil }
func (stubTriggerRepo) Delete(context.Context, string) error                 { return nil }
func (stubTriggerRepo) ClearDerivedNextFireAt(context.Context, string) error { return nil }
func (stubTriggerRepo) ListEnabled(context.Context) ([]domain.Trigger, error) {
	return nil, nil
}

// stubEventStore satisfies domain.EventStore with no-ops.
type stubEventStore struct{}

func (stubEventStore) Save(context.Context, domain.Event) error { return nil }
func (stubEventStore) ListByType(context.Context, domain.EventType) ([]domain.Event, error) {
	return nil, nil
}

// fixedHandler is a programmable CapabilityHandler returning a fixed result and
// optional error for any call. A nil error means success.
type fixedHandler struct {
	result CapabilityResult
	err    error
}

func (h fixedHandler) Execute(context.Context, json.RawMessage) (CapabilityResult, error) {
	return h.result, h.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildTaskService builds a full TaskService backed by an in-memory repo,
// seeded with the given tasks. All construction happens through the real
// service constructor.
func buildTaskService(tasks ...domain.Task) (*service.TaskService, *mockTaskRepo) {
	repo := &mockTaskRepo{tasks: tasks}
	ts := service.NewTaskService(repo, &stubTriggerRepo{}, &stubEventStore{})
	return ts, repo
}

// registerListTasks is a shorthand used by dispatcher tests: it registers the
// list_tasks capability with its strict empty-properties schema.
func registerListTasks(t *testing.T, ts *service.TaskService) *Registry {
	t.Helper()
	r := NewRegistry()
	r.Register(
		Capability{
			Name:        "list_tasks",
			Description: "list all tasks",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		NewListTasksHandler(ts),
	)
	return r
}

// ---------------------------------------------------------------------------
// Registry tests
// ---------------------------------------------------------------------------

func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	cap := Capability{Name: "test", Description: "a test capability"}
	h := &fixedHandler{result: CapabilityResult{Data: "ok"}}

	r.Register(cap, h)

	got, gotH, err := r.Get("test")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.Name != "test" {
		t.Errorf("Name = %q, want %q", got.Name, "test")
	}
	if gotH != h {
		t.Error("handler mismatch")
	}
}

func TestGetUnknownCapability(t *testing.T) {
	r := NewRegistry()
	_, _, err := r.Get("nope")
	if err == nil {
		t.Fatal("expected error for unknown capability")
	}
	if !errors.Is(err, ErrUnknownCapability) {
		t.Errorf("errors.Is(err, ErrUnknownCapability) = false, err = %v", err)
	}
	if got := err.Error(); got == fmt.Sprintf("%v", ErrUnknownCapability) {
		t.Errorf("expected the unknown capability name in the message, got %q", got)
	}
}

func TestHasAndNames(t *testing.T) {
	r := NewRegistry()
	r.Register(Capability{Name: "beta"}, &fixedHandler{})
	r.Register(Capability{Name: "alpha"}, &fixedHandler{})

	if !r.Has("alpha") {
		t.Error("Has(alpha) = false, want true")
	}
	if r.Has("gamma") {
		t.Error("Has(gamma) = true, want false")
	}

	names := r.Names()
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("Names() = %v, want [alpha beta]", names)
	}
}

func TestRegisterPanicsOnEmptyName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty name")
		}
	}()
	NewRegistry().Register(Capability{}, &fixedHandler{})
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate")
		}
	}()
	r := NewRegistry()
	r.Register(Capability{Name: "dup"}, &fixedHandler{})
	r.Register(Capability{Name: "dup"}, &fixedHandler{})
}

func TestRegisterPanicsOnNilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil handler")
		}
	}()
	NewRegistry().Register(Capability{Name: "x"}, nil)
}

// ---------------------------------------------------------------------------
// Constructor guards
// ---------------------------------------------------------------------------

func TestNewDispatcherPanicsOnNilRegistry(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil registry")
		}
	}()
	NewDispatcher(nil)
}

func TestNewListTasksHandlerPanicsOnNilService(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil TaskService")
		}
	}()
	NewListTasksHandler(nil)
}

// ---------------------------------------------------------------------------
// Dispatcher tests
// ---------------------------------------------------------------------------

func TestDispatchUnknownCapability(t *testing.T) {
	d := NewDispatcher(NewRegistry())
	_, err := d.Dispatch(context.Background(), "missing", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnknownCapability) {
		t.Errorf("errors.Is(err, ErrUnknownCapability) = false, err = %v", err)
	}
}

// TestDispatchUsesCapabilitySchema verifies that validation is driven by the
// registered Capability.Parameters, never by the handler.
func TestDispatchUsesCapabilitySchema(t *testing.T) {
	r := NewRegistry()
	r.Register(
		Capability{
			Name:        "x",
			Description: "schema-driven capability",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`),
		},
		&fixedHandler{result: CapabilityResult{Data: "ok"}},
	)
	d := NewDispatcher(r)

	if _, err := d.Dispatch(context.Background(), "x", json.RawMessage(`{"title":123}`)); err == nil {
		t.Fatal("expected invalid args error for title:123")
	} else if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}

	if _, err := d.Dispatch(context.Background(), "x", json.RawMessage(`{"title":"x"}`)); err != nil {
		t.Fatalf("unexpected error for valid args: %v", err)
	}
}

func TestDispatchInvalidArgsRejected(t *testing.T) {
	ts, _ := buildTaskService()
	d := NewDispatcher(registerListTasks(t, ts))

	_, err := d.Dispatch(context.Background(), "list_tasks", json.RawMessage(`{"unexpected":true}`))
	if err == nil {
		t.Fatal("expected error for non-empty args on no-arg capability")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

func TestDispatchEmptyArgsAccepted(t *testing.T) {
	ts, _ := buildTaskService()
	d := NewDispatcher(registerListTasks(t, ts))

	for _, args := range []json.RawMessage{nil, {}, json.RawMessage("  \n\t "), json.RawMessage(`null`), json.RawMessage(`{}`)} {
		if _, err := d.Dispatch(context.Background(), "list_tasks", args); err != nil {
			t.Errorf("Dispatch with args %q: unexpected error: %v", args, err)
		}
	}
}

func TestDispatchExecutionErrorChain(t *testing.T) {
	r := NewRegistry()
	r.Register(
		Capability{
			Name:       "x",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		&fixedHandler{err: fmt.Errorf("%w: something went wrong", errInner)},
	)
	d := NewDispatcher(r)

	_, err := d.Dispatch(context.Background(), "x", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error from failing handler")
	}
	if !errors.Is(err, ErrExecutionFailed) {
		t.Errorf("errors.Is(err, ErrExecutionFailed) = false, err = %v", err)
	}
	if !errors.Is(err, errInner) {
		t.Errorf("errors.Is(err, errInner) = false: the inner error chain is lost, err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListTasksHandler integration
// ---------------------------------------------------------------------------

func TestListTasksHandlerReturnsRealData(t *testing.T) {
	now := time.Now().UTC()
	seed := []domain.Task{
		{ID: "a", Title: "Alpha", Status: domain.TaskStatusPending, Priority: domain.TaskPriorityLow, CreatedAt: now, UpdatedAt: now},
		{ID: "b", Title: "Beta", Status: domain.TaskStatusInProgress, Priority: domain.TaskPriorityHigh, CreatedAt: now, UpdatedAt: now},
	}
	ts, _ := buildTaskService(seed...)

	h := NewListTasksHandler(ts)
	result, err := h.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ltr, ok := result.Data.(ListTasksResult)
	if !ok {
		t.Fatalf("Data is %T, want ListTasksResult", result.Data)
	}
	if len(ltr.Tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(ltr.Tasks))
	}
	if ltr.Tasks[0].ID != "a" || ltr.Tasks[0].Title != "Alpha" {
		t.Errorf("unexpected first task view: %+v", ltr.Tasks[0])
	}
	if ltr.Tasks[1].Status != "in_progress" || ltr.Tasks[1].Priority != "high" {
		t.Errorf("status/priority must be plain enum strings, got %+v", ltr.Tasks[1])
	}
}

// ---------------------------------------------------------------------------
// Stable JSON contract
// ---------------------------------------------------------------------------

func TestListTasksStableJSONShape(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	seed := domain.Task{
		ID:          "t1",
		Title:       "Buy milk",
		Description: "urgent",
		Status:      domain.TaskStatusPending,
		Priority:    domain.TaskPriorityMedium,
		DueAt:       &now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	ts, _ := buildTaskService(seed)
	d := NewDispatcher(registerListTasks(t, ts))

	result, err := d.Dispatch(context.Background(), "list_tasks", json.RawMessage(`{}`))
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
	tasks, ok := data["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("data.tasks = %T (%d), want a single-task array", data["tasks"], len(tasks))
	}
	task := tasks[0].(map[string]any)

	wantTaskKeys := []string{"id", "title", "description", "status", "priority", "due_at", "created_at", "updated_at"}
	if len(task) != len(wantTaskKeys) {
		t.Fatalf("task keys = %v, want exactly %v", mapKeys(task), wantTaskKeys)
	}
	for _, k := range wantTaskKeys {
		if _, ok := task[k]; !ok {
			t.Errorf("missing task key %q", k)
		}
	}

	if task["id"] != "t1" || task["title"] != "Buy milk" || task["description"] != "urgent" {
		t.Errorf("unexpected scalar fields: %v", task)
	}
	if task["status"] != "pending" || task["priority"] != "medium" {
		t.Errorf("status/priority must be plain enum strings, got %v / %v", task["status"], task["priority"])
	}

	for _, k := range []string{"created_at", "updated_at", "due_at"} {
		s, ok := task[k].(string)
		if !ok {
			t.Fatalf("%s is %T, want string", k, task[k])
		}
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Errorf("%s = %q is not RFC3339: %v", k, s, err)
		}
		if !ts.Equal(now) {
			t.Errorf("%s = %v, want %v", k, ts, now)
		}
	}
}

func TestListTasksDueAtOmittedWhenNil(t *testing.T) {
	now := time.Date(2025, 2, 1, 8, 0, 0, 0, time.UTC)
	seed := domain.Task{
		ID:        "t2",
		Title:     "No due date",
		Status:    domain.TaskStatusPending,
		Priority:  domain.TaskPriorityLow,
		CreatedAt: now,
		UpdatedAt: now,
	}
	ts, _ := buildTaskService(seed)
	d := NewDispatcher(registerListTasks(t, ts))

	result, err := d.Dispatch(context.Background(), "list_tasks", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var outer map[string]any
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	tasks := outer["data"].(map[string]any)["tasks"].([]any)
	task := tasks[0].(map[string]any)

	if _, ok := task["due_at"]; ok {
		t.Errorf("due_at should be omitted when nil, got %v", task["due_at"])
	}
	if len(task) != 7 {
		t.Errorf("task keys = %v, want 7 keys without due_at", mapKeys(task))
	}
}

// mapKeys returns the sorted keys of a JSON object for readable assertions.
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
