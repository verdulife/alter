package application

import (
	"strings"
	"testing"

	"github.com/verdu/alter/internal/capability"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func planResult(results ...capability.CapabilityResult) FlowResult {
	return FlowResult{Kind: capability.ResponsePlan, Results: results}
}

func buildTaskView(id, title, description, status, priority string) capability.TaskView {
	return capability.TaskView{
		ID:          id,
		Title:       title,
		Description: description,
		Status:      status,
		Priority:    priority,
	}
}

func buildTaskViewWithDue(id, title, status, priority, due string) capability.TaskView {
	v := buildTaskView(id, title, "", status, priority)
	v.DueAt = &due
	return v
}

// ---------------------------------------------------------------------------
// Conversation — unchanged
// ---------------------------------------------------------------------------

func TestFlowDeliveryConversationUnchanged(t *testing.T) {
	r := FlowResult{Kind: capability.ResponseConversation, Response: "Hola, puedo crear tareas y recordatorios."}
	if got := FlowDeliveryText(r); got != r.Response {
		t.Errorf("conversation text = %q, want unchanged %q", got, r.Response)
	}
}

func TestFlowDeliveryConversationWithBracesNotPlan(t *testing.T) {
	text := "tienes {tareas} pendientes"
	r := FlowResult{Kind: capability.ResponseConversation, Response: text}
	if got := FlowDeliveryText(r); got != text {
		t.Errorf("conversation text = %q, want unchanged %q", got, text)
	}
}

// ---------------------------------------------------------------------------
// Empty plan
// ---------------------------------------------------------------------------

func TestFlowDeliveryEmptyPlanReturnsEmpty(t *testing.T) {
	r := planResult()
	if got := FlowDeliveryText(r); got != "" {
		t.Errorf("empty plan text = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// create_task
// ---------------------------------------------------------------------------

func TestFlowDeliveryCreateTaskText(t *testing.T) {
	res := capability.CapabilityResult{
		Data: capability.CreateTaskResult{Task: buildTaskView("t1", "comprar leche", "", "pending", "medium")},
	}
	got := FlowDeliveryText(planResult(res))
	want := "✅ Tarea creada: comprar leche"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlowDeliveryCreateTaskRichText(t *testing.T) {
	due := "2025-03-01T20:00:00Z"
	res := capability.CapabilityResult{
		Data: capability.CreateTaskResult{
			Task: capability.TaskView{
				Title:       "pagar la luz",
				Description: "antes del corte",
				Priority:    "high",
				DueAt:       &due,
				Status:      "pending",
			},
		},
	}
	got := FlowDeliveryText(planResult(res))
	want := "✅ Tarea creada: pagar la luz\n   Descripción: antes del corte\n   Prioridad: alta\n   Vence: 01/03/2025 20:00 UTC"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlowDeliveryCreateTaskPriorityOnlyHigh(t *testing.T) {
	res := capability.CapabilityResult{
		Data: capability.CreateTaskResult{Task: buildTaskView("t1", "urgente", "", "pending", "urgent")},
	}
	got := FlowDeliveryText(planResult(res))
	want := "✅ Tarea creada: urgente\n   Prioridad: urgente"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlowDeliveryCreateTaskOmitsDefaultPriority(t *testing.T) {
	// medium is the default and must not appear
	res := capability.CapabilityResult{
		Data: capability.CreateTaskResult{Task: buildTaskView("t1", "sin prioridad explícita", "", "pending", "medium")},
	}
	got := FlowDeliveryText(planResult(res))
	want := "✅ Tarea creada: sin prioridad explícita"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// complete_task
// ---------------------------------------------------------------------------

func TestFlowDeliveryCompleteTaskText(t *testing.T) {
	res := capability.CapabilityResult{
		Data: capability.CompleteTaskResult{
			Task: buildTaskView("t1", "comprar SSD", "", "completed", "medium"),
		},
	}
	got := FlowDeliveryText(planResult(res))
	want := "✅ Tarea completada: comprar SSD"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlowDeliveryCompleteTaskLongTitle(t *testing.T) {
	title := "llamar al fontanero para arreglar la filtración del baño"
	res := capability.CapabilityResult{
		Data: capability.CompleteTaskResult{
			Task: buildTaskView("t1", title, "", "completed", "high"),
		},
	}
	got := FlowDeliveryText(planResult(res))
	want := "✅ Tarea completada: " + title
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// list_tasks
// ---------------------------------------------------------------------------

func TestFlowDeliveryListTasksText(t *testing.T) {
	res := capability.CapabilityResult{
		Data: capability.ListTasksResult{
			Tasks: []capability.TaskView{
				buildTaskView("t1", "comprar SSD", "", "pending", "medium"),
				buildTaskView("t2", "llamar al fontanero", "", "completed", "low"),
			},
		},
	}
	got := FlowDeliveryText(planResult(res))
	want := "Tus tareas (2):\n1) comprar SSD\n2) llamar al fontanero [completada]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlowDeliveryListTasksMultipleStatuses(t *testing.T) {
	res := capability.CapabilityResult{
		Data: capability.ListTasksResult{
			Tasks: []capability.TaskView{
				buildTaskView("t1", "in progress task", "", "in_progress", "medium"),
				buildTaskView("t2", "cancelled task", "", "cancelled", "low"),
				buildTaskView("t3", "pending task", "", "pending", "medium"),
			},
		},
	}
	got := FlowDeliveryText(planResult(res))
	want := "Tus tareas (3):\n1) in progress task [en progreso]\n2) cancelled task [cancelada]\n3) pending task"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlowDeliveryListTasksEmpty(t *testing.T) {
	res := capability.CapabilityResult{
		Data: capability.ListTasksResult{Tasks: []capability.TaskView{}},
	}
	got := FlowDeliveryText(planResult(res))
	want := "No tenés tareas pendientes."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlowDeliveryListTasksSingleTask(t *testing.T) {
	res := capability.CapabilityResult{
		Data: capability.ListTasksResult{
			Tasks: []capability.TaskView{
				buildTaskView("t1", "comprar leche", "", "pending", "medium"),
			},
		},
	}
	got := FlowDeliveryText(planResult(res))
	want := "Tus tareas (1):\n1) comprar leche"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Unknown capability — fallback
// ---------------------------------------------------------------------------

func TestFlowDeliveryUnknownCapabilityFallback(t *testing.T) {
	res := capability.CapabilityResult{Data: map[string]any{"ok": true}}
	got := FlowDeliveryText(planResult(res))
	want := "✅ Operación completada."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlowDeliveryNilDataFallback(t *testing.T) {
	res := capability.CapabilityResult{} // Data is nil
	got := FlowDeliveryText(planResult(res))
	want := "✅ Operación completada."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlowDeliveryStringDataFallback(t *testing.T) {
	// A capability returning a plain string (e.g. the old V1 behavior)
	res := capability.CapabilityResult{Data: "done"}
	got := FlowDeliveryText(planResult(res))
	want := "✅ Operación completada."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// No internal JSON leak
// ---------------------------------------------------------------------------

func TestFlowDeliveryNeverLeaksInternalJSON(t *testing.T) {
	cases := []struct {
		name string
		res  FlowResult
	}{
		{"create_task", planResult(capability.CapabilityResult{
			Data: capability.CreateTaskResult{Task: buildTaskView("t1", "comprar SSD", "NVMe 1TB", "pending", "high")},
		})},
		{"complete_task", planResult(capability.CapabilityResult{
			Data: capability.CompleteTaskResult{Task: buildTaskView("t1", "comprar SSD", "", "completed", "medium")},
		})},
		{"list_tasks", planResult(capability.CapabilityResult{
			Data: capability.ListTasksResult{
				Tasks: []capability.TaskView{
					buildTaskView("t1", "comprar SSD", "", "pending", "medium"),
				},
			},
		})},
		{"unknown", planResult(capability.CapabilityResult{Data: map[string]any{"ok": true}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FlowDeliveryText(tc.res)
			if strings.Contains(got, "{") || strings.Contains(got, "[") || strings.Contains(got, `"`) {
				t.Errorf("output leaks JSON: %q", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Multiple results in a single plan
// ---------------------------------------------------------------------------

func TestFlowDeliveryMultipleResultsJoined(t *testing.T) {
	createRes := capability.CapabilityResult{
		Data: capability.CreateTaskResult{Task: buildTaskView("t1", "comprar leche", "", "pending", "medium")},
	}
	completeRes := capability.CapabilityResult{
		Data: capability.CompleteTaskResult{Task: buildTaskView("t2", "comprar SSD", "", "completed", "medium")},
	}
	got := FlowDeliveryText(planResult(createRes, completeRes))
	want := "✅ Tarea creada: comprar leche\n✅ Tarea completada: comprar SSD"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// JSON contract: task list has no due_at field when nil
// ---------------------------------------------------------------------------

func TestFlowDeliveryListTaskViewContract(t *testing.T) {
	// Verify the TaskView contract: due_at is only present when non-nil in the
	// rendered output (the list never shows due_at, confirming no JSON leak).
	res := capability.CapabilityResult{
		Data: capability.ListTasksResult{
			Tasks: []capability.TaskView{
				buildTaskViewWithDue("t1", "con fecha", "pending", "medium", "2025-03-01T10:00:00Z"),
				buildTaskView("t2", "sin fecha", "", "pending", "medium"),
			},
		},
	}
	got := FlowDeliveryText(planResult(res))
	if strings.Contains(got, "2025-03-01") {
		t.Errorf("list output should not contain RFC3339 dates: %q", got)
	}
	want := "Tus tareas (2):\n1) con fecha\n2) sin fecha"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Priority rendering edge cases
// ---------------------------------------------------------------------------

func TestFlowDeliveryCreateTaskLowPriority(t *testing.T) {
	res := capability.CapabilityResult{
		Data: capability.CreateTaskResult{Task: buildTaskView("t1", "tarea baja", "", "pending", "low")},
	}
	got := FlowDeliveryText(planResult(res))
	want := "✅ Tarea creada: tarea baja\n   Prioridad: baja"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlowDeliveryCreateTaskWithDueDate(t *testing.T) {
	due := "2025-12-31T23:59:59Z"
	res := capability.CapabilityResult{
		Data: capability.CreateTaskResult{Task: buildTaskViewWithDue("t1", "fin de año", "pending", "medium", due)},
	}
	got := FlowDeliveryText(planResult(res))
	want := "✅ Tarea creada: fin de año\n   Vence: 31/12/2025 23:59 UTC"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
