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
}

// Handle maps an incoming message text to an application action and returns the
// reply to send back to the originating chat. Unknown or malformed commands
// produce a help reply rather than an error, so the caller always has a reply.
//
// Internal task/trigger IDs are deliberately not exposed to the user in replies.
func Handle(ctx context.Context, svc CommandService, text string) (string, error) {
	trimmed := strings.TrimSpace(text)

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

	default:
		return "Comando no reconocido. Usa /nueva <título> o /recordar <título> in <duración>", nil
	}
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
