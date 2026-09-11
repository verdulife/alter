package application

import (
	"context"

	"github.com/verdu/alter/internal/domain"
)

// AgentFlow connects an Agent with the CapabilityFlow layer, completing the
// Pi → classify → capabilities pipeline:
//
//	Agent.Execute(request)
//	        ↓
//	CapabilityFlow.Process(AgentResult.Response)
//	        ↓
//	  Conversation → FlowResult{Response: text}
//	  Plan         → FlowResult{Results: [...]}
//	  InvalidPlan  → error
//
// Agent errors are propagated before CapabilityFlow sees anything.
type AgentFlow struct {
	agent domain.Agent
	flow  *CapabilityFlow
}

// NewAgentFlow creates an AgentFlow. Both agent and flow must be non-nil.
func NewAgentFlow(agent domain.Agent, flow *CapabilityFlow) *AgentFlow {
	if agent == nil {
		panic("application: NewAgentFlow with nil Agent")
	}
	if flow == nil {
		panic("application: NewAgentFlow with nil CapabilityFlow")
	}
	return &AgentFlow{agent: agent, flow: flow}
}

// Execute runs the Agent once, passes its response through CapabilityFlow,
// and returns the result. Agent errors are returned immediately without
// touching CapabilityFlow. CapabilityFlow errors propagate unchanged.
func (f *AgentFlow) Execute(ctx context.Context, request domain.AgentRequest) (FlowResult, error) {
	result, err := f.agent.Execute(ctx, request)
	if err != nil {
		return FlowResult{}, err
	}
	return f.flow.Process(ctx, result.Response)
}
