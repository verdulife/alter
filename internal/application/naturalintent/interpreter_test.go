package naturalintent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// fakePiRunner is a test double that returns pre-configured responses.
type fakePiRunner struct {
	response   string
	err        error
	lastPrompt string
}

func (f *fakePiRunner) RunPi(_ context.Context, prompt string) (string, error) {
	f.lastPrompt = prompt
	if f.err != nil {
		return "", f.err
	}
	return f.response, nil
}

func TestInterpretCreateTask(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_task","title":"comprar SSD"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "comprar SSD", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Action != ActionCreateTask {
		t.Errorf("action = %q, want %q", result.Recognized.Action, ActionCreateTask)
	}
	if result.Recognized.Title != "comprar SSD" {
		t.Errorf("title = %q, want %q", result.Recognized.Title, "comprar SSD")
	}
	if result.Recognized.Reminder != nil {
		t.Error("expected no reminder for create_task")
	}
}

func TestInterpretCreateReminderRelative(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"comprar SSD","reminder":{"relative":"30m"}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "recuérdame comprar SSD en 30 minutos", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Action != ActionCreateReminder {
		t.Errorf("action = %q, want %q", result.Recognized.Action, ActionCreateReminder)
	}
	if result.Recognized.Reminder == nil {
		t.Fatal("expected reminder spec")
	}
	if result.Recognized.Reminder.Relative != "30m" {
		t.Errorf("relative = %q, want %q", result.Recognized.Reminder.Relative, "30m")
	}
}

func TestInterpretCreateReminderAbsolute(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"comprar SSD","reminder":{"absolute_time":"20:00","absolute_date":"today"}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "comprar SSD hoy a las 20h", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Action != ActionCreateReminder {
		t.Errorf("action = %q, want %q", result.Recognized.Action, ActionCreateReminder)
	}
	if result.Recognized.Reminder == nil {
		t.Fatal("expected reminder spec")
	}
	if result.Recognized.Reminder.AbsoluteTime != "20:00" {
		t.Errorf("absolute_time = %q, want %q", result.Recognized.Reminder.AbsoluteTime, "20:00")
	}
	if result.Recognized.Reminder.AbsoluteDate != "today" {
		t.Errorf("absolute_date = %q, want %q", result.Recognized.Reminder.AbsoluteDate, "today")
	}
}

// --- Recurrence interpretation (B3 S4) --------------------------------------

func TestInterpretCreateReminderRecurringDaily(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"sacar la basura","reminder":{"recurrence":{"freq":"daily","time":"21:00"}}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "sacar la basura todos los días a las 21", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil || result.Recognized.Reminder == nil || result.Recognized.Reminder.Recurrence == nil {
		t.Fatal("expected recognized intent with a recurrence")
	}
	rec := result.Recognized.Reminder.Recurrence
	if rec.Freq != domain.RecurrenceFreqDaily {
		t.Errorf("freq = %q, want daily", rec.Freq)
	}
	if rec.Time != "21:00" {
		t.Errorf("time = %q, want 21:00", rec.Time)
	}
	if rec.Interval != nil {
		t.Errorf("interval = %v, want nil (defaults to 1)", *rec.Interval)
	}
}

func TestInterpretCreateReminderRecurringWeekly(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"llamar a mamá","reminder":{"recurrence":{"freq":"weekly","weekdays":[1,4],"time":"09:00"}}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "llamar a mamá cada lunes y jueves a las 9", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	rec := result.Recognized.Reminder.Recurrence
	if rec.Freq != domain.RecurrenceFreqWeekly {
		t.Errorf("freq = %q, want weekly", rec.Freq)
	}
	if len(rec.Weekdays) != 2 || rec.Weekdays[0] != 1 || rec.Weekdays[1] != 4 {
		t.Errorf("weekdays = %v, want [1 4]", rec.Weekdays)
	}
}

func TestInterpretCreateReminderRecurringMonthly(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"pagar alquiler","reminder":{"recurrence":{"freq":"monthly","day_of_month":1,"time":"08:00"}}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "pagar alquiler el día 1 de cada mes a las 8", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	rec := result.Recognized.Reminder.Recurrence
	if rec.Freq != domain.RecurrenceFreqMonthly {
		t.Errorf("freq = %q, want monthly", rec.Freq)
	}
	if rec.DayOfMonth == nil || *rec.DayOfMonth != 1 {
		t.Errorf("day_of_month = %v, want 1", rec.DayOfMonth)
	}
}

