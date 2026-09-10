package domain

import "time"

// TriggerType defines how a trigger activates.
type TriggerType string

const (
	TriggerTypeAt        TriggerType = "at"         // Fires at a specific datetime
	TriggerTypeBeforeDue TriggerType = "before_due" // Fires N duration before DueAt
	TriggerTypeAfterDue  TriggerType = "after_due"  // Fires N duration after DueAt
	TriggerTypeRecurring TriggerType = "recurring"  // Repeats on a deterministic recurring calendar (B3 S1)
	TriggerTypeCustom    TriggerType = "custom"     // Reserved for future use
)

// Trigger represents a scheduled activation associated with a task.
//
// NextFireAt, LastFiredAt and RetryAt are execution bookkeeping used by the
// Scheduler; they may all be nil.
//   - NextFireAt is the derived scheduling deadline (from Type/Value/Task.DueAt),
//     nil when not yet computed.
//   - LastFiredAt records the last time the trigger actually fired.
//   - RetryAt is transient operational state: it is set when a due trigger's
//     TriggerAction failed, and holds the earliest time the retry becomes
//     eligible. It is unrelated to the derived deadline.
type Trigger struct {
	ID          string
	TaskID      string
	Type        TriggerType
	Value       string // Encoded value: ISO timestamp for "at", duration string for "before_due"/"after_due", canonical recurrence spec JSON for "recurring" (B3 S1)
	Enabled     bool
	NextFireAt  *time.Time // Derived scheduling deadline (nil when not yet computed)
	LastFiredAt *time.Time // Last actual run (nil when never fired)
	RetryAt     *time.Time // Action-retry backoff deadline (nil when no retry pending)
	CreatedAt   time.Time
}
