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

// registerCreateTask is a shorthand used by dispatcher tests: it registers the
// create_task capability with its shipped schema (title required).
func registerCreateTask(t *testing.T, ts *service.TaskService) *Registry {
	t.Helper()
	r := NewRegistry()
	r.Register(
		Capability{
			Name:        "create_task",
			Description: "create a new task",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"title":       {"type": "string"},
					"description": {"type": "string"},
					"priority":    {"type": "string"},
					"due_at":      {"type": "string"}
				},
				"required": ["title"]
			}`),
		},
		NewCreateTaskHandler(ts),
	)
	return r
}

// ---------------------------------------------------------------------------
// Constructor guard
// ---------------------------------------------------------------------------

func TestNewCreateTaskHandlerPanicsOnNilService(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil TaskService")
		}
	}()
	NewCreateTaskHandler(nil)
}

// ---------------------------------------------------------------------------
// Schema validation (contract)
// ---------------------------------------------------------------------------

func TestCreateTaskSchemaRejectsMissingTitle(t *testing.T) {
	ts, _ := buildTaskService()
	d := NewDispatcher(registerCreateTask(t, ts))

	_, err := d.Dispatch(context.Background(), "create_task", json.RawMessage(`{"description":"sin titulo"}`))
	if err == nil {
		t.Fatal("expected error for missing required title")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

func TestCreateTaskSchemaRejectsNonStringTitle(t *testing.T) {
	ts, _ := buildTaskService()
	d := NewDispatcher(registerCreateTask(t, ts))

	_, err := d.Dispatch(context.Background(), "create_task", json.RawMessage(`{"title":123}`))
	if err == nil {
		t.Fatal("expected error for non-string title")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

func TestCreateTaskSchemaRejectsUnknownProperties(t *testing.T) {
	ts, _ := buildTaskService()
	d := NewDispatcher(registerCreateTask(t, ts))

	_, err := d.Dispatch(context.Background(), "create_task", json.RawMessage(`{"title":"x","bogus":true}`))
	if err == nil {
		t.Fatal("expected error for unknown property")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Handler integration
// ---------------------------------------------------------------------------

// TestCreateTaskHandlerPersistsRealTask verifies that Execute delegates to the
// shared TaskService: the created task is persisted and returned with the
// service-managed fields (ID, pending status, RFC3339 timestamps).
func TestCreateTaskHandlerPersistsRealTask(t *testing.T) {
	ts, repo := buildTaskService()
	h := NewCreateTaskHandler(ts)

	result, err := h.Execute(context.Background(), json.RawMessage(`{"title":"comprar leche"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ctr, ok := result.Data.(CreateTaskResult)
	if !ok {
		t.Fatalf("Data is %T, want CreateTaskResult", result.Data)
	}
	task := ctr.Task

	if task.ID == "" {
		t.Error("expected a generated ID")
	}
	if task.Title != "comprar leche" {
		t.Errorf("Title = %q, want %q", task.Title, "comprar leche")
	}
	if task.Status != "pending" {
		t.Errorf("Status = %q, want %q", task.Status, "pending")
	}
	if task.Priority != "medium" {
		t.Errorf("Priority default = %q, want %q", task.Priority, "medium")
	}

	if len(repo.tasks) != 1 || repo.tasks[0].ID != task.ID {
		t.Errorf("task must be persisted in the shared repository, got %d tasks", len(repo.tasks))
	}
	if task.CreatedAt == "" || task.UpdatedAt == "" {
		t.Error("timestamps must be present")
	}
}

// TestCreateTaskHandlerFullArgs verifies the allocation of every optional
// field: description, explicit priority and RFC3339 due_at.
func TestCreateTaskHandlerFullArgs(t *testing.T) {
	ts, _ := buildTaskService()
	h := NewCreateTaskHandler(ts)

	result, err := h.Execute(context.Background(), json.RawMessage(`{
		"title":       "pagina la luz",
		"description": "antes del corte",
		"priority":    "high",
		"due_at":      "2025-03-01T20:00:00Z"
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	task := result.Data.(CreateTaskResult).Task

	if task.Description != "antes del corte" {
		t.Errorf("Description = %q, want %q", task.Description, "antes del corte")
	}
	if task.Priority != "high" {
		t.Errorf("Priority = %q, want %q", task.Priority, "high")
	}
	if task.DueAt == nil {
		t.Fatal("DueAt must be set")
	}
	due, err := time.Parse(time.RFC3339, *task.DueAt)
	if err != nil {
		t.Fatalf("DueAt %q is not RFC3339: %v", *task.DueAt, err)
	}
	if !due.Equal(time.Date(2025, 3, 1, 20, 0, 0, 0, time.UTC)) {
		t.Errorf("DueAt = %v, want 2025-03-01T20:00:00Z", due)
	}
}

func TestCreateTaskHandlerRejectsMalformedDueAt(t *testing.T) {
	ts, _ := buildTaskService()
	h := NewCreateTaskHandler(ts)

	_, err := h.Execute(context.Background(), json.RawMessage(`{"title":"x","due_at":"manana a las 8"}`))
	if err == nil {
		t.Fatal("expected error for non-RFC3339 due_at")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stable JSON contract
// ---------------------------------------------------------------------------

// TestCreateTaskStableJSONShape pins the external JSON shape: exactly the
// "data" key with a single "task" object carrying the same stable keys list_tasks
// uses (Source is internal and never exposed).
func TestCreateTaskStableJSONShape(t *testing.T) {
	ts, _ := buildTaskService()
	d := NewDispatcher(registerCreateTask(t, ts))

	result, err := d.Dispatch(context.Background(), "create_task", json.RawMessage(`{"title":"comprar SSD"}`))
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

	if task["title"] != "comprar SSD" || task["status"] != "pending" || task["priority"] != "medium" {
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

// TestCreateTaskDueAtOmittedWhenNil pins that due_at is omitted from the JSON
// when the task has no due date (same contract as list_tasks).
func TestCreateTaskDueAtOmittedWhenNil(t *testing.T) {
	ts, _ := buildTaskService()
	d := NewDispatcher(registerCreateTask(t, ts))

	result, err := d.Dispatch(context.Background(), "create_task", json.RawMessage(`{"title":"sin fecha"}`))
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
	task := outer["data"].(map[string]any)["task"].(map[string]any)

	if _, ok := task["due_at"]; ok {
		t.Errorf("due_at should be omitted when nil, got %v", task["due_at"])
	}
	if len(task) != 7 {
		t.Errorf("task keys = %v, want 7 keys without due_at", mapKeys(task))
	}
}

// failingCreateRepo is a test-local double that wraps the shared mockTaskRepo
// and forces Create failures, mirroring the createErr injection of the service
// package fakes without touching the shared double.
type failingCreateRepo struct {
	*mockTaskRepo
	err error
}

func (f *failingCreateRepo) Create(context.Context, domain.Task) error { return f.err }

// TestCreateTaskServiceFailurePropagates proves that a repository failure
// surfaces through the handler (wrapped by the dispatcher as
// ErrExecutionFailed) instead of being swallowed.
func TestCreateTaskServiceFailurePropagates(t *testing.T) {
	repo := &failingCreateRepo{mockTaskRepo: &mockTaskRepo{}, err: errNotFound}
	ts := service.NewTaskService(repo, &stubTriggerRepo{}, &stubEventStore{})
	h := NewCreateTaskHandler(ts)

	_, err := h.Execute(context.Background(), json.RawMessage(`{"title":"x"}`))
	if err == nil {
		t.Fatal("expected error from failing repository")
	}
	if !errors.Is(err, errNotFound) {
		t.Errorf("errors.Is(err, errNotFound) = false, err = %v", err)
	}
}
