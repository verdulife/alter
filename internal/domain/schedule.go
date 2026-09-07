package domain

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors returned by CalculateNextFireAt.
var (
	// ErrTriggerCustomUnsupported means the "custom" trigger type is not yet supported.
	ErrTriggerCustomUnsupported = errors.New("custom trigger type is not supported yet")
	// ErrUnknownTriggerType means the trigger type is not recognised.
	ErrUnknownTriggerType = errors.New("unknown trigger type")
	// ErrTaskDueAtMissing means a before/after_due trigger requires Task.DueAt.
	ErrTaskDueAtMissing = errors.New("task has no due date (DueAt is nil)")
	// ErrInvalidTimestamp means an "at" trigger carries a malformed timestamp.
	ErrInvalidTimestamp = errors.New("invalid timestamp for at trigger")
	// ErrInvalidDuration means a duration string is not parseable.
	ErrInvalidDuration = errors.New("invalid duration")
	// ErrNegativeDuration means a duration string is negative.
	ErrNegativeDuration = errors.New("duration must not be negative")
)

// CalculateNextFireAt computes the deterministic next-fire time for a trigger,
// given the task it is attached to. It is a pure function: it never mutates
// trigger or task, never touches storage, and has no side effects.
//
// Supported types (V1):
//   - TriggerTypeAt:      Value is an RFC3339 timestamp; NextFireAt is that value.
//   - TriggerTypeBeforeDue: Value is a Go duration; NextFireAt = DueAt - d.
//   - TriggerTypeAfterDue:  Value is a Go duration; NextFireAt = DueAt + d.
//   - TriggerTypeCustom:   returns ErrTriggerCustomUnsupported.
func CalculateNextFireAt(trigger Trigger, task Task) (time.Time, error) {
	switch trigger.Type {
	case TriggerTypeAt:
		return parseTimestamp(trigger.Value)

	case TriggerTypeBeforeDue, TriggerTypeAfterDue:
		d, err := parseDuration(trigger.Value)
		if err != nil {
			return time.Time{}, err
		}
		if d < 0 {
			return time.Time{}, fmt.Errorf("%w: %s", ErrNegativeDuration, trigger.Value)
		}
		if task.DueAt == nil {
			return time.Time{}, ErrTaskDueAtMissing
		}
		if trigger.Type == TriggerTypeBeforeDue {
			return task.DueAt.Add(-d), nil
		}
		return task.DueAt.Add(d), nil

	case TriggerTypeCustom:
		return time.Time{}, ErrTriggerCustomUnsupported

	default:
		return time.Time{}, fmt.Errorf("%w: %q", ErrUnknownTriggerType, trigger.Type)
	}
}

// parseTimestamp parses an RFC3339 timestamp.
func parseTimestamp(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidTimestamp, value)
	}
	return t, nil
}

// parseDuration parses a Go duration string.
func parseDuration(value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrInvalidDuration, value)
	}
	return d, nil
}