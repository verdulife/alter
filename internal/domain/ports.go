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
