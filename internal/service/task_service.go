package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// Sentinel errors for state transitions considered invalid in V1.
var (
	// ErrCannotComplete is returned when trying to complete a cancelled task.
	ErrCannotComplete = errors.New("cannot complete a cancelled task")
	// ErrCannotCancel is returned when trying to cancel a completed task.
	ErrCannotCancel = errors.New("cannot cancel a completed task")
)

// CreateTaskParams carries the user-supplied fields for creating a task.
// Status, ID and timestamps are managed by the service.
type CreateTaskParams struct {
	Title       string
	Description string
	Priority    domain.TaskPriority
	DueAt       *time.Time
	Source      string
}

// UpdateTaskParams carries optional field changes for Update. A nil pointer
// leaves the corresponding field unchanged. Clearing DueAt is not supported
// in V1.
type UpdateTaskParams struct {
	Title       *string
	Description *string
	Priority    *domain.TaskPriority
	DueAt       *time.Time
	Source      *string
}

// TaskService encapsulates application operations over domain.Task.
// It depends only on domain interfaces; it never touches concrete storage
// (e.g. SQLite), keeping the service testable and storage-agnostic.
type TaskService struct {
	tasks       domain.TaskRepository
	triggers    domain.TriggerRepository
	events      domain.EventStore
	now         func() time.Time
	newID       func() string
	rescheduler domain.Rescheduler
}

// TaskOption configures a TaskService (dependency injection for testing and the
// optional Scheduler hint).
type TaskOption func(*TaskService)

// WithTaskRescheduler wires the optional inbound port that hints the Scheduler to
// rescan after a persisted change that may move a deadline (e.g. a DueAt change).
// It is optional: without it the service still works (the Scheduler re-reads
// SQLite on its next normal cycle); Wake only accelerates re-evaluation.
func WithTaskRescheduler(r domain.Rescheduler) TaskOption {
	return func(s *TaskService) { s.rescheduler = r }
}

// NewTaskService builds a TaskService with production defaults.
func NewTaskService(tasks domain.TaskRepository, triggers domain.TriggerRepository, events domain.EventStore, opts ...TaskOption) *TaskService {
	s := &TaskService{
		tasks:    tasks,
		triggers: triggers,
		events:   events,
		now:      time.Now,
		newID:    newLocalID,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Create creates a new pending task, persists it and emits task.created.
func (s *TaskService) Create(ctx context.Context, p CreateTaskParams) (domain.Task, error) {
	now := s.now().UTC()
	task := domain.Task{
		ID:          s.newID(),
		Title:       p.Title,
		Description: p.Description,
		Status:      domain.TaskStatusPending,
		Priority:    p.Priority,
		DueAt:       p.DueAt,
		Source:      p.Source,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.tasks.Create(ctx, task); err != nil {
		return domain.Task{}, err
	}
	s.emit(ctx, domain.EventTaskCreated, task.ID)
	return task, nil
}

// GetByID returns a single task.
func (s *TaskService) GetByID(ctx context.Context, id string) (domain.Task, error) {
	return s.tasks.GetByID(ctx, id)
}

// List returns all tasks.
func (s *TaskService) List(ctx context.Context) ([]domain.Task, error) {
	return s.tasks.List(ctx)
}

// Update applies optional field changes to an existing task, bumps
// UpdatedAt, persists and emits task.updated.
func (s *TaskService) Update(ctx context.Context, id string, p UpdateTaskParams) (domain.Task, error) {
	task, err := s.tasks.GetByID(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}

	if p.Title != nil {
		task.Title = *p.Title
	}
	if p.Description != nil {
		task.Description = *p.Description
	}
	if p.Priority != nil {
		task.Priority = *p.Priority
	}
	if p.DueAt != nil {
		task.DueAt = p.DueAt
	}
	if p.Source != nil {
		task.Source = *p.Source
	}

	task.UpdatedAt = s.now().UTC()
	if err := s.tasks.Update(ctx, task); err != nil {
		return domain.Task{}, err
	}

	// If DueAt changed, invalidate the task's time-derived triggers (before_due /
	// after_due) so the Scheduler recalculates their NextFireAt from the new due
	// date. This deliberately does not duplicate scheduling math: it only nulls
	// the cached deadline. Best-effort so a repository failure does not block the
	// task update (mirrors emit's resilience). DueAt can only be set/changed in
	// V1 (clearing is unsupported), so a non-nil param always means a change.
	if p.DueAt != nil {
		_ = s.triggers.ClearDerivedNextFireAt(ctx, task.ID)
		// A DueAt change may move the effective next deadline, so hint the Scheduler
		// to rescan — but only AFTER the task update and trigger invalidation are
		// persisted, so the rescan sees the committed write.
		s.wake()
	}

	s.emit(ctx, domain.EventTaskUpdated, task.ID)
	return task, nil
}

// Complete marks a non-cancelled task as completed and emits task.completed.
func (s *TaskService) Complete(ctx context.Context, id string) (domain.Task, error) {
	task, err := s.tasks.GetByID(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if task.Status == domain.TaskStatusCancelled {
		return domain.Task{}, fmt.Errorf("%w: %s", ErrCannotComplete, id)
	}

	task.Status = domain.TaskStatusCompleted
	task.UpdatedAt = s.now().UTC()
	if err := s.tasks.Update(ctx, task); err != nil {
		return domain.Task{}, err
	}
	s.emit(ctx, domain.EventTaskCompleted, task.ID)
	return task, nil
}

// Cancel marks a non-completed task as cancelled and emits task.cancelled.
func (s *TaskService) Cancel(ctx context.Context, id string) (domain.Task, error) {
	task, err := s.tasks.GetByID(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if task.Status == domain.TaskStatusCompleted {
		return domain.Task{}, fmt.Errorf("%w: %s", ErrCannotCancel, id)
	}

	task.Status = domain.TaskStatusCancelled
	task.UpdatedAt = s.now().UTC()
	if err := s.tasks.Update(ctx, task); err != nil {
		return domain.Task{}, err
	}
	s.emit(ctx, domain.EventTaskCancelled, task.ID)
	return task, nil
}

// Delete removes a task by ID.
func (s *TaskService) Delete(ctx context.Context, id string) error {
	return s.tasks.Delete(ctx, id)
}

// emit records a domain event. A failure in the event store is logged/ignored
// so that a broken event persistence never blocks the underlying task
// operation. A general event bus is out of scope for V1.
func (s *TaskService) emit(ctx context.Context, eventType domain.EventType, taskID string) {
	_ = s.events.Save(ctx, domain.Event{
		ID:        s.newID(),
		Type:      eventType,
		Payload:   map[string]any{"task_id": taskID},
		CreatedAt: s.now().UTC(),
	})
}

// wake hints the Scheduler to rescan. It is a no-op when no Rescheduler is wired
// (the Scheduler re-reads SQLite on its next normal cycle regardless).
func (s *TaskService) wake() {
	if s.rescheduler != nil {
		s.rescheduler.Wake()
	}
}