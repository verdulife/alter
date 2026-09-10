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
// returns a new Trigger reflecting a successful execution. It is a pure domain
// function: no repositories, storage, channels or side effects. It never
// mutates its inputs. A Scheduler is responsible for actually performing the
// notification/action and then persisting the returned trigger.
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
//   - RetryAt     = nil (clears any pending action-retry backoff)
//
// One-shot triggers (at / before_due / after_due):
//   - NextFireAt = nil, Enabled = false: the trigger is consumed and never
//     re-fires.
//
// Recurring triggers (B3 S1):
//   - Value is parsed with ParseRecurrence; an invalid spec fails with no state
//     transition at all (no partial bookkeeping).
//   - NextFireAt advances to NextOccurrence(spec, reference) where reference =
//     max(NextFireAt, executedAt): the calendar advances from the scheduled
//     occurrence, and occurrences already past during downtime are skipped
//     (at most one late fire, no catch-up burst).
//   - Enabled stays true: the trigger remains armed for the next occurrence.
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

	// Successful execution: apply bookkeeping to a copy. LastFiredAt and RetryAt
	// are common to both branches; the scheduling fields differ.
	executed := trigger
	executed.LastFiredAt = &executedAt
	executed.RetryAt = nil // a successful fire clears any pending action-retry backoff

	if trigger.Type == TriggerTypeRecurring {
		// Recurring branch (B3 S1): the calendar in Value is the source of truth.
		// Parse before touching any returned state so an invalid spec fails as a
		// whole, never as a partial transition.
		spec, err := ParseRecurrence(trigger.Value)
		if err != nil {
			return Trigger{}, err
		}
		// Advance from the scheduled occurrence, or from now when the fire ran
		// late (downtime): occurrences between NextFireAt and executedAt are
		// skipped, never re-fired.
		reference := *trigger.NextFireAt
		if executedAt.After(reference) {
			reference = executedAt
		}
		next, err := NextOccurrence(spec, reference)
		if err != nil {
			return Trigger{}, err
		}
		executed.NextFireAt = &next
		executed.Enabled = true // recurring stays armed for the next occurrence
		return executed, nil
	}

	// One-shot branch: consume the trigger (never re-fires).
	executed.NextFireAt = nil
	executed.Enabled = false
	return executed, nil
}
