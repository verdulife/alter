package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/service"
)

// Sentinel errors for complete_task capability.
var (
	// ErrTaskNotFound is returned when no pending task matches the given reference.
	ErrTaskNotFound = errors.New("task not found")
	// ErrAmbiguousTaskRef is returned when multiple pending tasks match the reference.
	ErrAmbiguousTaskRef = errors.New("ambiguous task reference")
)

// AmbiguousTaskRefError carries structured ambiguity data when a textual
// reference resolves to multiple pending tasks. Callers can type-assert to
// obtain the list of matching titles for clarification prompts.
type AmbiguousTaskRefError struct {
	Ref     string
	Matches []string
}

func (e *AmbiguousTaskRefError) Error() string {
	return fmt.Sprintf("ambiguous task reference «%s»: %d candidates", e.Ref, len(e.Matches))
}

func (e *AmbiguousTaskRefError) Unwrap() error {
	return ErrAmbiguousTaskRef
}

// completeTaskArgs is the JSON-validated input for the complete_task capability.
type completeTaskArgs struct {
	TaskRef string `json:"task_ref"`
}

// CompleteTaskResult is the Data payload produced by the complete_task capability.
// It reuses the existing TaskView stable external representation.
type CompleteTaskResult struct {
	Task TaskView `json:"task"`
}

// CompleteTaskHandler executes the complete_task capability by resolving a
// textual task reference to a single pending task and then delegating to
// TaskService.Complete.
type CompleteTaskHandler struct {
	taskService *service.TaskService
}

// NewCompleteTaskHandler creates a handler backed by the given TaskService. It
// panics if ts is nil.
func NewCompleteTaskHandler(ts *service.TaskService) *CompleteTaskHandler {
	if ts == nil {
		panic("capability: NewCompleteTaskHandler with nil TaskService")
	}
	return &CompleteTaskHandler{taskService: ts}
}

// Execute resolves a task reference to a single pending task, then marks it as
// completed. Args have already been validated against the capability schema before
// this method is called.
func (h *CompleteTaskHandler) Execute(ctx context.Context, args json.RawMessage) (CapabilityResult, error) {
	var in completeTaskArgs
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

	completed, err := h.taskService.Complete(ctx, task.ID)
	if err != nil {
		return CapabilityResult{}, err
	}

	return CapabilityResult{Data: CompleteTaskResult{Task: toTaskView(completed)}}, nil
}

// resolveTaskByRef finds a single pending task matching the given textual
// reference. Resolution rules live in service.MatchTaskRef (the shared
// task_ref resolution); this method only maps its classification to the
// capability error contract:
//
//   - 0 matches → ErrTaskNotFound
//   - 1 match → returns the task
//   - 2+ matches → AmbiguousTaskRefError (wraps ErrAmbiguousTaskRef)
func (h *CompleteTaskHandler) resolveTaskByRef(ctx context.Context, ref string) (domain.Task, error) {
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
