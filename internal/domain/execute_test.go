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

// --- B3 S1: recurring triggers ---

// Canonical recurrence specs (validated by ParseRecurrence; calendar math is
// tested independently in recurrence_test.go, so expected instants are hardcoded
// here to pin the ExecuteTrigger integration contract).
const (
	// Daily at 09:00 UTC, anchored 2024-05-01 (a Wednesday).
	recDailyJSON = `{"freq":"daily","interval":1,"time":"09:00","timezone":"UTC","anchor":"2024-05-01"}`
	// Weekly on Monday+Wednesday (mask 1|4 = 5) at 09:00 UTC, anchored
	// Monday 2024-04-29.
	recWeeklyJSON = `{"freq":"weekly","interval":1,"weekdays":5,"time":"09:00","timezone":"UTC","anchor":"2024-04-29"}`
)

// makeRecurringTrigger builds an enabled recurring trigger (daily by default)
// armed at next. Use Value = recWeeklyJSON for the weekly calendar cases.
func makeRecurringTrigger(next time.Time) Trigger {
	return Trigger{ID: "trg-rec", TaskID: "task-1", Type: TriggerTypeRecurring, Value: recDailyJSON, Enabled: true, NextFireAt: &next}
}

func TestExecuteRecurringTriggerAdvancesToNextOccurrence(t *testing.T) {
	// The due daily occurrence (Wed 2024-05-01 09:00) fires at 10:00: the
	// trigger stays enabled and NextFireAt advances to the next calendar
	// occurrence (Thu 2024-05-02 09:00).
	next := time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)
	got, err := ExecuteTrigger(makeRecurringTrigger(next), execTask, execNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantNext := time.Date(2024, 5, 2, 9, 0, 0, 0, time.UTC)
	if got.NextFireAt == nil || !got.NextFireAt.Equal(wantNext) {
		t.Errorf("NextFireAt should advance to %v, got %v", wantNext, got.NextFireAt)
	}
	if !got.Enabled {
		t.Error("recurring trigger must stay enabled after a successful fire")
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(execNow) {
		t.Errorf("LastFiredAt should be set to executedAt %v, got %v", execNow, got.LastFiredAt)
	}
	if got.RetryAt != nil {
		t.Errorf("RetryAt should be nil after success, got %v", got.RetryAt)
	}
}

func TestExecuteRecurringTriggerSkipsPastOccurrencesDuringDowntime(t *testing.T) {
	// Weekly calendar (Mon/Wed 09:00, anchor Mon 2024-04-29) last armed at
	// Mon 2024-04-29 09:00, but the fire happens late on Wed 2024-05-01 10:00.
	// Reference = max(NextFireAt, executedAt) = executedAt, so the Wed 09:00
	// occurrence of the same cycle (already past) is skipped and exactly ONE
	// advance is produced: the next future occurrence is Mon 2024-05-06 09:00
	// (no catch-up burst).
	next := time.Date(2024, 4, 29, 9, 0, 0, 0, time.UTC)
	tr := makeRecurringTrigger(next)
	tr.Value = recWeeklyJSON
	got, err := ExecuteTrigger(tr, execTask, execNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantNext := time.Date(2024, 5, 6, 9, 0, 0, 0, time.UTC)
	if got.NextFireAt == nil || !got.NextFireAt.Equal(wantNext) {
		t.Errorf("NextFireAt should skip past occurrences and land on %v, got %v", wantNext, got.NextFireAt)
	}
	if !got.Enabled {
		t.Error("recurring trigger must stay enabled after a successful fire")
	}
}

func TestExecuteRecurringTriggerClearsRetryAt(t *testing.T) {
	// An elapsed RetryAt backoff (from a previous failed action) is cleared by
	// a successful recurring fire, exactly like the one-shot path.
	next := time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)
	elapsedRetry := execNow.Add(-time.Minute)
	tr := makeRecurringTrigger(next)
	tr.RetryAt = &elapsedRetry
	got, err := ExecuteTrigger(tr, execTask, execNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.RetryAt != nil {
		t.Errorf("RetryAt must be cleared after a successful recurring fire, got %v", got.RetryAt)
	}
}

func TestExecuteRecurringTriggerRejectsNonExecutableTask(t *testing.T) {
	// A completed/cancelled task is terminal regardless of recurrence: no
	// calendar advance, no state transition, input untouched.
	for _, status := range []TaskStatus{TaskStatusCompleted, TaskStatusCancelled} {
		task := execTask
		task.Status = status
		if _, err := ExecuteTrigger(makeRecurringTrigger(execNow.Add(-time.Hour)), task, execNow); !errors.Is(err, ErrTaskNotExecutable) {
			t.Errorf("status %s: expected ErrTaskNotExecutable, got %v", status, err)
		}
	}
}

func TestExecuteRecurringTriggerRejectsInvalidValue(t *testing.T) {
	// An invalid recurrence Value must fail before any state transition.
	tr := makeRecurringTrigger(execNow.Add(-time.Hour))
	tr.Value = "not-a-recurrence"
	if _, err := ExecuteTrigger(tr, execTask, execNow); !errors.Is(err, ErrInvalidRecurrenceJSON) {
		t.Errorf("expected ErrInvalidRecurrenceJSON, got %v", err)
	}
}

func TestExecuteRecurringTriggerDoesNotMutateInput(t *testing.T) {
	next := time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)
	retry := execNow.Add(-time.Minute)
	tr := makeRecurringTrigger(next)
	tr.RetryAt = &retry

	if _, err := ExecuteTrigger(tr, execTask, execNow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Trigger unchanged: still enabled, NextFireAt/RetryAt intact, LastFiredAt nil.
	if !tr.Enabled {
		t.Error("input trigger was mutated (Enabled changed)")
	}
	if tr.NextFireAt == nil || !tr.NextFireAt.Equal(next) {
		t.Error("input trigger was mutated (NextFireAt changed)")
	}
	if tr.LastFiredAt != nil {
		t.Error("input trigger was mutated (LastFiredAt set)")
	}
	if tr.RetryAt == nil || !tr.RetryAt.Equal(retry) {
		t.Error("input trigger was mutated (RetryAt changed)")
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
