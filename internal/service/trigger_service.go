package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// ErrTriggerTaskNotFound is returned when creating a trigger whose TaskID
// does not reference an existing task.
var ErrTriggerTaskNotFound = errors.New("task not found for trigger")

// Note (possible future evolution): changes to triggers (created/updated,
// enabled/disabled) are not represented as domain events yet. If auditing or
// notifications require them, new EventType values such as "trigger.created"
// and "trigger.disabled" could be added to domain/event.go later. Deliberately
// not added now to keep the event surface minimal.

// CreateTriggerParams carries the user-supplied fields for creating a trigger.
// ID and CreatedAt are managed by the service; Enabled must be explicit.
type CreateTriggerParams struct {
	TaskID  string
	Type    domain.TriggerType
	Value   string
	Enabled bool
}

// UpdateTriggerParams carries optional field changes for Update. A nil pointer
// leaves the corresponding field unchanged. TaskID, Enabled and CreatedAt are
// invariant through Update (use Enable/Disable for the enabled flag).
type UpdateTriggerParams struct {
	Type  *domain.TriggerType
	Value *string
}

// TriggerService encapsulates application operations over domain.Trigger.
// It depends only on domain interfaces; it never touches concrete storage.
type TriggerService struct {
	triggers domain.TriggerRepository
	tasks    domain.TaskRepository
	now      func() time.Time
	newID    func() string
}

// NewTriggerService builds a TriggerService with production defaults.
func NewTriggerService(triggers domain.TriggerRepository, tasks domain.TaskRepository) *TriggerService {
	return &TriggerService{
		triggers: triggers,
		tasks:    tasks,
		now:      time.Now,
		newID:    newLocalID,
	}
}

// Create validates that the referenced task exists, then persists a new
// trigger with an explicitly provided Enabled flag and a service-assigned
// CreatedAt.
func (s *TriggerService) Create(ctx context.Context, p CreateTriggerParams) (domain.Trigger, error) {
	if _, err := s.tasks.GetByID(ctx, p.TaskID); err != nil {
		return domain.Trigger{}, fmt.Errorf("%w: %s", ErrTriggerTaskNotFound, p.TaskID)
	}

	trigger := domain.Trigger{
		ID:        s.newID(),
		TaskID:    p.TaskID,
		Type:      p.Type,
		Value:     p.Value,
		Enabled:   p.Enabled,
		CreatedAt: s.now().UTC(),
	}

	if err := s.triggers.Create(ctx, trigger); err != nil {
		return domain.Trigger{}, err
	}
	return trigger, nil
}

// GetByID returns a single trigger.
func (s *TriggerService) GetByID(ctx context.Context, id string) (domain.Trigger, error) {
	return s.triggers.GetByID(ctx, id)
}

// GetByTaskID returns all triggers associated with a task.
func (s *TriggerService) GetByTaskID(ctx context.Context, taskID string) ([]domain.Trigger, error) {
	return s.triggers.GetByTaskID(ctx, taskID)
}

// Update applies optional Type/Value changes to an existing trigger and
// persists it. Enabled, TaskID, NextFireAt, LastFiredAt and CreatedAt are left
// untouched: the service fetches the full trigger and re-saves it unchanged
// except for the requested fields, so execution bookkeeping is preserved.
func (s *TriggerService) Update(ctx context.Context, id string, p UpdateTriggerParams) (domain.Trigger, error) {
	trigger, err := s.triggers.GetByID(ctx, id)
	if err != nil {
		return domain.Trigger{}, err
	}

	if p.Type != nil {
		trigger.Type = *p.Type
	}
	if p.Value != nil {
		trigger.Value = *p.Value
	}

	if err := s.triggers.Update(ctx, trigger); err != nil {
		return domain.Trigger{}, err
	}
	return trigger, nil
}

// Note: NextFireAt / LastFiredAt are preserved on Enable, Disable and Update
// because these operations fetch the full trigger and re-save it as-is except
// for the fields they change. They are only recomputed by the future
// Scheduler upon firing.

// Enable sets Enabled to true and persists.
func (s *TriggerService) Enable(ctx context.Context, id string) (domain.Trigger, error) {
	trigger, err := s.triggers.GetByID(ctx, id)
	if err != nil {
		return domain.Trigger{}, err
	}

	trigger.Enabled = true
	if err := s.triggers.Update(ctx, trigger); err != nil {
		return domain.Trigger{}, err
	}
	return trigger, nil
}

// Disable sets Enabled to false and persists.
func (s *TriggerService) Disable(ctx context.Context, id string) (domain.Trigger, error) {
	trigger, err := s.triggers.GetByID(ctx, id)
	if err != nil {
		return domain.Trigger{}, err
	}

	trigger.Enabled = false
	if err := s.triggers.Update(ctx, trigger); err != nil {
		return domain.Trigger{}, err
	}
	return trigger, nil
}

// Delete removes a trigger by ID.
func (s *TriggerService) Delete(ctx context.Context, id string) error {
	return s.triggers.Delete(ctx, id)
}