package application

import (
	"fmt"
	"strings"
	"time"

	"github.com/verdu/alter/internal/capability"
)

// FlowDeliveryText derives the user-facing delivery text from a FlowResult.
// A conversation carries its text unchanged; a plan carries the executed
// capability results rendered as human-readable text (never internal JSON).
//
// It is the single presentation seam shared by the Scheduler Orchestrator and
// the inbound free-text AgentFlow handler, so the delivered shape of a plan is
// identical regardless of the entry point.
func FlowDeliveryText(r FlowResult) string {
	if r.Kind == capability.ResponseConversation {
		return r.Response
	}
	if len(r.Results) == 0 {
		return ""
	}
	lines := make([]string, 0, len(r.Results))
	for _, res := range r.Results {
		lines = append(lines, renderResult(res.Data))
	}
	return strings.Join(lines, "\n")
}

// renderResult renders one capability result as human-readable text. Known
// capability results have dedicated shapes; unknown or empty results fall back
// to a generic success line so delivery never breaks and internal JSON never
// leaks into the reply.
func renderResult(data any) string {
	switch v := data.(type) {
	case capability.CreateTaskResult:
		return renderCreatedTask(v.Task)
	case capability.CompleteTaskResult:
		return "✅ Tarea completada: " + v.Task.Title
	case capability.ListTasksResult:
		return renderTaskList(v.Tasks)
	default:
		return "✅ Operación completada."
	}
}

// renderCreatedTask renders a created task. The title is always shown; the
// description, an explicit priority and the due date are added when they add
// information.
func renderCreatedTask(t capability.TaskView) string {
	var sb strings.Builder
	sb.WriteString("✅ Tarea creada: ")
	sb.WriteString(t.Title)

	if t.Description != "" {
		sb.WriteString("\n   Descripción: ")
		sb.WriteString(t.Description)
	}
	if p := priorityLabel(t.Priority); p != "" {
		sb.WriteString("\n   Prioridad: ")
		sb.WriteString(p)
	}
	if t.DueAt != nil {
		sb.WriteString("\n   Vence: ")
		sb.WriteString(formatDue(*t.DueAt))
	}
	return sb.String()
}

// renderTaskList renders the task list as numbered lines, showing the status
// only when it is not the default pending. An empty list is reported clearly.
func renderTaskList(tasks []capability.TaskView) string {
	if len(tasks) == 0 {
		return "No tenés tareas pendientes."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Tus tareas (%d):", len(tasks))
	for i, t := range tasks {
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "%d) %s", i+1, t.Title)
		if s := statusLabel(t.Status); s != "" {
			fmt.Fprintf(&sb, " [%s]", s)
		}
	}
	return sb.String()
}

// priorityLabel maps a priority string to a readable Spanish label. The
// implicit default (medium) carries no information and is omitted; unknown
// values pass through so the title is never lost.
func priorityLabel(p string) string {
	switch p {
	case "low":
		return "baja"
	case "high":
		return "alta"
	case "urgent":
		return "urgente"
	case "medium", "":
		return ""
	default:
		return p
	}
}

// statusLabel maps a task status string to a readable Spanish label. Pending is
// the default and is omitted; unknown values pass through.
func statusLabel(s string) string {
	switch s {
	case "pending":
		return ""
	case "in_progress":
		return "en progreso"
	case "completed":
		return "completada"
	case "cancelled":
		return "cancelada"
	default:
		return s
	}
}

// formatDue renders an RFC3339 due date as a readable UTC timestamp,
// falling back to the raw string on an unparseable value (defensive).
func formatDue(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.UTC().Format("02/01/2006 15:04") + " UTC"
}
