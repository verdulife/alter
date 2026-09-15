package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// newReminderHarness wires a ReminderService over the shared in-memory fakes
// with a controllable clock and a deterministic ID sequence (task-N / trg-N).
// The TaskService and TriggerService are constructed directly (like the other
// service test harnesses) so the clock and IDs are injected; both operate on
// the same fake repositories.
func newReminderHarness() (*ReminderService, *fakeTaskRepo, *fakeTriggerRepo, *clock) {
	tasks := newFakeTaskRepo()
	triggers := newFakeTriggerRepo()
	events := newFakeEventStore()
	clk := &clock{t: time.Date(2024, 3, 1, 9, 30, 0, 0, time.UTC)}
	idSeq := 0

	taskSvc := &TaskService{
		tasks:    tasks,
		triggers: triggers,
		events:   events,
		now:      clk.now,
		newID: func() string {
			idSeq++
			return fmt.Sprintf("task-%d", idSeq)
		},
	}
	triggerSvc := &TriggerService{
		triggers: triggers,
		tasks:    tasks,
		now:      clk.now,
		newID: func() string {
			idSeq++
			return fmt.Sprintf("trg-%d", idSeq)
		},
	}
	return NewReminderService(taskSvc, triggerSvc), tasks, triggers, clk
}

// taskTriggers returns the triggers persisted for a task, or fails the test.
func taskTriggers(t *testing.T, repo *fakeTriggerRepo, taskID string) []domain.Trigger {
	t.Helper()
	trs, err := repo.GetByTaskID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("GetByTaskID(%q): %v", taskID, err)
	}
	return trs
}

func TestReminderServiceCreateOneShot(t *testing.T) {
	svc, tasks, triggers, clk := newReminderHarness()
	at := clk.t.Add(30 * time.Minute)

	task, err := svc.CreateOneShot(context.Background(), "comprar pan", "cli", at)
	if err != nil {
		t.Fatalf("CreateOneShot: %v", err)
	}

	if !tasks.has(task.ID) {
		t.Fatalf("task %q must be persisted", task.ID)
	}
	if task.Title != "comprar pan" || task.Source != "cli" {
		t.Errorf("task = (%q, source %q), want (comprar pan, cli)", task.Title, task.Source)
	}
	if task.Status != domain.TaskStatusPending {
		t.Errorf("task status = %q, want pending", task.Status)
	}

	trs := taskTriggers(t, triggers, task.ID)
	if len(trs) != 1 {
		t.Fatalf("expected 1 trigger for the task, got %d", len(trs))
	}
	tr := trs[0]
	if tr.Type != domain.TriggerTypeAt {
		t.Errorf("trigger type = %q, want %q", tr.Type, domain.TriggerTypeAt)
	}
	wantValue := at.UTC().Format(time.RFC3339)
	if tr.Value != wantValue {
		t.Errorf("trigger value = %q, want %q", tr.Value, wantValue)
	}
	if !tr.Enabled {
		t.Error("one-shot trigger must be created enabled")
	}
	if tr.TaskID != task.ID {
		t.Errorf("trigger task_id = %q, want %q", tr.TaskID, task.ID)
	}
}

func TestReminderServiceCreateOneShotTaskFailure(t *testing.T) {
	svc, tasks, triggers, clk := newReminderHarness()
	tasks.createErr = errors.New("storage down")

	_, err := svc.CreateOneShot(context.Background(), "x", "cli", clk.t)
	if err == nil {
		t.Fatal("expected error when task creation fails")
	}
	if !errors.Is(err, tasks.createErr) {
		t.Errorf("error = %v, want the task service error propagated unchanged", err)
	}
	if len(triggers.triggers) != 0 {
		t.Error("no trigger must be created when the task creation failed")
	}
}

func TestReminderServiceCreateOneShotTriggerFailureKeepsTask(t *testing.T) {
	svc, tasks, triggers, _ := newReminderHarness()
	triggers.createErr = errors.New("storage down")

	at := time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)
	task, err := svc.CreateOneShot(context.Background(), "x", "cli", at)
	if err == nil {
		t.Fatal("expected error when trigger creation fails")
	}
	if !errors.Is(err, triggers.createErr) {
		t.Errorf("error = %v, want the trigger service error propagated unchanged", err)
	}
	// The task stays persisted: no rollback (documented, deliberate semantics).
	if task.ID == "" {
		t.Fatal("expected the created task to be returned alongside the error")
	}
	if !tasks.has(task.ID) {
		t.Error("the persisted task must remain (orphaned) after trigger failure")
	}
	if len(triggers.triggers) != 0 {
		t.Error("the failed trigger must not be persisted")
	}
}

func TestReminderServiceCreateRecurring(t *testing.T) {
	svc, tasks, triggers, _ := newReminderHarness()
	value := `{"freq":"daily","interval":1,"weekdays":0,"day_of_month":1,"time":"21:00","timezone":"UTC","anchor":"2026-09-10"}`

	task, err := svc.CreateRecurring(context.Background(), "sacar la basura", "cli", value)
	if err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	if !tasks.has(task.ID) {
		t.Fatalf("task %q must be persisted", task.ID)
	}
	if task.Title != "sacar la basura" || task.Source != "cli" {
		t.Errorf("task = (%q, source %q), want (sacar la basura, cli)", task.Title, task.Source)
	}

	trs := taskTriggers(t, triggers, task.ID)
	if len(trs) != 1 {
		t.Fatalf("expected 1 trigger for the task, got %d", len(trs))
	}
	tr := trs[0]
	if tr.Type != domain.TriggerTypeRecurring {
		t.Errorf("trigger type = %q, want %q", tr.Type, domain.TriggerTypeRecurring)
	}
	if tr.Value != value {
		t.Errorf("trigger value = %q, want %q", tr.Value, value)
	}
	if !tr.Enabled {
		t.Error("recurring trigger must be created enabled")
	}
}

func TestReminderServiceCreateRecurringInvalidSpecRejected(t *testing.T) {
	svc, tasks, triggers, _ := newReminderHarness()
	// TriggerService.Create (via domain.ParseRecurrence) is the authority for
	// recurrence validation: an invalid canonical spec must fail composition.
	_, err := svc.CreateRecurring(context.Background(), "x", "cli", `{"freq":"daily"}`)
	if err == nil {
		t.Fatal("expected error for an invalid recurrence value")
	}
	if !errors.Is(err, ErrInvalidRecurrenceValue) {
		t.Errorf("error = %v, want %v (TriggerService authority)", err, ErrInvalidRecurrenceValue)
	}
	if len(triggers.triggers) != 0 {
		t.Error("no trigger must be persisted for a rejected recurrence")
	}
	// The task was still created first (documented partial semantics). The
	// harness issues task-1 to the TaskService; assert an orphaned task exists.
	if stored, _ := tasks.List(context.Background()); len(stored) != 1 {
		t.Errorf("expected the orphaned task to remain, got %d tasks", len(stored))
	}
}
