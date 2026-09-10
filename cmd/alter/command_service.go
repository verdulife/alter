package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/service"
)

// commandService adapts the application services to the telegram.CommandService
// contract used by telegram.Handle. It is the runtime composition seam: it is
// the only place that turns /nueva, /recordar, /listar, /completar and /cancelar
// messages into service calls.
type commandService struct {
	tasks    *service.TaskService
	triggers *service.TriggerService
}

var _ telegram.CommandService = commandService{}

// CreateTask creates a plain pending task via the task service.
func (c commandService) CreateTask(ctx context.Context, title string) (domain.Task, error) {
	return c.tasks.Create(ctx, service.CreateTaskParams{Title: title})
}

// CreateReminder implements the /recordar semantics documented on
// telegram.CommandService: it creates a Task plus its one-shot "at" Trigger
// scheduled `in` from now. Composition happens here (no service change), and
// both services persist before returning: the Scheduler receives the Wake hint
// from TriggerService.Create only after the write is committed.
func (c commandService) CreateReminder(ctx context.Context, title string, in time.Duration) (domain.Task, error) {
	task, err := c.tasks.Create(ctx, service.CreateTaskParams{Title: title})
	if err != nil {
		return domain.Task{}, err
	}
	_, err = c.triggers.Create(ctx, service.CreateTriggerParams{
		TaskID:  task.ID,
		Type:    domain.TriggerTypeAt,
		Value:   time.Now().Add(in).UTC().Format(time.RFC3339),
		Enabled: true,
	})
	if err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

// ListPendingTasks returns all tasks (the caller filters to pending).
func (c commandService) ListPendingTasks(ctx context.Context) ([]domain.Task, error) {
	return c.tasks.List(ctx)
}

// CompleteTaskByRef finds a pending task by textual reference and completes it.
// It returns the resolved task and a reply string.
func (c commandService) CompleteTaskByRef(ctx context.Context, ref string) (domain.Task, string, error) {
	task, err := c.resolveTask(ctx, ref)
	if err != nil {
		return domain.Task{}, err.Error(), err
	}
	if _, err := c.tasks.Complete(ctx, task.ID); err != nil {
		return domain.Task{}, "No pude completar la tarea: " + err.Error(), err
	}
	return task, fmt.Sprintf("Listo, completé «%s» ✓", task.Title), nil
}

// CancelTaskByRef finds a pending task by textual reference and cancels it.
// It returns the resolved task and a reply string.
func (c commandService) CancelTaskByRef(ctx context.Context, ref string) (domain.Task, string, error) {
	task, err := c.resolveTask(ctx, ref)
	if err != nil {
		return domain.Task{}, err.Error(), err
	}
	if _, err := c.tasks.Cancel(ctx, task.ID); err != nil {
		return domain.Task{}, "No pude cancelar la tarea: " + err.Error(), err
	}
	return task, fmt.Sprintf("Cancelé «%s» ✓", task.Title), nil
}

// resolveTask finds a single pending task matching the given reference by
// case-insensitive substring match. Returns an error if 0 or 2+ tasks match.
func (c commandService) resolveTask(ctx context.Context, ref string) (domain.Task, error) {
	tasks, err := c.tasks.List(ctx)
	if err != nil {
		return domain.Task{}, fmt.Errorf("No pude obtener las tareas: %w", err)
	}

	refLower := strings.ToLower(ref)
	var matches []domain.Task
	for _, t := range tasks {
		if t.Status == domain.TaskStatusPending && strings.Contains(strings.ToLower(t.Title), refLower) {
			matches = append(matches, t)
		}
	}

	switch len(matches) {
	case 0:
		return domain.Task{}, fmt.Errorf("No encontré ninguna tarea pendiente que coincida con «%s».", ref)
	case 1:
		return matches[0], nil
	default:
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Encontré varias tareas que coinciden con «%s»:\n", ref))
		for i, t := range matches {
			sb.WriteString(fmt.Sprintf("%d) %s\n", i+1, t.Title))
		}
		sb.WriteString("¿Cuál?")
		return domain.Task{}, errors.New(sb.String())
	}
}
