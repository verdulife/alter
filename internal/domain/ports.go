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
	ListPending(ctx context.Context) ([]Trigger, error)
}

// EventStore defines persistence operations for events.
type EventStore interface {
	Save(ctx context.Context, event Event) error
	ListByType(ctx context.Context, eventType EventType) ([]Event, error)
}

// Scheduler defines the contract for time-based trigger management.
type Scheduler interface {
	Register(ctx context.Context, trigger Trigger) error
	Cancel(ctx context.Context, triggerID string) error
}

// Channel defines the contract for sending messages to external platforms.
type Channel interface {
	Name() string
	Send(ctx context.Context, message string) error
}
