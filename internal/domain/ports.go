package domain

import "context"

// TaskRepository defines persistence operations for tasks.
type TaskRepository interface {
	Create(ctx context.Context, task Task) error
	GetByID(ctx context.Context, id string) (Task, error)
	Update(ctx context.Context, task Task) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]Task, error)
}

// TriggerRepository defines persistence operations for triggers.
type TriggerRepository interface {
	Create(ctx context.Context, trigger Trigger) error
	GetByID(ctx context.Context, id string) (Trigger, error)
	GetByTaskID(ctx context.Context, taskID string) ([]Trigger, error)
	Update(ctx context.Context, trigger Trigger) error
	Delete(ctx context.Context, id string) error
	// ClearDerivedNextFireAt invalidates the cached NextFireAt of a task's
	// time-derived triggers (before_due / after_due) by setting it to nil.
	// Called when Task.DueAt changes so the Scheduler recalculates them from the
	// new due date via CalculateNextFireAt. It preserves LastFiredAt and Enabled
	// and only touches derived triggers: "at" triggers are absolute and must not
	// be cleared.
	ClearDerivedNextFireAt(ctx context.Context, taskID string) error
	// ListEnabled returns all triggers that are currently enabled, regardless
	// of their NextFireAt. It is the seam for the future Scheduler: enabled
	// triggers are scanned here and filtered by due-ness using the pure
	// execution contract (due = NextFireAt != nil && NextFireAt <= now).
	ListEnabled(ctx context.Context) ([]Trigger, error)
}

// EventStore defines persistence operations for events.
type EventStore interface {
	Save(ctx context.Context, event Event) error
	ListByType(ctx context.Context, eventType EventType) ([]Event, error)
}

// Note on scheduling: the Scheduler is intentionally NOT declared as a port in
// this package. It is an inbound orchestrator, not an outbound dependency the
// application consumes; domain ports only hold driven contracts (repositories,
// EventStore, Channel). The Scheduler will live in the application/service
// layer when implemented (e.g. internal/scheduler), depending on these ports
// plus the pure CalculateNextFireAt/ExecuteTrigger functions.

// Channel defines the contract for sending messages to external platforms.
type Channel interface {
	Name() string
	Send(ctx context.Context, message string) error
}

// TriggerAction executes the user-facing consequence of a fired trigger. It is
// a driven output the Scheduler invokes between ExecuteTrigger and persisting
// fire state. A real implementation (e.g. Telegram) may wrap domain.Channel.
//
// The Scheduler requires a non-nil TriggerAction at construction and must never
// be started with a no-op: a trigger must never be marked fired without a real
// consequence. A failed action leaves the trigger due (it is not consumed).
type TriggerAction interface {
	Execute(ctx context.Context, trigger Trigger, task Task) error
}

// Rescheduler is the small inbound port that lets write services hint the
// Scheduler that the state determining the next deadline may have changed and
// that it should rescan SQLite now. It is deliberately minimal and never the
// source of truth.
//
// Dependency direction: write services depend only on this interface; the
// Scheduler provides a concrete implementation (its non-blocking, coalescing
// Wake). Services must never depend on the scheduler package directly.
//
// Wake is fired only AFTER the relevant state has been persisted (so a rescan
// is meaningful), is non-blocking and coalescing (a hint, not a poll), and its
// absence never breaks correctness: the Scheduler re-reads SQLite on every
// normal cycle anyway.
type Rescheduler interface {
	Wake()
}
