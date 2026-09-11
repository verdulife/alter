package capability

import (
	"context"
)

// PlanExecutor composes ParsePlan and Dispatcher.ExecutePlan into a single
// entry point: raw JSON from the planner becomes parsed-and-executed plan
// in one call. It contains no validation or dispatch logic of its own.
type PlanExecutor struct {
	dispatcher *Dispatcher
}

// NewPlanExecutor creates a PlanExecutor backed by the given Dispatcher.
// It panics if dispatcher is nil.
func NewPlanExecutor(d *Dispatcher) *PlanExecutor {
	if d == nil {
		panic("capability: NewPlanExecutor with nil Dispatcher")
	}
	return &PlanExecutor{dispatcher: d}
}

// Execute parses raw JSON into a Plan and executes it. Parse errors are
// returned without executing any capability; dispatch errors propagate
// unchanged. A plan with no calls returns an empty non-nil result slice.
func (e *PlanExecutor) Execute(ctx context.Context, data []byte) ([]CapabilityResult, error) {
	plan, err := ParsePlan(data)
	if err != nil {
		return nil, err
	}
	return e.dispatcher.ExecutePlan(ctx, plan)
}