func TestInterpretCreateReminderRecurringYearly(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"aniversario","reminder":{"recurrence":{"freq":"yearly","anchor_month":9,"anchor_day":10,"time":"09:00"}}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "aniversario cada año el 10 de septiembre a las 9", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	rec := result.Recognized.Reminder.Recurrence
	if rec.Freq != domain.RecurrenceFreqYearly {
		t.Errorf("freq = %q, want yearly", rec.Freq)
	}
	if rec.AnchorMonth == nil || *rec.AnchorMonth != 9 || rec.AnchorDay == nil || *rec.AnchorDay != 10 {
		t.Errorf("anchor = (%v, %v), want (9, 10)", rec.AnchorMonth, rec.AnchorDay)
	}
}

func TestInterpretCreateReminderRecurringInterval(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"revisar el coche","reminder":{"recurrence":{"freq":"weekly","interval":2,"weekdays":[2],"time":"10:00"}}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "revisar el coche cada 2 semanas los martes a las 10", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	rec := result.Recognized.Reminder.Recurrence
	if rec.Interval == nil || *rec.Interval != 2 {
		t.Errorf("interval = %v, want 2", rec.Interval)
	}
}

func TestInterpretRecurrenceRequiresTime(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"sacar la basura","reminder":{"recurrence":{"freq":"daily"}}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "todos los días", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent (missing time)")
	}
}

func TestInterpretRecurrenceContradiction(t *testing.T) {
	// Pi must never emit relative/absolute together with recurrence; if it does,
	// Go treats it as ambiguous rather than guessing.
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"sacar la basura","reminder":{"relative":"30m","recurrence":{"freq":"daily","time":"21:00"}}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "sacar la basura todos los días en 30 minutos", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent (contradictory reminder)")
	}
}

func TestInterpretRecurringMissingFields(t *testing.T) {
	cases := []struct {
		name     string
		response string
	}{
		{"weekly without weekdays", `{"action":"create_reminder","title":"x","reminder":{"recurrence":{"freq":"weekly","time":"09:00"}}}`},
		{"monthly without day", `{"action":"create_reminder","title":"x","reminder":{"recurrence":{"freq":"monthly","time":"09:00"}}}`},
		{"yearly without month/day", `{"action":"create_reminder","title":"x","reminder":{"recurrence":{"freq":"yearly","time":"09:00"}}}`},
		{"unknown freq", `{"action":"create_reminder","title":"x","reminder":{"recurrence":{"freq":"fortnightly","time":"09:00"}}}`},
		{"no freq", `{"action":"create_reminder","title":"x","reminder":{"recurrence":{"time":"09:00"}}}`},
		{"interval zero", `{"action":"create_reminder","title":"x","reminder":{"recurrence":{"freq":"daily","interval":0,"time":"09:00"}}}`},
		{"weekdays out of range", `{"action":"create_reminder","title":"x","reminder":{"recurrence":{"freq":"weekly","weekdays":[8],"time":"09:00"}}}`},
		{"monthly day out of range", `{"action":"create_reminder","title":"x","reminder":{"recurrence":{"freq":"monthly","day_of_month":32,"time":"09:00"}}}`},
		{"anchor month without day", `{"action":"create_reminder","title":"x","reminder":{"recurrence":{"freq":"yearly","anchor_month":9,"time":"09:00"}}}`},
		{"anchor year only", `{"action":"create_reminder","title":"x","reminder":{"recurrence":{"freq":"yearly","anchor_year":2027,"time":"09:00"}}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runner := &fakePiRunner{response: c.response}
			interpreter := NewPiNaturalInterpreter(runner)
			ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

			result, err := interpreter.Interpret(context.Background(), "prueba", ctx)
			if err != nil {
				t.Fatalf("Interpret() error = %v", err)
			}
			if result.Ambiguous == nil {
				t.Errorf("%s: expected Ambiguous intent", c.name)
			}
		})
	}
}

func TestInterpretCreateTaskStillNoRecurrence(t *testing.T) {
	// A plain create_task (no reminder) must keep Recognized.Reminder nil.
	runner := &fakePiRunner{
		response: `{"action":"create_task","title":"comprar SSD"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "comprar SSD", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil || result.Recognized.Reminder != nil {
		t.Error("plain create_task must not carry a reminder/recurrence")
	}
}

func TestInterpretUnrecognized(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"unrecognized"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "hola", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if !result.Unrecognized {
		t.Error("expected Unrecognized to be true")
	}
	if result.Recognized != nil {
		t.Error("expected no Recognized intent")
	}
	if result.Ambiguous != nil {
		t.Error("expected no Ambiguous intent")
	}
}

func TestInterpretAmbiguousMissingTitle(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_task","missing_fields":["title"],"clarification_prompt":"¿Qué tarea quieres crear?"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "poné una tarea", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent")
	}
	if result.Ambiguous.Action != ActionCreateTask {
		t.Errorf("action = %q, want %q", result.Ambiguous.Action, ActionCreateTask)
	}
	if len(result.Ambiguous.MissingFields) != 1 || result.Ambiguous.MissingFields[0] != "title" {
		t.Errorf("missing_fields = %v, want [title]", result.Ambiguous.MissingFields)
	}
	if result.Ambiguous.ClarificationPrompt != "¿Qué tarea quieres crear?" {
		t.Errorf("clarification_prompt = %q", result.Ambiguous.ClarificationPrompt)
	}
}

func TestInterpretAmbiguousMissingTime(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"algo del SSD","missing_fields":["time"],"clarification_prompt":"¿Cuándo quieres el recordatorio?"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "recordame algo del SSD", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent")
	}
	if result.Ambiguous.Action != ActionCreateReminder {
		t.Errorf("action = %q, want %q", result.Ambiguous.Action, ActionCreateReminder)
	}
	if len(result.Ambiguous.MissingFields) != 1 || result.Ambiguous.MissingFields[0] != "time" {
		t.Errorf("missing_fields = %v, want [time]", result.Ambiguous.MissingFields)
	}
}

