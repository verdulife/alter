package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/verdu/alter/internal/domain"
)

const (
	cmdNueva    = "/nueva"
	cmdRecordar = "/recordar"
	cmdListar   = "/listar"
	cmdCompletar = "/completar"
	cmdCancelar  = "/cancelar"
)

// Sentinel parsing/validation errors for the /recordar command.
var (
	errReminderSyntax   = errors.New("invalid reminder syntax")
	errReminderEmpty    = errors.New("reminder title empty")
	errReminderDuration = errors.New("invalid or non-positive duration")
)

// CommandService is the minimal application surface the Telegram inbound commands
// need.
type CommandService interface {
	CreateTask(ctx context.Context, title string) (domain.Task, error)
	// CreateReminder creates a Task plus its one-shot "at" Trigger scheduled
	// `in` from now. It is the single /recordar operation.
	CreateReminder(ctx context.Context, title string, in time.Duration) (domain.Task, error)
	// ListPendingTasks returns all pending tasks.
	ListPendingTasks(ctx context.Context) ([]domain.Task, error)
	// CompleteTaskByRef finds a pending task by textual reference and completes it.
	// Returns the resolved task and the reply text.
	CompleteTaskByRef(ctx context.Context, ref string) (domain.Task, string, error)
	// CancelTaskByRef finds a pending task by textual reference and cancels it.
	// Returns the resolved task and the reply text.
	CancelTaskByRef(ctx context.Context, ref string) (domain.Task, string, error)
}

// NaturalHandler processes a free-text message through the natural language
// interpreter and returns a reply. It is the V1 seam for lenguaje natural:
// when no slash command matches, the message is passed here.
// If nil, the adapter falls back to the existing "unknown command" reply.
type NaturalHandler func(ctx context.Context, text string) (string, error)

// Handle maps an incoming message text to an application action and returns the
// reply to send back to the originating chat.
//
// Processing order:
//  1. Slash commands (/nueva, /recordar, /listar, /completar, /cancelar) are handled directly.
//  2. If a NaturalHandler is configured, non-slash messages are passed to it
//     for natural language interpretation.
//  3. If no NaturalHandler is configured, unknown messages produce a help reply.
//
// Internal task/trigger IDs are deliberately not exposed to the user in replies.
func Handle(ctx context.Context, svc CommandService, text string, natural NaturalHandler) (string, error) {
	trimmed := strings.TrimSpace(text)

	// 1. Slash commands: direct handling, no natural language fallback.
	switch {
	case strings.HasPrefix(trimmed, cmdNueva):
		title := strings.TrimSpace(strings.TrimPrefix(trimmed, cmdNueva))
		if title == "" {
			return "Uso: /nueva <título>", nil
		}
		if _, err := svc.CreateTask(ctx, title); err != nil {
			return "No pude crear la tarea: " + err.Error(), err
		}
		return "Tarea creada ✓", nil

	case strings.HasPrefix(trimmed, cmdRecordar):
		title, d, err := parseReminder(trimmed)
		if err != nil {
			return "Uso: /recordar <título> in <duración> (p. ej. 10s, 5m, 1h)", nil
		}
		task, err := svc.CreateReminder(ctx, title, d)
		if err != nil {
			return "No pude crear el recordatorio: " + err.Error(), err
		}
		return fmt.Sprintf("Recordatorio creado ✓ para «%s» en %s", task.Title, d), nil

	case strings.HasPrefix(trimmed, cmdListar):
		tasks, err := svc.ListPendingTasks(ctx)
		if err != nil {
			return "No pude obtener las tareas.", err
		}
		return formatTaskList(tasks), nil

	case strings.HasPrefix(trimmed, cmdCompletar):
		ref := strings.TrimSpace(strings.TrimPrefix(trimmed, cmdCompletar))
		if ref == "" {
			return "Uso: /completar <referencia de la tarea>", nil
		}
		_, reply, err := svc.CompleteTaskByRef(ctx, ref)
		if err != nil {
			return reply, err
		}
		return reply, nil

	case strings.HasPrefix(trimmed, cmdCancelar):
		ref := strings.TrimSpace(strings.TrimPrefix(trimmed, cmdCancelar))
		if ref == "" {
			return "Uso: /cancelar <referencia de la tarea>", nil
		}
		_, reply, err := svc.CancelTaskByRef(ctx, ref)
		if err != nil {
			return reply, err
		}
		return reply, nil
	}

	// 2. Natural language: delegate to the interpreter if configured.
	if natural != nil {
		return natural(ctx, trimmed)
	}

	// 3. Fallback: no natural handler configured, show legacy help.
	return "Comando no reconocido. Usa /nueva <título>, /recordar <título> in <duración>, /listar, /completar <ref> o /cancelar <ref>", nil
}

// formatTaskList formats a list of tasks for the user.
func formatTaskList(tasks []domain.Task) string {
	// Filter to pending only.
	var pending []domain.Task
	for _, t := range tasks {
		if t.Status == domain.TaskStatusPending {
			pending = append(pending, t)
		}
	}

	if len(pending) == 0 {
		return "No tenés tareas pendientes."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Tenes %d tarea(s) pendiente(s):\n", len(pending)))
	for i, t := range pending {
		sb.WriteString(fmt.Sprintf("%d) %s\n", i+1, t.Title))
	}
	return sb.String()
}

// parseReminder parses "<title> in <duration>" from a /recordar message. It
// returns the title and a positive duration, or a sentinel parse error.
func parseReminder(text string) (string, time.Duration, error) {
	rest := strings.TrimPrefix(strings.TrimSpace(text), cmdRecordar)

	idx := strings.LastIndex(rest, " in ")
	if idx < 0 {
		return "", 0, errReminderSyntax
	}
	title := strings.TrimSpace(rest[:idx])
	if title == "" {
		return "", 0, errReminderEmpty
	}

	durPart := strings.TrimSpace(rest[idx+len(" in "):])
	d, err := time.ParseDuration(durPart)
	if err != nil || d <= 0 {
		return "", 0, errReminderDuration
	}
	return title, d, nil
}
