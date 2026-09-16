package telegram

import (
	"context"

	"github.com/verdu/alter/internal/application"
	"github.com/verdu/alter/internal/capability"
	"github.com/verdu/alter/internal/domain"
)

// AgentFlowHandler is the inbound free-text route that puts the AgentFlow
// capability pipeline (Pi Agent + CapabilityFlow) in front of Telegram messages
// whenever the Pi Agent is enabled:
//
//	free text
//	  → AgentFlow.Execute (Pi decides conversation vs Plan JSON)
//	  ├── Plan         → executed capability results (list_tasks, create_task,
//	│                    complete_task, cancel_task, create_reminder) are delivered
//	  └── Conversation → Pi's conversational reply is delivered
//
// Slash commands never reach this handler: telegram.Handle routes them directly
// to the CommandService before consulting the natural handler.
type AgentFlowHandler struct {
	flow *application.AgentFlow
}

// NewAgentFlowHandler builds the handler. flow must be non-nil.
func NewAgentFlowHandler(flow *application.AgentFlow) *AgentFlowHandler {
	if flow == nil {
		panic("telegram: NewAgentFlowHandler with nil AgentFlow")
	}
	return &AgentFlowHandler{flow: flow}
}

// Handle implements the telegram.NaturalHandler seam for free text. The reply
// it returns is the user-facing text for Telegram; the caller owns HTML escape.
func (h *AgentFlowHandler) Handle(ctx context.Context, text string) (string, error) {
	result, err := h.flow.Execute(ctx, domain.AgentRequest{
		Instruction: freeTextInstruction(text),
	})
	if err != nil {
		// The AgentFlow pipeline failed (invalid plan, unknown capability or
		// execution failure). The error is surfaced with the standard generic
		// NL-error text so the user never receives an empty message.
		return MsgNLErrInterpret(), err
	}

	switch result.Kind {
	case capability.ResponsePlan:
		// Pi proposed and CapabilityFlow already executed the plan: deliver the
		// executed results with the shared rendering.
		return application.FlowDeliveryText(result), nil

	case capability.ResponseConversation:
		// Pi answered conversationally: its reply is the answer.
		return result.Response, nil

	default:
		// Defensive: FlowResult.Kind is always conversation or plan; treat
		// anything else as an invalid-plan error.
		return MsgNLErrInterpret(), capability.ErrInvalidPlan
	}
}

// freeTextInstruction frames a free-text Telegram message for the AgentFlow
// pipeline exactly like the Orchestrator frames a fired task: a neutral
// "User message: …" envelope with no response-format or action mandates, so the
// planner context appended by PiAgent is the single authority deciding between a
// conversational reply and a Plan JSON.
func freeTextInstruction(text string) string {
	return "User message: " + text
}
