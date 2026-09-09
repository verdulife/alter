package main

import (
	"context"
	"time"

	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/service"
)

// commandService adapts the application services to the telegram.CommandService
// contract used by telegram.Handle. It is the runtime composition seam: it is
// the only place that turns /nueva and /recordar messages into service calls.
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
