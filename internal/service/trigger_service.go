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

// ErrInvalidRecurrenceValue is returned when creating a TriggerTypeRecurring
// trigger whose Value is not a valid canonical recurrence spec (B3 S4). It is
// an additional integrity barrier: the domain's ParseRecurrence is the single
// authority for recurrence validation (no duplicated rules here).
var ErrInvalidRecurrenceValue = errors.New("invalid recurrence value")

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
	triggers    domain.TriggerRepository
	tasks       domain.TaskRepository
	now         func() time.Time
	newID       func() string
	rescheduler domain.Rescheduler
}

// TriggerOption configures a TriggerService (dependency injection for testing
// and the optional Scheduler hint).
type TriggerOption func(*TriggerService)

// WithTriggerRescheduler wires the optional inbound port that hints the Scheduler
// to rescan after a persisted trigger change. It is optional: without it the
// service still works (the Scheduler re-reads SQLite on its next normal cycle);
// Wake only accelerates re-evaluation.
func WithTriggerRescheduler(r domain.Rescheduler) TriggerOption {
	return func(s *TriggerService) { s.rescheduler = r }
}

// NewTriggerService builds a TriggerService with production defaults.
func NewTriggerService(triggers domain.TriggerRepository, tasks domain.TaskRepository, opts ...TriggerOption) *TriggerService {
	s := &TriggerService{
		triggers: triggers,
		tasks:    tasks,
		now:      time.Now,
		newID:    newLocalID,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Create validates that the referenced task exists, then persists a new
// trigger with an explicitly provided Enabled flag and a service-assigned
// CreatedAt. A newly created (and enabled) trigger may be immediately due, so it
// hints the Scheduler to rescan after the write is persisted.
func (s *TriggerService) Create(ctx context.Context, p CreateTriggerParams) (domain.Trigger, error) {
	if _, err := s.tasks.GetByID(ctx, p.TaskID); err != nil {
		return domain.Trigger{}, fmt.Errorf("%w: %s", ErrTriggerTaskNotFound, p.TaskID)
	}

	// B3 S4 integrity barrier: a recurring trigger must carry a valid canonical
	// recurrence spec, or it is rejected before persisting. Non-recurring types
	// keep their exact previous behavior (no validation of Value).
	if p.Type == domain.TriggerTypeRecurring {
		if _, err := domain.ParseRecurrence(p.Value); err != nil {
			return domain.Trigger{}, fmt.Errorf("%w: %v", ErrInvalidRecurrenceValue, err)
		}
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
	s.wake()
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
// persists it. Enabled, TaskID and CreatedAt are left untouched. NextFireAt is a
// value derived from Type/Value/Task.DueAt, so changing Type and/or Value
// invalidates it (set to nil) — the Scheduler recomputes it later via
// CalculateNextFireAt. LastFiredAt is preserved (it records an actual past run,
// independent of scheduling).
func (s *TriggerService) Update(ctx context.Context, id string, p UpdateTriggerParams) (domain.Trigger, error) {
	trigger, err := s.triggers.GetByID(ctx, id)
	if err != nil {
		return domain.Trigger{}, err
	}

	changed := false
	if p.Type != nil {
		trigger.Type = *p.Type
		changed = true
	}
	if p.Value != nil {
		trigger.Value = *p.Value
		changed = true
	}

	if changed {
		// Type/Value changed => the cached NextFireAt is stale (it was derived from
		// the old Type/Value/DueAt). Invalidate it so the Scheduler re-arms from the
		// new inputs. A pending RetryAt is stale too: it belonged to the old
		// scheduling (a failed action on the old deadline) and must NOT carry over to
		// the new one, or the retry could fire before the re-armed deadine. LastFiredAt
		// and Enabled are preserved.
		trigger.NextFireAt = nil
		trigger.RetryAt = nil
	}

	if err := s.triggers.Update(ctx, trigger); err != nil {
		return domain.Trigger{}, err
	}
	if changed {
		// A Type/Value change may move the effective next deadline (earlier or
		// later), so hint the Scheduler to rescan after the write is persisted.
		s.wake()
	}
	return trigger, nil
}

// Note: NextFireAt / LastFiredAt are preserved on Enable, Disable and Update
// because these operations fetch the full trigger and re-save it as-is except
// for the fields they change. They are only recomputed by the future
// Scheduler upon firing.

// Enable sets Enabled to true and persists. Enabling may make an unarmed trigger
// schedulable again, so it hints the Scheduler to rescan after the write.
func (s *TriggerService) Enable(ctx context.Context, id string) (domain.Trigger, error) {
	trigger, err := s.triggers.GetByID(ctx, id)
	if err != nil {
		return domain.Trigger{}, err
	}

	trigger.Enabled = true
	if err := s.triggers.Update(ctx, trigger); err != nil {
		return domain.Trigger{}, err
	}
	s.wake()
	return trigger, nil
}

// Disable sets Enabled to false and persists. Disabling may remove a trigger from
// the active set, so it hints the Scheduler to rescan after the write.
func (s *TriggerService) Disable(ctx context.Context, id string) (domain.Trigger, error) {
	trigger, err := s.triggers.GetByID(ctx, id)
	if err != nil {
		return domain.Trigger{}, err
	}

	trigger.Enabled = false
	if err := s.triggers.Update(ctx, trigger); err != nil {
		return domain.Trigger{}, err
	}
	s.wake()
	return trigger, nil
}

// Delete removes a trigger by ID. Removing a trigger may change the next deadline,
// so it hints the Scheduler to rescan after the write.
func (s *TriggerService) Delete(ctx context.Context, id string) error {
	if err := s.triggers.Delete(ctx, id); err != nil {
		return err
	}
	s.wake()
	return nil
}

// wake hints the Scheduler to rescan. It is a no-op when no Rescheduler is wired
// (the Scheduler re-reads SQLite on its next normal cycle regardless).
func (s *TriggerService) wake() {
	if s.rescheduler != nil {
		s.rescheduler.Wake()
	}
}
