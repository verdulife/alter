package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/service"
)

// cancelTaskArgs is the JSON-validated input for the cancel_task capability.
type cancelTaskArgs struct {
	TaskRef string `json:"task_ref"`
}

// CancelTaskResult is the Data payload produced by the cancel_task capability.
// It reuses the existing TaskView stable external representation.
type CancelTaskResult struct {
	Task TaskView `json:"task"`
}

// CancelTaskHandler executes the cancel_task capability by resolving a
// textual task reference to a single pending task and then delegating to
// TaskService.Cancel.
type CancelTaskHandler struct {
	taskService *service.TaskService
}

// NewCancelTaskHandler creates a handler backed by the given TaskService. It
// panics if ts is nil.
func NewCancelTaskHandler(ts *service.TaskService) *CancelTaskHandler {
	if ts == nil {
		panic("capability: NewCancelTaskHandler with nil TaskService")
	}
	return &CancelTaskHandler{taskService: ts}
}

// Execute resolves a task reference to a single pending task, then cancels it.
// Args have already been validated against the capability schema before
// this method is called.
func (h *CancelTaskHandler) Execute(ctx context.Context, args json.RawMessage) (CapabilityResult, error) {
	var in cancelTaskArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return CapabilityResult{}, fmt.Errorf("%w: unparseable args", ErrInvalidArgs)
	}
	if strings.TrimSpace(in.TaskRef) == "" {
		return CapabilityResult{}, fmt.Errorf("%w: task_ref is required", ErrInvalidArgs)
	}

	task, err := h.resolveTaskByRef(ctx, in.TaskRef)
	if err != nil {
		return CapabilityResult{}, err
	}

	cancelled, err := h.taskService.Cancel(ctx, task.ID)
	if err != nil {
		return CapabilityResult{}, err
	}

	return CapabilityResult{Data: CancelTaskResult{Task: toTaskView(cancelled)}}, nil
}

// resolveTaskByRef finds a single pending task matching the given textual
// reference. Resolution rules live in service.MatchTaskRef (the shared
// task_ref resolution); this method only maps its classification to the
// capability error contract:
//
//   - 0 matches → ErrTaskNotFound
//   - 1 match → returns the task
//   - 2+ matches → AmbiguousTaskRefError (wraps ErrAmbiguousTaskRef)
func (h *CancelTaskHandler) resolveTaskByRef(ctx context.Context, ref string) (domain.Task, error) {
	tasks, err := h.taskService.List(ctx)
	if err != nil {
		return domain.Task{}, err
	}

	match := service.MatchTaskRef(tasks, strings.TrimSpace(ref))
	switch {
	case match.IsNotFound():
		return domain.Task{}, fmt.Errorf("%w: no pending task matches «%s»", ErrTaskNotFound, ref)
	case match.IsAmbiguous():
		titles := make([]string, len(match.Matches))
		for i, t := range match.Matches {
			titles[i] = t.Title
		}
		return domain.Task{}, &AmbiguousTaskRefError{Ref: ref, Matches: titles}
	default:
		task, _ := match.ResolvedTask()
		return task, nil
	}
}
