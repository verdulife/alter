package naturalintent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/verdu/alter/internal/domain"
	appservice "github.com/verdu/alter/internal/service"
)

// Service is the application-layer service that bridges natural language
// interpretation to domain operations. It owns the full flow:
//   text → interpret → validate → resolve time → execute → reply.
//
// It depends on domain.TaskService-like operations (via the interfaces below)
// and never exposes internal state to Pi or the interpreter.
type Service struct {
	interpreter IntentInterpreter
	tasks       TaskCreator
	triggers    TriggerCreator
	manager     TaskManager
	now         func() time.Time
	timezone    *time.Location
	logger      *log.Logger
}

// TaskCreator abstracts task creation for testability.
type TaskCreator interface {
	Create(ctx context.Context, params appservice.CreateTaskParams) (domain.Task, error)
}

// TriggerCreator abstracts trigger creation for testability.
type TriggerCreator interface {
	Create(ctx context.Context, params appservice.CreateTriggerParams) (domain.Trigger, error)
}

// TaskManager abstracts task listing, completion and cancellation for testability.
// It is satisfied by service.TaskService.
type TaskManager interface {
	List(ctx context.Context) ([]domain.Task, error)
	Complete(ctx context.Context, id string) (domain.Task, error)
	Cancel(ctx context.Context, id string) (domain.Task, error)
}

// Option configures a Service.
type Option func(*Service)

// WithLogger sets the logger for non-fatal diagnostics.
func WithLogger(l *log.Logger) Option {
	return func(s *Service) { s.logger = l }
}

// WithNow overrides the clock (for deterministic tests).
func WithNow(f func() time.Time) Option {
	return func(s *Service) { s.now = f }
}