func TestInterpretInvalidJSON(t *testing.T) {
	runner := &fakePiRunner{
		response: `this is not json`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	_, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error = %q, want 'invalid JSON'", err.Error())
	}
	// An unclassifiable protocol defect is an internal failure, never an
	// unrecognized input.
	if got := domain.AgentErrorKindOf(err); got != domain.AgentErrorKindInternal {
		t.Errorf("invalid JSON kind = %v, want internal", got)
	}
}

// TestInterpretEmptyResponse checks that an empty agent response is classified
// as an agent failure (EmptyResponse), NOT as an unrecognized input: the user's
// message was never classified, so it must not trigger the "unrecognized"
// welcome path.
func TestInterpretEmptyResponse(t *testing.T) {
	runner := &fakePiRunner{
		response: "",
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	_, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err == nil {
		t.Fatal("expected an error for an empty agent response")
	}
	if got := domain.AgentErrorKindOf(err); got != domain.AgentErrorKindEmptyResponse {
		t.Errorf("kind = %v, want %v", got, domain.AgentErrorKindEmptyResponse)
	}
}

// TestInterpretPreservesRunnerErrorKind checks that a classified runner error
// keeps its classification through the interpreter's wrapping: the wrapper adds
// context but never reclassifies, so the reply layer can distinguish timeout,
// empty response, provider failure and internal failure.
func TestInterpretPreservesRunnerErrorKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind domain.AgentErrorKind
	}{
		{"timeout", domain.AgentErrorKindTimeout},
		{"empty response", domain.AgentErrorKindEmptyResponse},
		{"provider", domain.AgentErrorKindProvider},
		{"internal", domain.AgentErrorKindInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakePiRunner{
				err: &domain.AgentError{Kind: tc.kind, Err: errors.New("boom")},
			}
			interpreter := NewPiNaturalInterpreter(runner)
			ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

			_, err := interpreter.Interpret(context.Background(), "test", ctx)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := domain.AgentErrorKindOf(err); got != tc.kind {
				t.Errorf("kind = %v, want %v", got, tc.kind)
			}
		})
	}
}

