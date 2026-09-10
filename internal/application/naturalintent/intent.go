// Package naturalintent implements the V1 natural-language interpretation
// layer for ALTER: it turns free-text Telegram messages into structured
// intents that the application can execute.
//
// Architecture boundary: this package lives in application/, not domain/.
// It depends on domain (for Task/Trigger types) but domain never depends on it.
// The Pi LLM is the interpreter backend; Go owns all state mutations.
package naturalintent

import (
	"time"

	"github.com/verdu/alter/internal/domain"
)

// Action identifies the operation the user intends.
type Action string

const (
	// ActionCreateTask is a plain task without a reminder trigger.
	ActionCreateTask Action = "create_task"
	// ActionCreateReminder is a task plus a scheduled reminder trigger.
	ActionCreateReminder Action = "create_reminder"
	// ActionListTasks lists pending tasks.
	ActionListTasks Action = "list_tasks"
	// ActionCompleteTask marks an existing pending task as completed.
	ActionCompleteTask Action = "complete_task"
	// ActionCancelTask marks an existing pending task as cancelled.
	ActionCancelTask Action = "cancel_task"
)

// IntentResult is the three-state output of the natural language interpreter.
// Exactly one of the three fields determines the next step:
//   - Recognized: clear intent, sufficient params → execute immediately.
//   - Ambiguous: clear intent but missing/uncertain data → ask a question.
//   - Unrecognized: cannot determine intent → show help.
type IntentResult struct {
	// Recognized holds the parsed intent when the interpreter is confident
	// that the user's message has a clear action, title, and (if applicable)
	// time specification. The caller proceeds to execute.
	Recognized *RecognizedIntent `json:"recognized,omitempty"`
	// Ambiguous holds the partial parse when the intent is clear but
	// required data is missing or uncertain. The caller asks a targeted
	// clarification question and waits for the next message.
	Ambiguous *AmbiguousIntent `json:"ambiguous,omitempty"`
	// Unrecognized is true when the interpreter cannot determine any
	// actionable intent from the message. The caller shows help text.
	Unrecognized bool `json:"unrecognized"`
}

// RecognizedIntent is the fully parsed intent ready for execution.
type RecognizedIntent struct {
	Action   Action        `json:"action"`
	Title    string        `json:"title"`
	Reminder *ReminderSpec `json:"reminder,omitempty"`
	// TaskRef is the textual reference to an existing task (for complete/cancel).
	// Pi extracts this from the user's message; Go resolves it against the database.
	TaskRef string `json:"task_ref,omitempty"`
}

// ReminderSpec describes when the reminder should fire.
// The LLM never produces a time.Time; it produces one of two forms:
//   - Relative: a Go duration string like "30m", "1h30m".
//   - Absolute: a local time + optional date like "20:00" + "today".
//   - Recurrence: a recurring cadence (daily/weekly/monthly/yearly).
//
// Relative/Absolute and Recurrence are mutually exclusive (contradictory specs
// are rejected as ambiguous). The application resolves the final time using the
// current time and the user's timezone.
//
// Recurrence is a B3 S4 extension: Pi only extracts the cadence from the
// message; Go builds the canonical RecurrenceSpec JSON and validates it with
// domain.ParseRecurrence before persisting.
type ReminderSpec struct {
	// Relative is a Go duration string (e.g. "30m", "1h30m", "2h").
	// Resolved as: now.Add(parsed).
	Relative string `json:"relative,omitempty"`
	// AbsoluteTime is a local time in "HH:MM" format (e.g. "20:00", "09:30").
	// Must be paired with AbsoluteDate (or defaults to "today").
	AbsoluteTime string `json:"absolute_time,omitempty"`
	// AbsoluteDate is "today", "tomorrow", or "YYYY-MM-DD".
	// Omitted when Relative is present.
	AbsoluteDate string `json:"absolute_date,omitempty"`
	// Recurrence is the recurring cadence (B3 S4). Mutually exclusive with
	// Relative/AbsoluteTime. nil means a one-shot reminder.
	Recurrence *RecurrenceParams `json:"recurrence,omitempty"`
}

// RecurrenceParams is the natural-language layer's view of a recurring cadence
// (B3 S4). It carries ONLY what Pi extracted from the user message; Go derives
// timezone, interval default, anchor year and validates the resulting spec.
// Nil pointers and empty slices mean "not provided" (never a zero value).
type RecurrenceParams struct {
	// Freq is daily, weekly, monthly or yearly.
	Freq domain.RecurrenceFreq `json:"freq"`
	// Interval is the cadence multiple ("cada 2 semanas"). nil -> 1.
	Interval *int `json:"interval,omitempty"`
	// Time is the local wall-clock time in "HH:MM" ("a las 9" -> "09:00").
	// Required for recurring reminders.
	Time string `json:"time,omitempty"`
	// Weekdays are 1=monday..7=sunday, weekly only.
	Weekdays []int `json:"weekdays,omitempty"`
	// DayOfMonth is 1..31, monthly only (domain clamps to the month's last day).
	DayOfMonth *int `json:"day_of_month,omitempty"`
	// AnchorMonth/AnchorDay of an explicit start date ("el 10 de septiembre").
	// Required for yearly; optional for the other frequencies. Go completes the
	// year from the current date (InterpretContext.Now).
	AnchorMonth *int `json:"anchor_month,omitempty"`
	AnchorDay   *int `json:"anchor_day,omitempty"`
	// AnchorYear is an explicit start year ("a partir de 2027"). Go defaults to
	// the current year; AnchorYear requires AnchorMonth and AnchorDay.
	AnchorYear *int `json:"anchor_year,omitempty"`
}

// AmbiguousIntent is produced when the intent is clear but required fields
// are missing or the message is too vague to act on.
type AmbiguousIntent struct {
	Action         Action   `json:"action"`
	MissingFields  []string `json:"missing_fields"`
	CandidateTitle string   `json:"candidate_title,omitempty"`
	// ClarificationPrompt is the exact question to send to the user.
	ClarificationPrompt string `json:"clarification_prompt"`
}

// InterpretContext carries the runtime context the interpreter needs
// to resolve time expressions. The LLM never sees this; Go provides it.
type InterpretContext struct {
	// Now is the current time. Used for relative resolution and for
	// deciding "today" vs "tomorrow" when an absolute time has passed.
	Now time.Time
	// Timezone is the user's timezone. Resolved from ALTER_TIMEZONE or time.Local.
	Timezone *time.Location
}
