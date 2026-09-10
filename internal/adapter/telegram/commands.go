package telegram

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/verdu/alter/internal/domain"
)

const (
	cmdNueva     = "/nueva"
	cmdRecordar  = "/recordar"
	cmdListar    = "/listar"
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
//
// Presentation ownership: every reply returned by Handle is either produced by
// format.go (HTML-safe) or, for the natural language path, the plain-text reply
// of the application service escaped by the adapter before it reaches the
// HTML-parsed Telegram send. The application layers (naturalintent, command
// service) never know about Telegram formatting.
func Handle(ctx context.Context, svc CommandService, text string, natural NaturalHandler) (string, error) {
	trimmed := strings.TrimSpace(text)

	// 1. Slash commands: direct handling, no natural language fallback.
	switch {
	case strings.HasPrefix(trimmed, cmdNueva):
		title := strings.TrimSpace(strings.TrimPrefix(trimmed, cmdNueva))
		if title == "" {
			return MsgUsageNueva(), nil
		}
		if _, err := svc.CreateTask(ctx, title); err != nil {
			return MsgErrorCreating("la tarea", err.Error()), err
		}
		return MsgTaskCreated(title), nil

	case strings.HasPrefix(trimmed, cmdRecordar):
		title, d, err := parseReminder(trimmed)
		if err != nil {
			return MsgUsageRecordar(), nil
		}
		task, err := svc.CreateReminder(ctx, title, d)
		if err != nil {
			return MsgErrorCreating("el recordatorio", err.Error()), err
		}
		return MsgReminderCreated(task.Title, d.String()), nil

	case strings.HasPrefix(trimmed, cmdListar):
		tasks, err := svc.ListPendingTasks(ctx)
		if err != nil {
			return MsgErrorAction("obtener las tareas"), err
		}
		titles := pendingTitles(tasks)
		if len(titles) == 0 {
			return MsgTaskListEmpty(), nil
		}
		return MsgTaskList(titles), nil

	case strings.HasPrefix(trimmed, cmdCompletar):
		ref := strings.TrimSpace(strings.TrimPrefix(trimmed, cmdCompletar))
		if ref == "" {
			return MsgUsageCompletar(), nil
		}
		task, reply, err := svc.CompleteTaskByRef(ctx, ref)
		if err != nil {
			// The service reply is plain text (the adapter owns presentation);
			// escape it so the HTML-parsed send cannot be broken.
			return escapeHTML(reply), err
		}
		return MsgTaskCompleted(task.Title), nil

	case strings.HasPrefix(trimmed, cmdCancelar):
		ref := strings.TrimSpace(strings.TrimPrefix(trimmed, cmdCancelar))
		if ref == "" {
			return MsgUsageCancelar(), nil
		}
		task, reply, err := svc.CancelTaskByRef(ctx, ref)
		if err != nil {
			return escapeHTML(reply), err
		}
		return MsgTaskCancelled(task.Title), nil
	}

	// 2. Natural language: delegate to the interpreter if configured. The reply
	// is Go-generated plain text (it may embed Pi-authored clarification text);
	// the adapter escapes it before it reaches the HTML-parsed Telegram send.
	if natural != nil {
		reply, err := natural(ctx, trimmed)
		if err != nil {
			return escapeHTML(reply), err
		}
		return escapeHTML(reply), nil
	}

	// 3. Fallback: no natural handler configured, show help.
	return MsgUnrecognized(), nil
}

// pendingTitles returns the titles of pending tasks only, in list order.
func pendingTitles(tasks []domain.Task) []string {
	var titles []string
	for _, t := range tasks {
		if t.Status == domain.TaskStatusPending {
			titles = append(titles, t.Title)
		}
	}
	return titles
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