func TestInterpretPiError(t *testing.T) {
	runner := &fakePiRunner{
		err: context.DeadlineExceeded,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	_, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err == nil {
		t.Error("expected error when Pi fails")
	}
	if !strings.Contains(err.Error(), "pi execution failed") {
		t.Errorf("error = %q, want 'pi execution failed'", err.Error())
	}
}

func TestInterpretMarkdownFencedJSON(t *testing.T) {
	runner := &fakePiRunner{
		response: "```json\n{\"action\":\"create_task\",\"title\":\"test\"}\n```",
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Title != "test" {
		t.Errorf("title = %q, want %q", result.Recognized.Title, "test")
	}
}

func TestInterpretUnknownAction(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"delete_all_tasks"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	_, err := interpreter.Interpret(context.Background(), "borra todo", ctx)
	if err == nil {
		t.Error("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error = %q, want 'unknown action'", err.Error())
	}
	// An unknown action is a contract defect of the agent's output: internal,
	// never presented as an unrecognized input.
	if got := domain.AgentErrorKindOf(err); got != domain.AgentErrorKindInternal {
		t.Errorf("unknown action kind = %v, want internal", got)
	}
}

func TestInterpretAmbiguousBothRelativeAndAbsolute(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"test","reminder":{"relative":"30m","absolute_time":"20:00"}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	// Should be ambiguous because the reminder spec is ambiguous
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent for conflicting reminder spec")
	}
}

func TestInterpretReminderNoTimeFields(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"test","reminder":{}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	// Should be ambiguous because reminder has no time
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent for reminder with no time")
	}
}

// --- list_tasks ----------------------------------------------------------------

func TestInterpretListTasks(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"list_tasks"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "qué tareas tengo", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Action != ActionListTasks {
		t.Errorf("action = %q, want %q", result.Recognized.Action, ActionListTasks)
	}
}

// --- complete_task ---------------------------------------------------------------

func TestInterpretCompleteTask(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"complete_task","task_ref":"compra SSD"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "compra SSD lista", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Action != ActionCompleteTask {
		t.Errorf("action = %q, want %q", result.Recognized.Action, ActionCompleteTask)
	}
	if result.Recognized.TaskRef != "compra SSD" {
		t.Errorf("task_ref = %q, want %q", result.Recognized.TaskRef, "compra SSD")
	}
}

func TestInterpretCompleteTaskMissingRef(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"complete_task","missing_fields":["task_ref"],"clarification_prompt":"¿Qué tarea querés completar?"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "completá algo", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent")
	}
	if result.Ambiguous.Action != ActionCompleteTask {
		t.Errorf("action = %q, want %q", result.Ambiguous.Action, ActionCompleteTask)
	}
	if len(result.Ambiguous.MissingFields) != 1 || result.Ambiguous.MissingFields[0] != "task_ref" {
		t.Errorf("missing_fields = %v, want [task_ref]", result.Ambiguous.MissingFields)
	}
}

// --- cancel_task -----------------------------------------------------------------

func TestInterpretCancelTask(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"cancel_task","task_ref":"fontanero"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "cancelá la del fontanero", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Action != ActionCancelTask {
		t.Errorf("action = %q, want %q", result.Recognized.Action, ActionCancelTask)
	}
	if result.Recognized.TaskRef != "fontanero" {
		t.Errorf("task_ref = %q, want %q", result.Recognized.TaskRef, "fontanero")
	}
}

func TestInterpretCancelTaskMissingRef(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"cancel_task","missing_fields":["task_ref"]}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "cancelá", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent")
	}
	if result.Ambiguous.Action != ActionCancelTask {
		t.Errorf("action = %q, want %q", result.Ambiguous.Action, ActionCancelTask)
	}
}

func TestInterpretPassesUserTextToPi(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"unrecognized"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	userText := "comprar leche"
	_, _ = interpreter.Interpret(context.Background(), userText, ctx)

	if !strings.Contains(runner.lastPrompt, userText) {
		t.Errorf("prompt does not contain user text: %q", runner.lastPrompt)
	}
}

func TestInterpretPromptContainsSchema(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"unrecognized"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	_, _ = interpreter.Interpret(context.Background(), "test", ctx)

	// The prompt should contain the schema definition
	if !strings.Contains(runner.lastPrompt, "create_task") {
		t.Error("prompt should contain 'create_task' from schema")
	}
	if !strings.Contains(runner.lastPrompt, "create_reminder") {
		t.Error("prompt should contain 'create_reminder' from schema")
	}
}

