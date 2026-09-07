package domain

import "time"

// TriggerType defines how a trigger activates.
type TriggerType string

const (
	TriggerTypeAt            TriggerType = "at"             // Fires at a specific datetime
	TriggerTypeBeforeDue     TriggerType = "before_due"     // Fires N duration before DueAt
	TriggerTypeAfterDue      TriggerType = "after_due"      // Fires N duration after DueAt
	TriggerTypeCustom        TriggerType = "custom"         // Reserved for future use
)

// Trigger represents a scheduled activation associated with a task.
//
// NextFireAt and LastFiredAt are execution bookkeeping used by a future
// Scheduler; they may both be nil. NextFireAt is when the trigger should next
// run, LastFiredAt records the last time it actually ran.
type Trigger struct {
	ID          string
	TaskID      string
	Type        TriggerType
	Value       string      // Encoded value: ISO timestamp for "at", duration string for before/after
	Enabled     bool
	NextFireAt  *time.Time  // Next scheduled run (nil when not yet computed)
	LastFiredAt *time.Time  // Last actual run (nil when never fired)
	CreatedAt   time.Time
}
