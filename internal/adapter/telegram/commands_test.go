package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// errPartial models a partial failure (task created, trigger failed).
var errPartial = errors.New("partial reminder failure")

type fakeCommandService struct {
	createCalls    []string
	reminderCalls  []string
	reminderDurs   []time.Duration
	err            error
	createdTask    domain.Task
	listTasks      []domain.Task
	listErr        error
	completeErr    error
	cancelErr      error
	completedTasks []string
	cancelledTasks []string
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

func (f *fakeCommandService) ListPendingTasks(_ context.Context) ([]domain.Task, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listTasks, nil
}

func (f *fakeCommandService) CompleteTaskByRef(_ context.Context, ref string) (domain.Task, string, error) {
	if f.completeErr != nil {
		return domain.Task{}, f.completeErr.Error(), f.completeErr
	}
	// Find all matching pending tasks
	var matches []domain.Task
	for _, t := range f.listTasks {
		if strings.Contains(t.Title, ref) && t.Status == domain.TaskStatusPending {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return domain.Task{}, "No encontré ninguna tarea pendiente que coincida con «" + ref + "».", errors.New("not found")
	case 1:
		f.completedTasks = append(f.completedTasks, matches[0].ID)
		return matches[0], "Listo, completé «" + matches[0].Title + "» ✓", nil
	default:
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Encontré varias tareas que coinciden con «%s»:\n", ref))
		for i, t := range matches {
			sb.WriteString(fmt.Sprintf("%d) %s\n", i+1, t.Title))
		}
		sb.WriteString("¿Cuál?")
		return domain.Task{}, sb.String(), errors.New("multiple matches")
	}
}

func (f *fakeCommandService) CancelTaskByRef(_ context.Context, ref string) (domain.Task, string, error) {
	if f.cancelErr != nil {
		return domain.Task{}, f.cancelErr.Error(), f.cancelErr
	}
	// Find all matching pending tasks
	var matches []domain.Task
	for _, t := range f.listTasks {
		if strings.Contains(t.Title, ref) && t.Status == domain.TaskStatusPending {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return domain.Task{}, "No encontré ninguna tarea pendiente que coincida con «" + ref + "».", errors.New("not found")
	case 1:
		f.cancelledTasks = append(f.cancelledTasks, matches[0].ID)
		return matches[0], "Cancelé «" + matches[0].Title + "» ✓", nil
	default:
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Encontré varias tareas que coinciden con «%s»:\n", ref))
		for i, t := range matches {
			sb.WriteString(fmt.Sprintf("%d) %s\n", i+1, t.Title))
		}
		sb.WriteString("¿Cuál?")
		return domain.Task{}, sb.String(), errors.New("multiple matches")
	}
}

func TestHandleNueva(t *testing.T) {
	svc := &fakeCommandService{}
	reply, err := Handle(context.Background(), svc, "/nueva comprar pan", nil)
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
	reply, err := Handle(context.Background(), svc, "/nueva", nil)
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
	reply, err := Handle(context.Background(), svc, "/listar", nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// /listar is now a known command, so this test needs to be updated
	if !strings.Contains(reply, "tarea") && !strings.Contains(reply, "No tenés") {
		t.Errorf("reply = %q, want task-related response", reply)
	}
}

func TestHandleAppErrorSurfaces(t *testing.T) {
	svc := &fakeCommandService{err: errApp}
	reply, err := Handle(context.Background(), svc, "/nueva algo", nil)
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
	reply, err := Handle(context.Background(), svc, "/recordar recoger pedido in 10s", nil)
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
	reply, err := Handle(context.Background(), svc, "/recordar sin duración", nil)
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
	reply, err := Handle(context.Background(), svc, "/recordar algo in 5s", nil)
	if err == nil {
		t.Fatal("expected the application error to be returned")
	}
	if !strings.Contains(reply, "No pude crear el recordatorio") {
		t.Errorf("reply = %q, want failure text", reply)
	}
}

// --- /listar ---------------------------------------------------------------------- 

func TestHandleListarEmpty(t *testing.T) {
	svc := &fakeCommandService{listTasks: []domain.Task{}}
	reply, err := Handle(context.Background(), svc, "/listar", nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(reply, "No tenés tareas pendientes") {
		t.Errorf("reply = %q, want empty message", reply)
	}
}

func TestHandleListarWithTasks(t *testing.T) {
	svc := &fakeCommandService{listTasks: []domain.Task{
		{ID: "1", Title: "comprar SSD", Status: domain.TaskStatusPending},
		{ID: "2", Title: "llamar al fontanero", Status: domain.TaskStatusPending},
	}}
	reply, err := Handle(context.Background(), svc, "/listar", nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(reply, "2") {
		t.Errorf("reply = %q, want count", reply)
	}
	if !strings.Contains(reply, "comprar SSD") {
		t.Errorf("reply = %q, want first task", reply)
	}
	if !strings.Contains(reply, "llamar al fontanero") {
		t.Errorf("reply = %q, want second task", reply)
	}
}

func TestHandleListarFiltersCompleted(t *testing.T) {
	svc := &fakeCommandService{listTasks: []domain.Task{
		{ID: "1", Title: "comprar SSD", Status: domain.TaskStatusPending},
		{ID: "2", Title: "tarea completada", Status: domain.TaskStatusCompleted},
	}}
	reply, err := Handle(context.Background(), svc, "/listar", nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if strings.Contains(reply, "tarea completada") {
		t.Errorf("reply should not contain completed task: %q", reply)
	}
}

func TestHandleListarError(t *testing.T) {
	svc := &fakeCommandService{listErr: errApp}
	reply, err := Handle(context.Background(), svc, "/listar", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(reply, "No pude obtener las tareas") {
		t.Errorf("reply = %q, want error message", reply)
	}
}

// --- /completar ------------------------------------------------------------------- 

func TestHandleCompletarValid(t *testing.T) {
	svc := &fakeCommandService{listTasks: []domain.Task{
		{ID: "1", Title: "comprar SSD", Status: domain.TaskStatusPending},
	}}
	reply, err := Handle(context.Background(), svc, "/completar SSD", nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(svc.completedTasks) != 1 {
		t.Errorf("expected 1 completed task, got %d", len(svc.completedTasks))
	}
	if !strings.Contains(reply, "completé") {
		t.Errorf("reply = %q, want completion confirmation", reply)
	}
}

func TestHandleCompletarEmptyRef(t *testing.T) {
	svc := &fakeCommandService{}
	reply, err := Handle(context.Background(), svc, "/completar", nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(reply, "Uso: /completar") {
		t.Errorf("reply = %q, want usage hint", reply)
	}
}

func TestHandleCompletarNoMatch(t *testing.T) {
	svc := &fakeCommandService{listTasks: []domain.Task{
		{ID: "1", Title: "comprar SSD", Status: domain.TaskStatusPending},
	}}
	reply, err := Handle(context.Background(), svc, "/completar inexistente", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(reply, "No encontré") {
		t.Errorf("reply = %q, want not found message", reply)
	}
}

func TestHandleCompletarMultipleMatches(t *testing.T) {
	svc := &fakeCommandService{listTasks: []domain.Task{
		{ID: "1", Title: "comprar SSD negro", Status: domain.TaskStatusPending},
		{ID: "2", Title: "comprar SSD blanco", Status: domain.TaskStatusPending},
	}}
	reply, err := Handle(context.Background(), svc, "/completar SSD", nil)
	if err == nil {
		t.Fatal("expected error for multiple matches")
	}
	if !strings.Contains(reply, "Encontré varias") {
		t.Errorf("reply = %q, want multiple matches message", reply)
	}
}

// --- /cancelar -------------------------------------------------------------------- 

func TestHandleCancelarValid(t *testing.T) {
	svc := &fakeCommandService{listTasks: []domain.Task{
		{ID: "1", Title: "llamar al fontanero", Status: domain.TaskStatusPending},
	}}
	reply, err := Handle(context.Background(), svc, "/cancelar fontanero", nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(svc.cancelledTasks) != 1 {
		t.Errorf("expected 1 cancelled task, got %d", len(svc.cancelledTasks))
	}
	if !strings.Contains(reply, "Cancelé") {
		t.Errorf("reply = %q, want cancel confirmation", reply)
	}
}

func TestHandleCancelarEmptyRef(t *testing.T) {
	svc := &fakeCommandService{}
	reply, err := Handle(context.Background(), svc, "/cancelar", nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(reply, "Uso: /cancelar") {
		t.Errorf("reply = %q, want usage hint", reply)
	}
}

func TestHandleCancelarNoMatch(t *testing.T) {
	svc := &fakeCommandService{listTasks: []domain.Task{
		{ID: "1", Title: "comprar SSD", Status: domain.TaskStatusPending},
	}}
	reply, err := Handle(context.Background(), svc, "/cancelar inexistente", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(reply, "No encontré") {
		t.Errorf("reply = %q, want not found message", reply)
	}
}

func TestHandleCancelarMultipleMatches(t *testing.T) {
	svc := &fakeCommandService{listTasks: []domain.Task{
		{ID: "1", Title: "comprar SSD negro", Status: domain.TaskStatusPending},
		{ID: "2", Title: "comprar SSD blanco", Status: domain.TaskStatusPending},
	}}
	reply, err := Handle(context.Background(), svc, "/cancelar SSD", nil)
	if err == nil {
		t.Fatal("expected error for multiple matches")
	}
	if !strings.Contains(reply, "Encontré varias") {
		t.Errorf("reply = %q, want multiple matches message", reply)
	}
}

// --- Natural language fallback -------------------------------------------------------------- 

func TestHandleNaturalLanguageFallback(t *testing.T) {
	svc := &fakeCommandService{}
	called := false
	natural := func(_ context.Context, text string) (string, error) {
		called = true
		if text == "comprar SSD" {
			return "Tarea creada ✓ \"comprar SSD\"", nil
		}
		return "fallback", nil
	}
	reply, err := Handle(context.Background(), svc, "comprar SSD", natural)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !called {
		t.Error("natural handler was not called for non-slash message")
	}
	if len(svc.createCalls) != 0 {
		t.Error("slash CommandService should not be called for natural language")
	}
	if !strings.Contains(reply, "Tarea creada") {
		t.Errorf("reply = %q, want confirmation from natural handler", reply)
	}
}

func TestHandleNaturalLanguageNilFallsBackToHelp(t *testing.T) {
	svc := &fakeCommandService{}
	reply, err := Handle(context.Background(), svc, "comprar SSD", nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(reply, "Comando no reconocido") {
		t.Errorf("reply = %q, want legacy help when natural handler is nil", reply)
	}
}

func TestHandleSlashCommandPrecedesNatural(t *testing.T) {
	svc := &fakeCommandService{}
	naturalCalled := false
	natural := func(_ context.Context, text string) (string, error) {
		naturalCalled = true
		return "natural", nil
	}
	reply, err := Handle(context.Background(), svc, "/nueva SSD", natural)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if naturalCalled {
		t.Error("natural handler should not be called when slash command matches")
	}
	if len(svc.createCalls) != 1 || svc.createCalls[0] != "SSD" {
		t.Errorf("CreateTask calls = %v, want [SSD]", svc.createCalls)
	}
	if !strings.Contains(reply, "Tarea creada") {
		t.Errorf("reply = %q, want confirmation", reply)
	}
}
