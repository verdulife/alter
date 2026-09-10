package naturalintent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
	appservice "github.com/verdu/alter/internal/service"
)

// fakeTaskCreator is a test double for TaskCreator.
type fakeTaskCreator struct {
	tasks   []domain.Task
	calls   []appservice.CreateTaskParams
	createErr error
}

func (f *fakeTaskCreator) Create(_ context.Context, params appservice.CreateTaskParams) (domain.Task, error) {
	f.calls = append(f.calls, params)
	if f.createErr != nil {
		return domain.Task{}, f.createErr
	}
	task := domain.Task{
		ID:    "task-" + strings.ReplaceAll(params.Title, " ", "-"),
		Title: params.Title,
	}
	f.tasks = append(f.tasks, task)
	return task, nil
}

// fakeTriggerCreator is a test double for TriggerCreator.
type fakeTriggerCreator struct {
	triggers  []domain.Trigger
	calls     []appservice.CreateTriggerParams
	createErr error
}

func (f *fakeTriggerCreator) Create(_ context.Context, params appservice.CreateTriggerParams) (domain.Trigger, error) {
	f.calls = append(f.calls, params)
	if f.createErr != nil {
		return domain.Trigger{}, f.createErr
	}
	trigger := domain.Trigger{
		ID:     "trigger-" + params.TaskID,
		TaskID: params.TaskID,
		Type:   params.Type,
		Value:  params.Value,
	}
	f.triggers = append(f.triggers, trigger)
	return trigger, nil
}

// fakeInterpreter is a test double for IntentInterpreter.
type fakeInterpreter struct {
	result IntentResult
	err    error
}

func (f *fakeInterpreter) Interpret(_ context.Context, _ string, _ InterpretContext) (IntentResult, error) {
	return f.result, f.err
}

func TestServiceCreateTask(t *testing.T) {
	interpreter := &fakeInterpreter{
		result: IntentResult{
			Recognized: &RecognizedIntent{
				Action: ActionCreateTask,
				Title:  "comprar SSD",
			},
		},
	}
	tasks := &fakeTaskCreator{}
	triggers := &fakeTriggerCreator{}
	tz := time.UTC

	svc := NewService(interpreter, tasks, triggers, tz)
	reply, err := svc.HandleMessage(context.Background(), "comprar SSD")
	if err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(tasks.calls) != 1 {
		t.Fatalf("expected 1 task creation, got %d", len(tasks.calls))
	}
	if tasks.calls[0].Title != "comprar SSD" {
		t.Errorf("task title = %q, want %q", tasks.calls[0].Title, "comprar SSD")
	}
	if tasks.calls[0].Source != "telegram:natural" {
		t.Errorf("task source = %q, want %q", tasks.calls[0].Source, "telegram:natural")
	}
	if len(triggers.calls) != 0 {
		t.Error("expected no trigger creation for plain task")
	}
	if !strings.Contains(reply, "Tarea creada") {
		t.Errorf("reply = %q, want confirmation", reply)
	}
}

func TestServiceCreateReminderRelative(t *testing.T) {
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	interpreter := &fakeInterpreter{
		result: IntentResult{
			Recognized: &RecognizedIntent{
				Action: ActionCreateReminder,
				Title:  "comprar SSD",
				Reminder: &ReminderSpec{
					Relative: "30m",
				},
			},
		},
	}
	tasks := &fakeTaskCreator{}
	triggers := &fakeTriggerCreator{}
	tz := time.UTC

	svc := NewService(interpreter, tasks, triggers, tz, WithNow(func() time.Time { return now }))
	reply, err := svc.HandleMessage(context.Background(), "comprar SSD en 30 minutos")
	if err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(tasks.calls) != 1 {
		t.Fatalf("expected 1 task creation, got %d", len(tasks.calls))
	}
	if len(triggers.calls) != 1 {
		t.Fatalf("expected 1 trigger creation, got %d", len(triggers.calls))
	}
	if triggers.calls[0].Type != domain.TriggerTypeAt {
		t.Errorf("trigger type = %q, want %q", triggers.calls[0].Type, domain.TriggerTypeAt)
	}
	wantTime := now.Add(30 * time.Minute).UTC().Format(time.RFC3339)
	if triggers.calls[0].Value != wantTime {
		t.Errorf("trigger value = %q, want %q", triggers.calls[0].Value, wantTime)
	}
	if !strings.Contains(reply, "Recordatorio creado") {
		t.Errorf("reply = %q, want confirmation", reply)
	}
}

