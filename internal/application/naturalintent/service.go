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
//
//	text → interpret → validate → resolve time → execute → reply.
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
// This is the main entry point from the Telegram adapter. It is a thin wrapper
// over HandleMessageStructured that discards the recognition signal, preserving
// the exact historical behavior for existing callers and tests.
func (s *Service) HandleMessage(ctx context.Context, text string) (string, error) {
	reply, _, err := s.HandleMessageStructured(ctx, text)
	return reply, err
}

// HandleMessageStructured processes a natural language message and returns the
// reply plus whether the message was recognized as an actionable operation that
// was executed (recognized=true). Ambiguous and unrecognized messages report
// false: nothing was executed, so a caller with its own conversational reply (the
// AgentFlow inbound route) keeps it instead of replacing it with a clarification
// or help text. The interpretation and execution logic is unchanged; this method
// only exposes the signal the AgentFlowHandler needs for its fallback decision.
func (s *Service) HandleMessageStructured(ctx context.Context, text string) (reply string, recognized bool, err error) {
	ictx := InterpretContext{
		Now:      s.now(),
		Timezone: s.timezone,
	}

	result, err := s.interpreter.Interpret(ctx, text, ictx)
	if err != nil {
		s.logger.Printf("natural: interpret failed: %v", err)
		return interpretErrorReply(err), false, err
	}

	switch {
	case result.Unrecognized:
		return "Hola, soy ALTER. Puedo crear tareas y recordatorios. " +
			"Por ejemplo: \"comprar SSD\" o \"recordar comprar SSD en 30 minutos\".", false, nil

	case result.Ambiguous != nil:
		return result.Ambiguous.ClarificationPrompt, false, nil

	case result.Recognized != nil:
		reply, err := s.executeRecognized(ctx, result.Recognized)
		return reply, true, err

	default:
		return "No pude procesar tu mensaje.", false, nil
	}
}

// interpretErrorReply maps a classified Agent failure to a distinct user-facing
// reply. The point is to never present an infrastructure/provider problem as an
// unrecognized input: each case tells the user what actually failed. The switch
// is exhaustive because domain.AgentErrorKindOf always returns one of the four
// kinds; an error without an attached classification is an internal failure, NOT
// an unrecognized input (that would recreate the confusion this fixes).
func interpretErrorReply(err error) string {
	switch domain.AgentErrorKindOf(err) {
	case domain.AgentErrorKindTimeout:
		return "El agente tardó demasiado en responder. Intenta de nuevo en unos segundos."
	case domain.AgentErrorKindEmptyResponse:
		return "El agente no devolvió una respuesta. Intenta de nuevo en unos segundos."
	case domain.AgentErrorKindProvider:
		return "El servicio del agente no está disponible ahora mismo. Intenta de nuevo en unos segundos."
	case domain.AgentErrorKindInternal:
		return "Ocurrió un error interno al procesar tu mensaje. Intenta de nuevo en unos segundos."
	}
	return "Ocurrió un error interno al procesar tu mensaje. Intenta de nuevo en unos segundos."
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

// createReminder creates a task plus a trigger: one-shot "at" for relative/
// absolute reminders, recurring for RecurrenceParams (B3 S4).
func (s *Service) createReminder(ctx context.Context, title string, spec *ReminderSpec) (string, error) {
	if spec == nil {
		return "No pude entender cuándo quieres el recordatorio.", nil
	}

	if spec.Recurrence != nil {
		return s.createRecurringReminder(ctx, title, spec.Recurrence)
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

// createRecurringReminder creates a task + a TriggerTypeRecurring trigger whose
// Value is the canonical RecurrenceSpec JSON. Go derives and validates the whole
// spec; if the recurrence cannot be represented, nothing is persisted and the
// reply asks for clarification (same visible behavior as an AmbiguousIntent, so
// Pi never decides execution).
func (s *Service) createRecurringReminder(ctx context.Context, title string, rec *RecurrenceParams) (string, error) {
	value, err := BuildRecurrenceJSON(*rec, InterpretContext{
		Now:      s.now(),
		Timezone: s.timezone,
	})
	if err != nil {
		return fmt.Sprintf("No pude entender la recurrencia: %v. ¿Me la describes de nuevo?", err), nil
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
		Type:    domain.TriggerTypeRecurring,
		Value:   value,
		Enabled: true,
	})
	if err != nil {
		return "Tarea creada pero no pude programar el recordatorio recurrente: " + err.Error(), err
	}

	return fmt.Sprintf("Recordatorio creado: %s — %s.",
		title, describeCadence(*rec)), nil
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

// --- Recurring cadence (B3 S4) ----------------------------------------------

// weekdayNames and monthNames are the Spanish day/month names used to describe a
// cadence to the user. Go owns the reply (Pi never produces user-facing text).
var weekdayNames = []string{"lunes", "martes", "miércoles", "jueves", "viernes", "sábado", "domingo"}
var monthNames = []string{"", "enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}

// describeCadence renders the recurrence as a human cadence, e.g.
// "todos los días a las 21:00" or "cada lunes y jueves a las 09:00".
func describeCadence(rec RecurrenceParams) string {
	interval := 1
	if rec.Interval != nil {
		interval = *rec.Interval
	}

	hh, mm, err := parseHHMM(rec.Time)
	if err != nil {
		// Unreachable in practice: BuildRecurrenceJSON validated the time first.
		hh, mm = 0, 0
	}
	at := fmt.Sprintf("a las %02d:%02d", hh, mm)

	switch rec.Freq {
	case domain.RecurrenceFreqDaily:
		if interval == 1 {
			return "todos los días " + at
		}
		return fmt.Sprintf("cada %d días %s", interval, at)

	case domain.RecurrenceFreqWeekly:
		days := joinWeekdays(rec.Weekdays)
		if interval == 1 {
			return fmt.Sprintf("cada %s %s", days, at)
		}
		return fmt.Sprintf("cada %d semanas, %s %s", interval, days, at)

	case domain.RecurrenceFreqMonthly:
		day := 1
		if rec.DayOfMonth != nil {
			day = *rec.DayOfMonth
		}
		if interval == 1 {
			return fmt.Sprintf("el día %d de cada mes %s", day, at)
		}
		return fmt.Sprintf("cada %d meses, el día %d %s", interval, day, at)

	case domain.RecurrenceFreqYearly:
		month, day := 1, 1
		if rec.AnchorMonth != nil {
			month = *rec.AnchorMonth
		}
		if rec.AnchorDay != nil {
			day = *rec.AnchorDay
		}
		monthName := ""
		if month >= 1 && month <= 12 {
			monthName = monthNames[month]
		}
		if interval == 1 {
			return fmt.Sprintf("cada año, el %d de %s %s", day, monthName, at)
		}
		return fmt.Sprintf("cada %d años, el %d de %s %s", interval, day, monthName, at)
	}
	return at
}

// joinWeekdays joins NL weekday numbers into "lunes y jueves" / "lunes, miércoles y viernes".
func joinWeekdays(weekdays []int) string {
	names := make([]string, 0, len(weekdays))
	for _, wd := range weekdays {
		if wd >= 1 && wd <= 7 {
			names = append(names, weekdayNames[wd-1])
		}
	}
	switch len(names) {
	case 0:
		return "?"
	case 1:
		return names[0]
	case 2:
		return names[0] + " y " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " y " + names[len(names)-1]
	}
}
