package domain

import "time"

// EventType identifies the kind of system event.
type EventType string

const (
	EventTaskCreated  EventType = "task.created"
	EventTaskUpdated  EventType = "task.updated"
	EventTaskCompleted EventType = "task.completed"
	EventTaskCancelled EventType = "task.cancelled"
	EventTriggerFired EventType = "trigger.fired"
)

// Event represents a relevant occurrence in the system.
type Event struct {
	ID        string
	Type      EventType
	Payload   map[string]any
	CreatedAt time.Time
}
