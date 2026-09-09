package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// errPartial models a partial failure (task created, trigger failed).
var errPartial = errors.New("partial reminder failure")

type fakeCommandService struct {
	createCalls   []string
	reminderCalls []string
	reminderDurs  []time.Duration
	err           error
	createdTask   domain.Task
}

func (f *fakeCommandService) CreateTask(_ context.Context, title string) (domain.Task, error) {
	f.createCalls = append(f.createCalls, title)
	if f.err != nil {
		return domain.Task{}, f.err
	}
	return domain.Task{ID: "task-abc", Title: title}, nil
}

func (f *fakeCommandService) CreateReminder(_ context.Context, title string, in time.Duration) (domain.Task, error) {
	f.reminderCalls = append(f.reminderCalls, title)
	f.reminderDurs = append(f.reminderDurs, in)
	if f.err != nil {
		return domain.Task{}, f.err
	}
	if f.createdTask.ID == "" {
		f.createdTask = domain.Task{ID: "task-rem", Title: title}
	}
	return f.createdTask, nil
}

func TestHandleNueva(t *testing.T) {
	svc := &fakeCommandService{}
	reply, err := Handle(context.Background(), svc, "/nueva comprar pan")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(svc.createCalls) != 1 || svc.createCalls[0] != "comprar pan" {
		t.Errorf("CreateTask calls = %v, want [comprar pan]", svc.createCalls)
	}
	if !strings.Contains(reply, "Tarea creada") {
		t.Errorf("reply = %q, want confirmation", reply)
	}
}

func TestHandleNuevaEmptyTitle(t *testing.T) {
	svc := &fakeCommandService{}
	reply, err := Handle(context.Background(), svc, "/nueva")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(svc.createCalls) != 0 {
		t.Errorf("CreateTask should not be called for empty title, got %v", svc.createCalls)
	}
	if !strings.Contains(reply, "Uso: /nueva") {
		t.Errorf("reply = %q, want usage hint", reply)
	}
}

func TestHandleUnknownCommand(t *testing.T) {
	svc := &fakeCommandService{}
	reply, err := Handle(context.Background(), svc, "/listar")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(svc.createCalls) != 0 && len(svc.reminderCalls) != 0 {
		t.Errorf("app should not be called for unknown command")
	}
	if !strings.Contains(reply, "Comando no reconocido") {
		t.Errorf("reply = %q, want unknown-command hint", reply)
	}
}

func TestHandleAppErrorSurfaces(t *testing.T) {
	svc := &fakeCommandService{err: errApp}
	reply, err := Handle(context.Background(), svc, "/nueva algo")
	if err == nil {
		t.Fatal("expected the application error to be returned")
	}
	if !strings.Contains(reply, "No pude crear la tarea") {
		t.Errorf("reply = %q, want failure text", reply)
	}
}

// --- /recordar --------------------------------------------------------------

func parseCall(t *testing.T, text string) (string, time.Duration, error) {
	t.Helper()
	return parseReminder(text)
}

func TestParseReminderValid(t *testing.T) {
	title, d, err := parseCall(t, "/recordar llamar al fontanero in 5m")
	if err != nil {
		t.Fatalf("parseReminder: %v", err)
	}
	if title != "llamar al fontanero" {
		t.Errorf("title = %q, want llamar al fontanero", title)
	}
	if d != 5*time.Minute {
		t.Errorf("duration = %v, want 5m", d)
	}
}

func TestParseReminderSubsecond(t *testing.T) {
	title, d, err := parseCall(t, "/recordar alarma in 1500ms")
	if err != nil {
		t.Fatalf("parseReminder: %v", err)
	}
	if title != "alarma" || d != 1500*time.Millisecond {
		t.Errorf("got %q %v", title, d)
	}
}

func TestParseReminderInvalidDuration(t *testing.T) {
	if _, _, err := parseCall(t, "/recordar tarea in nope"); !errors.Is(err, errReminderDuration) {
		t.Errorf("expected errReminderDuration, got %v", err)
	}
}

func TestParseReminderNoIn(t *testing.T) {
	if _, _, err := parseCall(t, "/recordar tarea 5m"); !errors.Is(err, errReminderSyntax) {
		t.Errorf("expected errReminderSyntax, got %v", err)
	}
}

func TestParseReminderEmptyTitle(t *testing.T) {
	if _, _, err := parseCall(t, "/recordar in 5m"); !errors.Is(err, errReminderEmpty) {
		t.Errorf("expected errReminderEmpty, got %v", err)
	}
}

func TestHandleRecordarValid(t *testing.T) {
	svc := &fakeCommandService{}
	reply, err := Handle(context.Background(), svc, "/recordar recoger pedido in 10s")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(svc.reminderCalls) != 1 || svc.reminderCalls[0] != "recoger pedido" {
		t.Errorf("CreateReminder calls = %v, want [recoger pedido]", svc.reminderCalls)
	}
	if len(svc.reminderDurs) != 1 || svc.reminderDurs[0] != 10*time.Second {
		t.Errorf("durations = %v, want [10s]", svc.reminderDurs)
	}
	if !strings.Contains(reply, "Recordatorio creado") {
		t.Errorf("reply = %q, want confirmation", reply)
	}
	if strings.Contains(reply, "task-") {
		t.Errorf("reply should not expose internal IDs: %q", reply)
	}
}

func TestHandleRecordarBadSyntaxShowsUsage(t *testing.T) {
	svc := &fakeCommandService{}
	reply, err := Handle(context.Background(), svc, "/recordar sin duración")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(svc.reminderCalls) != 0 {
		t.Errorf("app should not be called on bad syntax, got %v", svc.reminderCalls)
	}
	if !strings.Contains(reply, "Uso: /recordar") {
		t.Errorf("reply = %q, want usage hint", reply)
	}
}

func TestHandleRecordarAppErrorSurfaces(t *testing.T) {
	svc := &fakeCommandService{err: errPartial}
	reply, err := Handle(context.Background(), svc, "/recordar algo in 5s")
	if err == nil {
		t.Fatal("expected the application error to be returned")
	}
	if !strings.Contains(reply, "No pude crear el recordatorio") {
		t.Errorf("reply = %q, want failure text", reply)
	}
}