func TestParseIntentResponseVarious(t *testing.T) {
	tests := []struct {
		name       string
		json       string
		wantState  string // "recognized", "ambiguous", "unrecognized", "error"
		wantAction Action
	}{
		{
			name:       "valid create_task",
			json:       `{"action":"create_task","title":"test"}`,
			wantState:  "recognized",
			wantAction: ActionCreateTask,
		},
		{
			name:       "valid create_reminder with relative",
			json:       `{"action":"create_reminder","title":"test","reminder":{"relative":"1h"}}`,
			wantState:  "recognized",
			wantAction: ActionCreateReminder,
		},
		{
			name:      "unrecognized",
			json:      `{"action":"unrecognized"}`,
			wantState: "unrecognized",
		},
		{
			name:       "missing title",
			json:       `{"action":"create_task","missing_fields":["title"]}`,
			wantState:  "ambiguous",
			wantAction: ActionCreateTask,
		},
		{
			name:      "empty action",
			json:      `{"action":"","title":"test"}`,
			wantState: "unrecognized",
		},
		{
			name:      "missing action field",
			json:      `{"title":"test"}`,
			wantState: "unrecognized",
		},
		{
			name:      "invalid json",
			json:      `{bad json`,
			wantState: "error",
		},
		{
			name:      "null action",
			json:      `{"action":null}`,
			wantState: "unrecognized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseIntentResponse(tt.json)
			if tt.wantState == "error" {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIntentResponse() error = %v", err)
			}

			switch tt.wantState {
			case "recognized":
				if result.Recognized == nil {
					t.Fatal("expected Recognized")
				}
				if result.Recognized.Action != tt.wantAction {
					t.Errorf("action = %q, want %q", result.Recognized.Action, tt.wantAction)
				}
			case "ambiguous":
				if result.Ambiguous == nil {
					t.Fatal("expected Ambiguous")
				}
			case "unrecognized":
				if !result.Unrecognized {
					t.Error("expected Unrecognized")
				}
			}
		})
	}
}

func TestNormalizeReminderSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    *reminderJSON
		wantRel string
		wantAbs string
		wantErr bool
	}{
		{
			name:    "relative only",
			spec:    &reminderJSON{Relative: strPtr("30m")},
			wantRel: "30m",
		},
		{
			name:    "absolute time only",
			spec:    &reminderJSON{AbsoluteTime: strPtr("20:00")},
			wantAbs: "20:00",
		},
		{
			name:    "absolute time and date",
			spec:    &reminderJSON{AbsoluteTime: strPtr("09:00"), AbsoluteDate: strPtr("tomorrow")},
			wantAbs: "09:00",
		},
		{
			name:    "both relative and absolute",
			spec:    &reminderJSON{Relative: strPtr("30m"), AbsoluteTime: strPtr("20:00")},
			wantErr: true,
		},
		{
			name:    "no time fields",
			spec:    &reminderJSON{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeReminderSpec(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("normalizeReminderSpec() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Relative != tt.wantRel {
					t.Errorf("Relative = %q, want %q", got.Relative, tt.wantRel)
				}
				if got.AbsoluteTime != tt.wantAbs {
					t.Errorf("AbsoluteTime = %q, want %q", got.AbsoluteTime, tt.wantAbs)
				}
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// Verify piIntentJSON can be marshaled/unmarshaled for round-trip tests.
func TestPiIntentJSONRoundTrip(t *testing.T) {
	original := piIntentJSON{
		Action: strPtr("create_reminder"),
		Title:  strPtr("comprar SSD"),
		Reminder: &reminderJSON{
			Relative:     strPtr("30m"),
			AbsoluteTime: nil,
			AbsoluteDate: nil,
		},
		MissingFields:       nil,
		ClarificationPrompt: nil,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded piIntentJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if *decoded.Action != *original.Action {
		t.Errorf("action = %q, want %q", *decoded.Action, *original.Action)
	}
	if *decoded.Title != *original.Title {
		t.Errorf("title = %q, want %q", *decoded.Title, *original.Title)
	}
	if decoded.Reminder == nil || *decoded.Reminder.Relative != "30m" {
		t.Errorf("reminder.relative = %v, want 30m", decoded.Reminder)
	}
}
