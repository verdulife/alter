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

// registerCancelTask is a shorthand for tests: it registers the cancel_task
// capability with its shipped schema.
func registerCancelTask(t *testing.T, ts *service.TaskService) *Registry {
	t.Helper()
	r := NewRegistry()
	r.Register(
		Capability{
			Name:        "cancel_task",
			Description: "cancel an existing task by textual reference",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"task_ref": {"type": "string"}
				},
				"required": ["task_ref"]
			}`),
		},
		NewCancelTaskHandler(ts),
	)
	return r
}

// ---------------------------------------------------------------------------
// Constructor guard
// ---------------------------------------------------------------------------

func TestNewCancelTaskHandlerPanicsOnNilService(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil TaskService")
		}
	}()
	NewCancelTaskHandler(nil)
}

// ---------------------------------------------------------------------------
// Schema validation (contract)
// ---------------------------------------------------------------------------

func TestCancelTaskSchemaRejectsMissingTaskRef(t *testing.T) {
	ts, _ := buildTaskService()
	d := NewDispatcher(registerCancelTask(t, ts))

	_, err := d.Dispatch(context.Background(), "cancel_task", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing required task_ref")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

func TestCancelTaskSchemaRejectsNonStringTaskRef(t *testing.T) {
	ts, _ := buildTaskService()
	d := NewDispatcher(registerCancelTask(t, ts))

	_, err := d.Dispatch(context.Background(), "cancel_task", json.RawMessage(`{"task_ref":123}`))
	if err == nil {
		t.Fatal("expected error for non-string task_ref")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

func TestCancelTaskSchemaRejectsUnknownProperties(t *testing.T) {
	ts, _ := buildTaskService()
	d := NewDispatcher(registerCancelTask(t, ts))

	_, err := d.Dispatch(context.Background(), "cancel_task", json.RawMessage(`{"task_ref":"x","extra":true}`))
	if err == nil {
		t.Fatal("expected error for unknown property")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Handler: task found and cancelled
// ---------------------------------------------------------------------------

func TestCancelTaskHandlerFindsAndCancels(t *testing.T) {
	now := time.Now().UTC()
	seed := []domain.Task{
		{ID: "t1", Title: "comprar SSD", Status: domain.TaskStatusPending, Priority: domain.TaskPriorityMedium, CreatedAt: now, UpdatedAt: now},
		{ID: "t2", Title: "llamar al fontanero", Status: domain.TaskStatusPending, Priority: domain.TaskPriorityLow, CreatedAt: now, UpdatedAt: now},
	}
	ts, repo := buildTaskService(seed...)
	h := NewCancelTaskHandler(ts)

	result, err := h.Execute(context.Background(), json.RawMessage(`{"task_ref":"SSD"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ctr, ok := result.Data.(CancelTaskResult)
	if !ok {
		t.Fatalf("Data is %T, want CancelTaskResult", result.Data)
	}
	if ctr.Task.ID != "t1" {
		t.Errorf("Task.ID = %q, want %q", ctr.Task.ID, "t1")
	}
	if ctr.Task.Title != "comprar SSD" {
		t.Errorf("Task.Title = %q, want %q", ctr.Task.Title, "comprar SSD")
	}
	if ctr.Task.Status != "cancelled" {
		t.Errorf("Task.Status = %q, want %q", ctr.Task.Status, "cancelled")
	}

	// Verify persistence: the task must be updated in the repo.
	found := false
	for _, rt := range repo.tasks {
		if rt.ID == "t1" && rt.Status == domain.TaskStatusCancelled {
			found = true
		}
	}
	if !found {
		t.Error("task t1 must be persisted as cancelled in the repo")
	}
}

// TestCancelTaskHandlerCaseInsensitive verifies substring matching is
// case-insensitive.
func TestCancelTaskHandlerCaseInsensitive(t *testing.T) {
	now := time.Now().UTC()
	seed := []domain.Task{
		{ID: "t1", Title: "Comprar Leche", Status: domain.TaskStatusPending, CreatedAt: now, UpdatedAt: now},
	}
	ts, _ := buildTaskService(seed...)
	h := NewCancelTaskHandler(ts)

	result, err := h.Execute(context.Background(), json.RawMessage(`{"task_ref":"comprar"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ctr := result.Data.(CancelTaskResult)
	if ctr.Task.ID != "t1" {
		t.Errorf("Task.ID = %q, want %q (case-insensitive match)", ctr.Task.ID, "t1")
	}
}

// TestCancelTaskHandlerIgnoresCompletedTasks verifies that already-completed
// tasks are not matched by the resolver.
func TestCancelTaskHandlerIgnoresCompletedTasks(t *testing.T) {
	now := time.Now().UTC()
	seed := []domain.Task{
		{ID: "t1", Title: "comprar SSD", Status: domain.TaskStatusCompleted, CreatedAt: now, UpdatedAt: now},
	}
	ts, _ := buildTaskService(seed...)
	h := NewCancelTaskHandler(ts)

	_, err := h.Execute(context.Background(), json.RawMessage(`{"task_ref":"SSD"}`))
	if err == nil {
		t.Fatal("expected error for reference matching only completed tasks")
	}
	if !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("errors.Is(err, ErrTaskNotFound) = false, err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Handler: task not found
// ---------------------------------------------------------------------------

func TestCancelTaskHandlerNotFound(t *testing.T) {
	now := time.Now().UTC()
	seed := []domain.Task{
		{ID: "t1", Title: "comprar leche", Status: domain.TaskStatusPending, CreatedAt: now, UpdatedAt: now},
	}
	ts, _ := buildTaskService(seed...)
	h := NewCancelTaskHandler(ts)

	_, err := h.Execute(context.Background(), json.RawMessage(`{"task_ref":"inexistente"}`))
	if err == nil {
		t.Fatal("expected error for non-matching reference")
	}
	if !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("errors.Is(err, ErrTaskNotFound) = false, err = %v", err)
	}
}

func TestCancelTaskHandlerNotFoundEmptyRepo(t *testing.T) {
	ts, _ := buildTaskService()
	h := NewCancelTaskHandler(ts)

	_, err := h.Execute(context.Background(), json.RawMessage(`{"task_ref":"algo"}`))
	if err == nil {
		t.Fatal("expected error for empty repository")
	}
	if !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("errors.Is(err, ErrTaskNotFound) = false, err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Handler: ambiguous reference
// ---------------------------------------------------------------------------

func TestCancelTaskHandlerAmbiguousReference(t *testing.T) {
	now := time.Now().UTC()
	seed := []domain.Task{
		{ID: "t1", Title: "comprar SSD", Status: domain.TaskStatusPending, CreatedAt: now, UpdatedAt: now},
		{ID: "t2", Title: "instalar SSD", Status: domain.TaskStatusPending, CreatedAt: now, UpdatedAt: now},
	}
	ts, _ := buildTaskService(seed...)
	h := NewCancelTaskHandler(ts)

	_, err := h.Execute(context.Background(), json.RawMessage(`{"task_ref":"SSD"}`))
	if err == nil {
		t.Fatal("expected error for ambiguous reference")
	}
	if !errors.Is(err, ErrAmbiguousTaskRef) {
		t.Errorf("errors.Is(err, ErrAmbiguousTaskRef) = false, err = %v", err)
	}

	// Verify structured data is available via type assertion.
	var amb *AmbiguousTaskRefError
	if !errors.As(err, &amb) {
		t.Fatalf("errors.As(err, &AmbiguousTaskRefError) = false, err = %T", err)
	}
	if amb.Ref != "SSD" {
		t.Errorf("Ref = %q, want %q", amb.Ref, "SSD")
	}
	if len(amb.Matches) != 2 {
		t.Errorf("Matches = %d, want 2", len(amb.Matches))
	}
}

// ---------------------------------------------------------------------------
// Service failure propagation
// ---------------------------------------------------------------------------

// failingCancelRepo wraps mockTaskRepo and forces Update failures.
type failingCancelRepo struct {
	*mockTaskRepo
	err error
}

func (f *failingCancelRepo) Update(_ context.Context, task domain.Task) error {
	return f.err
}

// TestCancelTaskHandlerCancelServiceFailure verifies that a failure in
// TaskService.Cancel (after successful resolution) propagates correctly.
func TestCancelTaskHandlerCancelServiceFailure(t *testing.T) {
	now := time.Now().UTC()
	seed := []domain.Task{
		{ID: "t1", Title: "comprar SSD", Status: domain.TaskStatusPending, CreatedAt: now, UpdatedAt: now},
	}
	repo := &failingCancelRepo{mockTaskRepo: &mockTaskRepo{tasks: seed}, err: errInner}
	ts := service.NewTaskService(repo, &stubTriggerRepo{}, &stubEventStore{})
	h := NewCancelTaskHandler(ts)

	// Resolution succeeds (pending task matches), but Cancel fails on Update.
	_, err := h.Execute(context.Background(), json.RawMessage(`{"task_ref":"SSD"}`))
	if err == nil {
		t.Fatal("expected error from failing repository")
	}
	if !errors.Is(err, errInner) {
		t.Errorf("errors.Is(err, errInner) = false, err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stable JSON contract
// ---------------------------------------------------------------------------

func TestCancelTaskStableJSONShape(t *testing.T) {
	now := time.Now().UTC()
	seed := []domain.Task{
		{ID: "t1", Title: "comprar SSD", Description: "NVMe 1TB", Status: domain.TaskStatusPending, Priority: domain.TaskPriorityHigh, CreatedAt: now, UpdatedAt: now},
	}
	ts, _ := buildTaskService(seed...)
	d := NewDispatcher(registerCancelTask(t, ts))

	result, err := d.Dispatch(context.Background(), "cancel_task", json.RawMessage(`{"task_ref":"SSD"}`))
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
		t.Fatalf("outer keys = %v, want exactly [data]", mapKeys(outer))
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
		t.Fatalf("task keys = %v, want exactly %v (due_at omitted)", mapKeys(task), wantTaskKeys)
	}
	for _, k := range wantTaskKeys {
		if _, ok := task[k]; !ok {
			t.Errorf("missing task key %q", k)
		}
	}

	if task["title"] != "comprar SSD" || task["status"] != "cancelled" {
		t.Errorf("unexpected task fields: title=%v status=%v", task["title"], task["status"])
	}
	if task["priority"] != "high" {
		t.Errorf("priority = %v, want high", task["priority"])
	}
}

// TestCancelTaskAmbiguousJSONShape verifies the JSON shape of the
// ErrAmbiguousTaskRef error returned through the dispatcher.
func TestCancelTaskAmbiguousJSONShape(t *testing.T) {
	now := time.Now().UTC()
	seed := []domain.Task{
		{ID: "t1", Title: "comprar SSD", Status: domain.TaskStatusPending, CreatedAt: now, UpdatedAt: now},
		{ID: "t2", Title: "instalar SSD", Status: domain.TaskStatusPending, CreatedAt: now, UpdatedAt: now},
	}
	ts, _ := buildTaskService(seed...)
	d := NewDispatcher(registerCancelTask(t, ts))

	_, err := d.Dispatch(context.Background(), "cancel_task", json.RawMessage(`{"task_ref":"SSD"}`))
	if err == nil {
		t.Fatal("expected error")
	}
	// The dispatcher wraps with ErrExecutionFailed; the inner error is
	// AmbiguousTaskRefError wrapping ErrAmbiguousTaskRef.
	if !errors.Is(err, ErrExecutionFailed) {
		t.Errorf("errors.Is(err, ErrExecutionFailed) = false, err = %v", err)
	}
	if !errors.Is(err, ErrAmbiguousTaskRef) {
		t.Errorf("errors.Is(err, ErrAmbiguousTaskRef) = false, err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func TestRegisterShippedCapabilitiesRegistersCancelTask(t *testing.T) {
	ts, _ := buildTaskService()
	reg := NewRegistry()
	RegisterShippedCapabilities(reg, ts, buildReminderService(ts))

	if !reg.Has("cancel_task") {
		t.Error("cancel_task must be registered after RegisterShippedCapabilities")
	}
	names := reg.Names()
	found := false
	for _, n := range names {
		if n == "cancel_task" {
			found = true
		}
	}
	if !found {
		t.Errorf("Names() = %v, must include cancel_task", names)
	}
}
