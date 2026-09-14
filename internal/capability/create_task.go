package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/service"
)

// createTaskArgs is the JSON-validated input for the create_task capability.
// Title is required; all other fields are optional with sensible defaults.
type createTaskArgs struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Priority    string  `json:"priority"`
	DueAt       *string `json:"due_at"`
}

// CreateTaskResult is the Data payload produced by the create_task capability.
type CreateTaskResult struct {
	Task TaskView `json:"task"`
}

// CreateTaskHandler executes the create_task capability by delegating to
// TaskService.Create. It reuses the existing task creation logic with zero
// duplication; the only added logic is the mapping from capability args to
// service params and from domain.Task to TaskView.
type CreateTaskHandler struct {
	taskService *service.TaskService
}

// NewCreateTaskHandler creates a handler backed by the given TaskService. It
// panics if ts is nil.
func NewCreateTaskHandler(ts *service.TaskService) *CreateTaskHandler {
	if ts == nil {
		panic("capability: NewCreateTaskHandler with nil TaskService")
	}
	return &CreateTaskHandler{taskService: ts}
}

// Execute creates a new task via TaskService and returns the created task as a
// stable CreateTaskResult. Args have already been validated by the dispatcher
// before this method is called.
func (h *CreateTaskHandler) Execute(ctx context.Context, args json.RawMessage) (CapabilityResult, error) {
	var in createTaskArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return CapabilityResult{}, fmt.Errorf("%w: unparseable args", ErrInvalidArgs)
	}

	params := service.CreateTaskParams{
		Title:       in.Title,
		Description: in.Description,
		Priority:    parsePriority(in.Priority),
		Source:      "capability:create_task",
	}

	if in.DueAt != nil {
		due, err := time.Parse(time.RFC3339, *in.DueAt)
		if err != nil {
			return CapabilityResult{}, fmt.Errorf("%w: invalid due_at format, expected RFC3339", ErrInvalidArgs)
		}
		params.DueAt = &due
	}

	task, err := h.taskService.Create(ctx, params)
	if err != nil {
		return CapabilityResult{}, err
	}

	return CapabilityResult{Data: CreateTaskResult{Task: toTaskView(task)}}, nil
}

// parsePriority maps a string priority to the domain enum. Unknown values
// default to medium, matching the service's implicit default.
func parsePriority(s string) domain.TaskPriority {
	switch domain.TaskPriority(s) {
	case domain.TaskPriorityLow, domain.TaskPriorityMedium, domain.TaskPriorityHigh, domain.TaskPriorityUrgent:
		return domain.TaskPriority(s)
	default:
		return domain.TaskPriorityMedium
	}
}
