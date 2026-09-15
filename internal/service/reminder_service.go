package service

import (
	"context"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// ReminderService encapsulates the Task + Trigger composition that every
// reminder route (natural language one-shot, natural language recurring,
// /recordar command) shares: create the Task first, then create its Trigger.
//
// Deliberate semantics (unchanged from the previous inline composition):
//   - The Task is created and persisted BEFORE the Trigger. If Trigger creation
//     fails the Task remains (an orphan) and the error is propagated — there is
//     NO rollback and NO transaction. Callers can detect the partial outcome:
//     when Create returns a non-empty task together with an error, the Task was
//     persisted but the Trigger was not.
//   - Validation authority stays in the services this composes: TaskService
//     owns task invariants and TriggerService owns trigger/recurrence
//     validation (domain.ParseRecurrence via TriggerService.Create). No
//     validation rule is duplicated here.
//   - The Scheduler rescan hint is owned by TaskService/TriggerService (each
//     wakes when it persists a change), so this composition adds no extra one.
type ReminderService struct {
	tasks    *TaskService
	triggers *TriggerService
}

// NewReminderService builds the composition over the shared application
// services. Both must be non-nil.
func NewReminderService(tasks *TaskService, triggers *TriggerService) *ReminderService {
	return &ReminderService{tasks: tasks, triggers: triggers}
}

// CreateOneShot creates a pending task with the given Source and a one-shot
// "at" trigger scheduled at `at`, enabled. It composes TaskService.Create and
// TriggerService.Create in that order and propagates the first error unchanged
// (task failure: no trigger is attempted; trigger failure: the task stays, see
// the type doc).
func (s *ReminderService) CreateOneShot(ctx context.Context, title, source string, at time.Time) (task domain.Task, err error) {
	task, err = s.tasks.Create(ctx, CreateTaskParams{
		Title:  title,
		Source: source,
	})
	if err != nil {
		return domain.Task{}, err
	}

	if _, err = s.triggers.Create(ctx, CreateTriggerParams{
		TaskID:  task.ID,
		Type:    domain.TriggerTypeAt,
		Value:   at.UTC().Format(time.RFC3339),
		Enabled: true,
	}); err != nil {
		return task, err
	}
	return task, nil
}

// CreateRecurring creates a pending task with the given Source and a
// TriggerTypeRecurring trigger whose Value is the canonical recurrence JSON
// (validated by TriggerService.Create / domain.ParseRecurrence — the single
// authority for recurrence rules). It composes the two services in the same
// order and with the same partial-failure semantics as CreateOneShot.
func (s *ReminderService) CreateRecurring(ctx context.Context, title, source, recurrenceJSON string) (task domain.Task, err error) {
	task, err = s.tasks.Create(ctx, CreateTaskParams{
		Title:  title,
		Source: source,
	})
	if err != nil {
		return domain.Task{}, err
	}

	if _, err = s.triggers.Create(ctx, CreateTriggerParams{
		TaskID:  task.ID,
		Type:    domain.TriggerTypeRecurring,
		Value:   recurrenceJSON,
		Enabled: true,
	}); err != nil {
		return task, err
	}
	return task, nil
}
