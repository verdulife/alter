package capability

import (
	"context"
	"testing"

	"github.com/verdu/alter/internal/service"
)

// --- Tests ------------------------------------------------------------------

// TestRegisterShippedCapabilitiesRegistersListTasks verifies that the shipped
// registration function wires list_tasks into the registry: it appears in the
// catalog (Has returns true), carries a Description and a parseable Parameters
// schema, and the handler is non-nil (ready to execute). This is the single
// function cmd/alter/main.go calls.
func TestRegisterShippedCapabilitiesRegistersListTasks(t *testing.T) {
	ts, _ := buildTaskService()
	reg := NewRegistry()

	RegisterShippedCapabilities(reg, ts)

	if !reg.Has("list_tasks") {
		t.Fatal("registry must contain list_tasks after RegisterShippedCapabilities")
	}
	capDef, handler, err := reg.Get("list_tasks")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if handler == nil {
		t.Fatal("list_tasks handler must be wired")
	}
	if capDef.Description == "" {
		t.Error("list_tasks must carry a Description for the planner context")
	}
	if err := validateArgs(capDef.Parameters, []byte(`{}`)); err != nil {
		t.Errorf("Parameters must be parseable, got: %v", err)
	}
}

// TestPlannerContextDerivesFromSameRegistry verifies that the PlannerContext
// fed to Pi comes from the same Registry that the Dispatcher executes against:
// one source of truth, not two lists that can drift apart.
func TestPlannerContextDerivesFromSameRegistry(t *testing.T) {
	ts, _ := buildTaskService()
	reg := NewRegistry()
	RegisterShippedCapabilities(reg, ts)

	catalog := NewCatalog(reg)
	planner := NewPlannerContextBuilder(catalog)
	ctxDoc := planner.Build()

	if len(ctxDoc.Capabilities) != 4 {
		t.Fatalf("planner context capabilities = %d, want 4", len(ctxDoc.Capabilities))
	}
	// Same source: the entry parameters and description must match the
	// registry definition, for every shipped capability.
	for _, entry := range ctxDoc.Capabilities {
		capDef, _, err := reg.Get(entry.Name)
		if err != nil {
			t.Fatalf("Get(%q): %v", entry.Name, err)
		}
		if string(entry.Parameters) != string(capDef.Parameters) {
			t.Errorf("planner parameters %s != registry parameters %s", entry.Parameters, capDef.Parameters)
		}
		if entry.Description != capDef.Description {
			t.Errorf("planner description %q != registry description %q", entry.Description, capDef.Description)
		}
	}

	// Execution side: the same registry feeds the Dispatcher, so every shipped
	// capability is dispatchable right now through the same source.
	d := NewDispatcher(reg)
	if _, err := d.Dispatch(context.Background(), "list_tasks", []byte(`{}`)); err != nil {
		t.Errorf("Dispatch list_tasks through the same registry must succeed: %v", err)
	}
	if _, err := d.Dispatch(context.Background(), "create_task", []byte(`{"title":"x"}`)); err != nil {
		t.Errorf("Dispatch create_task through the same registry must succeed: %v", err)
	}
}

// TestListTasksHandlerSharesProvidedTaskService proves that the handler
// registered via RegisterShippedCapabilities operates on exactly the
// TaskService instance supplied by the caller. A task created through the
// service must appear in the dispatch result; no second service is involved.
func TestListTasksHandlerSharesProvidedTaskService(t *testing.T) {
	ts, _ := buildTaskService()
	created, err := ts.Create(context.Background(), service.CreateTaskParams{Title: "comprar leche"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	reg := NewRegistry()
	RegisterShippedCapabilities(reg, ts)

	d := NewDispatcher(reg)
	raw, err := d.Dispatch(context.Background(), "list_tasks", []byte(`{}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	result, ok := raw.Data.(ListTasksResult)
	if !ok {
		t.Fatalf("unexpected result type %T", raw.Data)
	}
	if len(result.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(result.Tasks))
	}
	if result.Tasks[0].Title != created.Title {
		t.Errorf("task title = %q, want %q", result.Tasks[0].Title, created.Title)
	}
	if result.Tasks[0].ID != created.ID {
		t.Errorf("task ID = %q, want %q", result.Tasks[0].ID, created.ID)
	}
}

// TestRegisterShippedCapabilitiesWiresCancelTask verifies that the shipped
// registration function wires cancel_task into the registry: it appears in the
// catalog, carries a Description and a parseable Parameters schema, and the
// handler is non-nil.
func TestRegisterShippedCapabilitiesWiresCancelTask(t *testing.T) {
	ts, _ := buildTaskService()
	reg := NewRegistry()

	RegisterShippedCapabilities(reg, ts)

	if !reg.Has("cancel_task") {
		t.Fatal("registry must contain cancel_task after RegisterShippedCapabilities")
	}
	capDef, handler, err := reg.Get("cancel_task")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if handler == nil {
		t.Fatal("cancel_task handler must be wired")
	}
	if capDef.Description == "" {
		t.Error("cancel_task must carry a Description for the planner context")
	}
	if err := validateArgs(capDef.Parameters, []byte(`{"task_ref":"x"}`)); err != nil {
		t.Errorf("Parameters must be parseable, got: %v", err)
	}
}

// TestCancelTaskHandlerSharesProvidedTaskService proves that the handler
// registered via RegisterShippedCapabilities operates on exactly the
// TaskService instance supplied by the caller. A task created through the
// service must be cancellable through the dispatch result; no second service is
// involved.
func TestCancelTaskHandlerSharesProvidedTaskService(t *testing.T) {
	ts, _ := buildTaskService()
	created, err := ts.Create(context.Background(), service.CreateTaskParams{Title: "comprar leche"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	reg := NewRegistry()
	RegisterShippedCapabilities(reg, ts)

	d := NewDispatcher(reg)
	raw, err := d.Dispatch(context.Background(), "cancel_task", []byte(`{"task_ref":"leche"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	result, ok := raw.Data.(CancelTaskResult)
	if !ok {
		t.Fatalf("unexpected result type %T", raw.Data)
	}
	if result.Task.ID != created.ID {
		t.Errorf("task ID = %q, want %q", result.Task.ID, created.ID)
	}
	if result.Task.Status != "cancelled" {
		t.Errorf("task status = %q, want %q", result.Task.Status, "cancelled")
	}
}
