package domain

import "time"

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusCancelled  TaskStatus = "cancelled"
)

// TaskPriority represents the urgency level of a task.
type TaskPriority string

const (
	TaskPriorityLow    TaskPriority = "low"
	TaskPriorityMedium TaskPriority = "medium"
	TaskPriorityHigh   TaskPriority = "high"
	TaskPriorityUrgent TaskPriority = "urgent"
)

// Task represents a unit of work to be tracked by the system.
type Task struct {
	ID          string
	Title       string
	Description string
	Status      TaskStatus
	Priority    TaskPriority
	DueAt       *time.Time
	Source      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