func TestServiceCreateReminderAbsolute(t *testing.T) {
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	interpreter := &fakeInterpreter{
		result: IntentResult{
			Recognized: &RecognizedIntent{
				Action: ActionCreateReminder,
				Title:  "comprar SSD",
				Reminder: &ReminderSpec{
					AbsoluteTime: "20:00",
					AbsoluteDate: "today",
				},
			},
		},
	}
	tasks := &fakeTaskCreator{}
	triggers := &fakeTriggerCreator{}
	tz := time.UTC

	svc := NewService(interpreter, tasks, triggers, tz, WithNow(func() time.Time { return now }))
	reply, err := svc.HandleMessage(context.Background(), "comprar SSD hoy a las 20h")
	if err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(triggers.calls) != 1 {
		t.Fatalf("expected 1 trigger creation, got %d", len(triggers.calls))
	}
	wantTime := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC).UTC().Format(time.RFC3339)
	if triggers.calls[0].Value != wantTime {
		t.Errorf("trigger value = %q, want %q", triggers.calls[0].Value, wantTime)
	}
	if !strings.Contains(reply, "Recordatorio creado") {
		t.Errorf("reply = %q, want confirmation", reply)
	}
}

func TestServiceUnrecognized(t *testing.T) {
	interpreter := &fakeInterpreter{
		result: IntentResult{Unrecognized: true},
	}
	tasks := &fakeTaskCreator{}
	triggers := &fakeTriggerCreator{}

	svc := NewService(interpreter, tasks, triggers, time.UTC)
	reply, err := svc.HandleMessage(context.Background(), "hola")
	if err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(tasks.calls) != 0 || len(triggers.calls) != 0 {
		t.Error("expected no operations for unrecognized message")
	}
	if !strings.Contains(reply, "ALTER") {
		t.Errorf("reply = %q, want mention of ALTER", reply)
	}
}

func TestServiceAmbiguous(t *testing.T) {
	interpreter := &fakeInterpreter{
		result: IntentResult{
			Ambiguous: &AmbiguousIntent{
				Action:              ActionCreateTask,
				MissingFields:       []string{"title"},
				ClarificationPrompt: "¿Qué tarea quieres crear?",
			},
		},
	}
	tasks := &fakeTaskCreator{}
	triggers := &fakeTriggerCreator{}

	svc := NewService(interpreter, tasks, triggers, time.UTC)
	reply, err := svc.HandleMessage(context.Background(), "poné una tarea")
	if err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(tasks.calls) != 0 || len(triggers.calls) != 0 {
		t.Error("expected no operations for ambiguous message")
	}
	if reply != "¿Qué tarea quieres crear?" {
		t.Errorf("reply = %q, want clarification prompt", reply)
	}
}

