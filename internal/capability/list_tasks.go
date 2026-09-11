package capability

import (
	"context"
	"encoding/json"
	"time"

	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/service"
)

// TaskView is the stable external representation of a task returned by the
// list_tasks capability. It deliberately does not serialize domain.Task
// directly: the JSON shape is a public contract, so it is explicit declaration
// rather than accidental. Source is an internal detail and is not exposed.
// Timestamps are RFC3339 strings; DueAt is omitted when nil.
type TaskView struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	DueAt       *string `json:"due_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// ListTasksResult is the Data payload produced by the list_tasks capability.
type ListTasksResult struct {
	Tasks []TaskView `json:"tasks"`
}

// ListTasksHandler executes the list_tasks capability by delegating to
// TaskService.List. It reuses existing listing logic with zero duplication; the
// only added logic is the mapping from domain.Task to TaskView.
type ListTasksHandler struct {
	taskService *service.TaskService
}

// NewListTasksHandler creates a handler backed by the given TaskService. It
// panics if ts is nil.
func NewListTasksHandler(ts *service.TaskService) *ListTasksHandler {
	if ts == nil {
		panic("capability: NewListTasksHandler with nil TaskService")
	}
	return &ListTasksHandler{taskService: ts}
}

// Execute lists all tasks via TaskService and returns them as a stable
// ListTasksResult. Args have already been validated by the dispatcher before
// this method is called.
func (h *ListTasksHandler) Execute(ctx context.Context, _ json.RawMessage) (CapabilityResult, error) {
	tasks, err := h.taskService.List(ctx)
	if err != nil {
		return CapabilityResult{}, err
	}
	views := make([]TaskView, len(tasks))
	for i, t := range tasks {
		views[i] = toTaskView(t)
	}
	return CapabilityResult{Data: ListTasksResult{Tasks: views}}, nil
}

// toTaskView maps a domain.Task to its stable external representation.
func toTaskView(t domain.Task) TaskView {
	view := TaskView{
		ID:          t.ID,
		Title:       t.Title,
		Description: t.Description,
		Status:      string(t.Status),
		Priority:    string(t.Priority),
		CreatedAt:   t.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   t.UpdatedAt.Format(time.RFC3339),
	}
	if t.DueAt != nil {
		due := t.DueAt.Format(time.RFC3339)
		view.DueAt = &due
	}
	return view
}
