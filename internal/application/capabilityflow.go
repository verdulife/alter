// Package application hosts orchestrating application layers on top of the
// domain and capability layers.
package application

import (
	"context"

	"github.com/verdu/alter/internal/capability"
)

// FlowResult is the outcome of processing a Pi response through the
// capability flow.
type FlowResult struct {
	// Kind reports the classification: ResponseConversation or ResponsePlan.
	// ResponseInvalidPlan never reaches a FlowResult: it surfaces as an error.
	Kind capability.ResponseKind
	// Response carries the original, unmodified text for a conversation.
	Response string
	// Results carries the executed capability results for a plan. Non-nil
	// (possibly empty) whenever a plan was executed.
	Results []capability.CapabilityResult
}

// CapabilityFlow connects the Pi response with the capability layer:
//
//	AgentResult.Response → ClassifyResponse
//	  ├── Conversation → FlowResult{Kind: conversation, Response: text}
//	  ├── Plan         → PlanExecutor → FlowResult{Kind: plan, Results: [...]}
//	  └── InvalidPlan  → error (errors.Is → capability.ErrInvalidPlan)
//
// It owns no classification, parsing or execution logic: ClassifyResponse and
// PlanExecutor are reused as-is. Conversations and invalid plans never execute
// anything.
type CapabilityFlow struct {
	executor *capability.PlanExecutor
}

// NewCapabilityFlow creates a CapabilityFlow backed by the given PlanExecutor.
// It panics if executor is nil.
func NewCapabilityFlow(executor *capability.PlanExecutor) *CapabilityFlow {
	if executor == nil {
		panic("application: NewCapabilityFlow with nil PlanExecutor")
	}
	return &CapabilityFlow{executor: executor}
}

// Process classifies the Pi response and, for plans, executes it via the
// PlanExecutor, returning the parsed results. Errors propagate unchanged:
// ErrInvalidPlan (failed classification), ErrUnknownCapability,
// ErrInvalidArgs and ErrExecutionFailed (failed execution) stay
// distinguishable with errors.Is. Nothing is executed for conversations or
// invalid plans.
func (f *CapabilityFlow) Process(ctx context.Context, response string) (FlowResult, error) {
	cls, err := capability.ClassifyResponse(response)
	if err != nil {
		// InvalidPlan: the response pretends to be a plan but is not.
		return FlowResult{}, err
	}

	switch cls.Kind {
	case capability.ResponseConversation:
		return FlowResult{Kind: cls.Kind, Response: cls.Text}, nil
	case capability.ResponsePlan:
		results, err := f.executor.Execute(ctx, []byte(response))
		if err != nil {
			return FlowResult{}, err
		}
		return FlowResult{Kind: cls.Kind, Results: results}, nil
	default:
		return FlowResult{}, capability.ErrInvalidPlan
	}
}
