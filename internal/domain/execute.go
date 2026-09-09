package domain

import (
	"errors"
	"time"
)

// Sentinel errors returned by ExecuteTrigger.
var (
	// ErrTriggerDisabled means the trigger is not enabled.
	ErrTriggerDisabled = errors.New("trigger is disabled")
	// ErrTriggerNotScheduled means the trigger has no NextFireAt set.
	ErrTriggerNotScheduled = errors.New("trigger has no next fire time set")
	// ErrTriggerNotDue means the trigger is enabled and scheduled, but its
	// NextFireAt is still in the future.
	ErrTriggerNotDue = errors.New("trigger is not due yet")
	// ErrTaskNotExecutable means the trigger's task is completed or cancelled.
	ErrTaskNotExecutable = errors.New("task is not executable")
)

// ExecuteTrigger decides whether a trigger may fire at executedAt and, if so,
// returns a new Trigger reflecting a successful one-shot execution. It is a
// pure domain function: no repositories, storage, channels or side effects.
// It never mutates its inputs. A future Scheduler is responsible for actually
// performing the notification/action and then persisting the returned trigger.
//
// V1 execution semantics:
//   - A trigger is executable when Enabled && NextFireAt != nil &&
//     NextFireAt <= executedAt. Overdue triggers stay executable, so a trigger
//     whose NextFireAt fell in the past during downtime is not ignored after a
//     process restart.
//   - A trigger bound to a completed or cancelled task is not executed.
//   - Only on success are these bookkeeping changes applied (atomically by the
//     caller when persisting):
//   - LastFiredAt = executedAt
//   - NextFireAt  = nil (one-shot, never re-fires)
//   - Enabled     = false
//   - RetryAt     = nil (clears any pending action-retry backoff)
//
// Recurring triggers are out of scope for V1.
func ExecuteTrigger(trigger Trigger, task Task, executedAt time.Time) (Trigger, error) {
	if !trigger.Enabled {
		return Trigger{}, ErrTriggerDisabled
	}
	if trigger.NextFireAt == nil {
		return Trigger{}, ErrTriggerNotScheduled
	}
	if trigger.NextFireAt.After(executedAt) {
		return Trigger{}, ErrTriggerNotDue
	}
	if task.Status == TaskStatusCompleted || task.Status == TaskStatusCancelled {
		return Trigger{}, ErrTaskNotExecutable
	}

	// Successful execution: apply one-shot bookkeeping to a copy.
	executed := trigger
	executed.LastFiredAt = &executedAt
	executed.NextFireAt = nil
	executed.Enabled = false
	executed.RetryAt = nil // a successful fire clears any pending action-retry backoff
	return executed, nil
}
