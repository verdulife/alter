package domain

import (
	"errors"
	"testing"
	"time"
)

var (
	execNow  = time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	execTask = Task{ID: "task-1", Title: "t", Status: TaskStatusPending, Priority: TaskPriorityLow}
)

func makeTrigger(next time.Time) Trigger {
	return Trigger{ID: "trg-1", TaskID: "task-1", Type: TriggerTypeAt, Enabled: true, NextFireAt: &next}
}

func TestExecuteFutureTriggerNotExecutable(t *testing.T) {
	tr := makeTrigger(execNow.Add(time.Hour))
	if _, err := ExecuteTrigger(tr, execTask, execNow); !errors.Is(err, ErrTriggerNotDue) {
		t.Errorf("expected ErrTriggerNotDue, got %v", err)
	}
}

func TestExecuteDueTrigger(t *testing.T) {
	tr := makeTrigger(execNow) // due exactly at now
	got, err := ExecuteTrigger(tr, execTask, execNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(execNow) {
		t.Errorf("LastFiredAt should be set to executedAt: %v", got.LastFiredAt)
	}
	if got.NextFireAt != nil {
		t.Errorf("NextFireAt should be cleared, got %v", got.NextFireAt)
	}
	if got.Enabled {
		t.Errorf("trigger should be disabled after execution")
	}
}

func TestExecuteOverdueTrigger(t *testing.T) {
	tr := makeTrigger(execNow.Add(-2 * time.Hour)) // overdue
	if _, err := ExecuteTrigger(tr, execTask, execNow); err != nil {
		t.Errorf("overdue trigger should execute, got %v", err)
	}
}

func TestExecuteDisabledTrigger(t *testing.T) {
	tr := makeTrigger(execNow.Add(-time.Hour))
	tr.Enabled = false
	if _, err := ExecuteTrigger(tr, execTask, execNow); !errors.Is(err, ErrTriggerDisabled) {
		t.Errorf("expected ErrTriggerDisabled, got %v", err)
	}
}

func TestExecuteTriggerWithoutNextFireAt(t *testing.T) {
	tr := Trigger{ID: "trg-1", Type: TriggerTypeAt, Enabled: true} // NextFireAt nil
	if _, err := ExecuteTrigger(tr, execTask, execNow); !errors.Is(err, ErrTriggerNotScheduled) {
		t.Errorf("expected ErrTriggerNotScheduled, got %v", err)
	}
}

func TestExecuteCompletedTaskRejected(t *testing.T) {
	tr := makeTrigger(execNow.Add(-time.Hour))
	task := execTask
	task.Status = TaskStatusCompleted
	if _, err := ExecuteTrigger(tr, task, execNow); !errors.Is(err, ErrTaskNotExecutable) {
		t.Errorf("expected ErrTaskNotExecutable, got %v", err)
	}
}

func TestExecuteCancelledTaskRejected(t *testing.T) {
	tr := makeTrigger(execNow.Add(-time.Hour))
	task := execTask
	task.Status = TaskStatusCancelled
	if _, err := ExecuteTrigger(tr, task, execNow); !errors.Is(err, ErrTaskNotExecutable) {
		t.Errorf("expected ErrTaskNotExecutable, got %v", err)
	}
}

func TestExecuteDoesNotMutateInputs(t *testing.T) {
	next := execNow.Add(-time.Hour)
	due := fixedDue
	tr := makeTrigger(next)
	task := execTask
	task.DueAt = &due

	if _, err := ExecuteTrigger(tr, task, execNow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Trigger unchanged: still enabled, NextFireAt intact, LastFiredAt nil.
	if !tr.Enabled {
		t.Error("input trigger was mutated (Enabled changed)")
	}
	if tr.NextFireAt == nil || !tr.NextFireAt.Equal(next) {
		t.Error("input trigger was mutated (NextFireAt changed)")
	}
	if tr.LastFiredAt != nil {
		t.Error("input trigger was mutated (LastFiredAt set)")
	}
	// Task unchanged.
	if task.DueAt == nil || !task.DueAt.Equal(due) {
		t.Error("input task was mutated (DueAt changed)")
	}
	if task.Status != execTask.Status {
		t.Error("input task was mutated (Status changed)")
	}
}