func TestServiceInterpreterError(t *testing.T) {
	interpreter := &fakeInterpreter{
		err: context.DeadlineExceeded,
	}
	tasks := &fakeTaskCreator{}
	triggers := &fakeTriggerCreator{}

	svc := NewService(interpreter, tasks, triggers, time.UTC)
	reply, err := svc.HandleMessage(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(reply, "No pude entender") {
		t.Errorf("reply = %q, want error message", reply)
	}
}

func TestServiceTaskCreationError(t *testing.T) {
	interpreter := &fakeInterpreter{
		result: IntentResult{
			Recognized: &RecognizedIntent{
				Action: ActionCreateTask,
				Title:  "test",
			},
		},
	}
	tasks := &fakeTaskCreator{createErr: context.DeadlineExceeded}
	triggers := &fakeTriggerCreator{}

	svc := NewService(interpreter, tasks, triggers, time.UTC)
	reply, err := svc.HandleMessage(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(reply, "No pude crear la tarea") {
		t.Errorf("reply = %q, want task creation error", reply)
	}
}

func TestServiceTriggerCreationError(t *testing.T) {
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	interpreter := &fakeInterpreter{
		result: IntentResult{
			Recognized: &RecognizedIntent{
				Action: ActionCreateReminder,
				Title:  "test",
				Reminder: &ReminderSpec{
					Relative: "30m",
				},
			},
		},
	}
	tasks := &fakeTaskCreator{}
	triggers := &fakeTriggerCreator{createErr: context.DeadlineExceeded}

	svc := NewService(interpreter, tasks, triggers, time.UTC, WithNow(func() time.Time { return now }))
	reply, err := svc.HandleMessage(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	// Task was created, but trigger failed
	if len(tasks.calls) != 1 {
		t.Error("expected task to be created before trigger failure")
	}
	if !strings.Contains(reply, "no pude programar el recordatorio") {
		t.Errorf("reply = %q, want trigger error", reply)
	}
}

func TestServiceNilReminderSpec(t *testing.T) {
	interpreter := &fakeInterpreter{
		result: IntentResult{
			Recognized: &RecognizedIntent{
				Action: ActionCreateReminder,
				Title:  "test",
				Reminder: nil,
			},
		},
	}
	tasks := &fakeTaskCreator{}
	triggers := &fakeTriggerCreator{}

	svc := NewService(interpreter, tasks, triggers, time.UTC)
	reply, err := svc.HandleMessage(context.Background(), "test")
	if err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(tasks.calls) != 0 || len(triggers.calls) != 0 {
		t.Error("expected no operations when reminder spec is nil")
	}
	if !strings.Contains(reply, "No pude entender cuándo") {
		t.Errorf("reply = %q, want time clarification", reply)
	}
}

func TestServiceUnknownAction(t *testing.T) {
	interpreter := &fakeInterpreter{
		result: IntentResult{
			Recognized: &RecognizedIntent{
				Action: "unknown_action",
				Title:  "test",
			},
		},
	}
	tasks := &fakeTaskCreator{}
	triggers := &fakeTriggerCreator{}

	svc := NewService(interpreter, tasks, triggers, time.UTC)
	reply, err := svc.HandleMessage(context.Background(), "test")
	if err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if !strings.Contains(reply, "Acción no soportada") {
		t.Errorf("reply = %q, want unsupported action message", reply)
	}
}

func TestServiceWithTimezone(t *testing.T) {
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	buenosAires, _ := time.LoadLocation("America/Argentina/Buenos_Aires")

	interpreter := &fakeInterpreter{
		result: IntentResult{
			Recognized: &RecognizedIntent{
				Action: ActionCreateReminder,
				Title:  "reunión",
				Reminder: &ReminderSpec{
					AbsoluteTime: "09:00",
					AbsoluteDate: "tomorrow",
				},
			},
		},
	}
	tasks := &fakeTaskCreator{}
	triggers := &fakeTriggerCreator{}

	svc := NewService(interpreter, tasks, triggers, buenosAires, WithNow(func() time.Time { return now }))
	_, err := svc.HandleMessage(context.Background(), "reunión mañana a las 9")
	if err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(triggers.calls) != 1 {
		t.Fatalf("expected 1 trigger creation, got %d", len(triggers.calls))
	}
	// 09:00 ART tomorrow = 12:00 UTC on Sep 11
	wantTime := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if triggers.calls[0].Value != wantTime {
		t.Errorf("trigger value = %q, want %q", triggers.calls[0].Value, wantTime)
	}
}