// NewService builds a natural language service.
func NewService(
	interpreter IntentInterpreter,
	tasks TaskCreator,
	triggers TriggerCreator,
	manager TaskManager,
	timezone *time.Location,
	opts ...Option,
) *Service {
	s := &Service{
		interpreter: interpreter,
		tasks:       tasks,
		triggers:    triggers,
		manager:     manager,
		now:         time.Now,
		timezone:    timezone,
		logger:      log.New(io.Discard, "", 0),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// HandleMessage processes a natural language message and returns a reply.
// This is the main entry point from the Telegram adapter.
func (s *Service) HandleMessage(ctx context.Context, text string) (string, error) {
	ictx := InterpretContext{
		Now:      s.now(),
		Timezone: s.timezone,
	}

	result, err := s.interpreter.Interpret(ctx, text, ictx)
	if err != nil {
		s.logger.Printf("natural: interpret failed: %v", err)
		return "No pude entender tu mensaje. Intenta con más detalle.", err
	}

	switch {
	case result.Unrecognized:
		return "Hola, soy ALTER. Puedo crear tareas y recordatorios. " +
			"Por ejemplo: \"comprar SSD\" o \"recordar comprar SSD en 30 minutos\".", nil

	case result.Ambiguous != nil:
		return result.Ambiguous.ClarificationPrompt, nil

	case result.Recognized != nil:
		return s.executeRecognized(ctx, result.Recognized)

	default:
		return "No pude procesar tu mensaje.", nil
	}
}

// executeRecognized executes a fully parsed intent.
func (s *Service) executeRecognized(ctx context.Context, intent *RecognizedIntent) (string, error) {
	switch intent.Action {
	case ActionCreateTask:
		return s.createTask(ctx, intent.Title)
	case ActionCreateReminder:
		return s.createReminder(ctx, intent.Title, intent.Reminder)
	case ActionListTasks:
		return s.listTasks(ctx)
	case ActionCompleteTask:
		return s.completeTask(ctx, intent.TaskRef)
	case ActionCancelTask:
		return s.cancelTask(ctx, intent.TaskRef)
	default:
		return fmt.Sprintf("Acción no soportada: %s", intent.Action), nil
	}
}

// createTask creates a plain task (no reminder trigger).
func (s *Service) createTask(ctx context.Context, title string) (string, error) {
	_, err := s.tasks.Create(ctx, appservice.CreateTaskParams{
		Title:  title,
		Source: "telegram:natural",
	})
	if err != nil {
		return "No pude crear la tarea: " + err.Error(), err
	}
	return fmt.Sprintf("Tarea creada ✓ \"%s\"", title), nil
}

// createReminder creates a task plus a one-shot "at" trigger.
func (s *Service) createReminder(ctx context.Context, title string, spec *ReminderSpec) (string, error) {
	if spec == nil {
		return "No pude entender cuándo quieres el recordatorio.", nil
	}

	at, err := ResolveTime(*spec, InterpretContext{
		Now:      s.now(),
		Timezone: s.timezone,
	})
	if err != nil {
		return fmt.Sprintf("No pude resolver el horario: %v", err), err
	}

	task, err := s.tasks.Create(ctx, appservice.CreateTaskParams{
		Title:  title,
		Source: "telegram:natural",
	})
	if err != nil {
		return "No pude crear la tarea: " + err.Error(), err
	}

	_, err = s.triggers.Create(ctx, appservice.CreateTriggerParams{
		TaskID:  task.ID,
		Type:    domain.TriggerTypeAt,
		Value:   at.UTC().Format(time.RFC3339),
		Enabled: true,
	})
	if err != nil {
		return "Tarea creada pero no pude programar el recordatorio: " + err.Error(), err
	}

	return fmt.Sprintf("Recordatorio creado ✓ \"%s\" para %s",
		title, at.In(s.timezone).Format("02/01 15:04")), nil
}

// listTasks returns all pending tasks formatted for the user.
func (s *Service) listTasks(ctx context.Context) (string, error) {
	tasks, err := s.manager.List(ctx)
	if err != nil {
		return "No pude obtener las tareas.", err
	}

	// Filter to pending only.
	var pending []domain.Task
	for _, t := range tasks {
		if t.Status == domain.TaskStatusPending {
			pending = append(pending, t)
		}
	}

	if len(pending) == 0 {
		return "No tenés tareas pendientes.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Tenes %d tarea(s) pendiente(s):\n", len(pending)))
	for i, t := range pending {
		sb.WriteString(fmt.Sprintf("%d) %s\n", i+1, t.Title))
	}
	return sb.String(), nil
}

// completeTask finds a pending task by reference and marks it as completed.
func (s *Service) completeTask(ctx context.Context, taskRef string) (string, error) {
	task, err := s.resolveTask(ctx, taskRef)
	if err != nil {
		return err.Error(), err
	}

	if _, err := s.manager.Complete(ctx, task.ID); err != nil {
		return "No pude completar la tarea: " + err.Error(), err
	}
	return fmt.Sprintf("Listo, completé «%s» ✓", task.Title), nil
}

// cancelTask finds a pending task by reference and cancels it.
func (s *Service) cancelTask(ctx context.Context, taskRef string) (string, error) {
	task, err := s.resolveTask(ctx, taskRef)
	if err != nil {
		return err.Error(), err
	}

	if _, err := s.manager.Cancel(ctx, task.ID); err != nil {
		return "No pude cancelar la tarea: " + err.Error(), err
	}
	return fmt.Sprintf("Cancelé «%s» ✓", task.Title), nil
}

// resolveTask finds a single pending task matching the given reference.
// Returns an error reply if 0 or 2+ tasks match.
func (s *Service) resolveTask(ctx context.Context, ref string) (domain.Task, error) {
	tasks, err := s.manager.List(ctx)
	if err != nil {
		return domain.Task{}, fmt.Errorf("No pude obtener las tareas: %w", err)
	}

	refLower := strings.ToLower(ref)
	var matches []domain.Task
	for _, t := range tasks {
		if t.Status == domain.TaskStatusPending && strings.Contains(strings.ToLower(t.Title), refLower) {
			matches = append(matches, t)
		}
	}

	switch len(matches) {
	case 0:
		return domain.Task{}, fmt.Errorf("No encontré ninguna tarea pendiente que coincida con «%s».", ref)
	case 1:
		return matches[0], nil
	default:
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Encontré varias tareas que coinciden con «%s»:\n", ref))
		for i, t := range matches {
			sb.WriteString(fmt.Sprintf("%d) %s\n", i+1, t.Title))
		}
		sb.WriteString("¿Cuál?")
		return domain.Task{}, errors.New(sb.String())
	}
}
