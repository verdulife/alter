package telegram

import (
	"context"

	"github.com/verdu/alter/internal/application"
	"github.com/verdu/alter/internal/capability"
	"github.com/verdu/alter/internal/domain"
)

// NaturalRecognizer is the minimal fallback surface the AgentFlowHandler needs
// from a natural-language interpreter: besides the reply it must report whether
// the free text was recognized as an actionable operation (so the caller knows a
// conversational Pi reply should be replaced by the interpreter's executed
// operation reply). naturalintent.Service implements this seam via
// HandleMessageStructured; the seam is declared here so the adapter never
// depends on the naturalintent package.
type NaturalRecognizer interface {
	// HandleMessageStructured processes a free-text message and returns the
	// reply plus whether the message was recognized as an actionable operation
	// (create/reminder/complete/cancel) and executed. recognized=false means the
	// interpreter had nothing actionable to contribute (ambiguous or
	// unrecognized), so the caller keeps its own reply.
	HandleMessageStructured(ctx context.Context, text string) (reply string, recognized bool, err error)
}

// AgentFlowHandler is the inbound free-text route that puts the AgentFlow
// capability pipeline (Pi Agent + CapabilityFlow) in front of natural-language
// interpretation whenever the Pi Agent is enabled:
//
//	free text
//	  → AgentFlow.Execute (Pi decides conversation vs Plan JSON)
//	  ├── Plan         → executed capability results (list_tasks) are delivered
//	  ├── Conversation → fallback NaturalIntent if it recognizes an actionable
//	│                    operation (e.g. create/reminder not yet migrated to a
//	│                    capability); otherwise the conversational Pi reply wins
//	  └── error        → fallback if available, otherwise the error is surfaced
//
// Slash commands never reach this handler: telegram.Handle routes them directly
// to the CommandService before consulting the natural handler.
type AgentFlowHandler struct {
	flow     *application.AgentFlow
	fallback NaturalRecognizer // optional: natural-language operations fallback
}

// NewAgentFlowHandler builds the handler. flow must be non-nil; fallback is
// optional (nil disables the natural-language fallback and conversational Pi
// replies are always delivered as-is).
func NewAgentFlowHandler(flow *application.AgentFlow, fallback NaturalRecognizer) *AgentFlowHandler {
	if flow == nil {
		panic("telegram: NewAgentFlowHandler with nil AgentFlow")
	}
	return &AgentFlowHandler{flow: flow, fallback: fallback}
}

// Handle implements the telegram.NaturalHandler seam for free text. The reply
// it returns is the user-facing text for Telegram; the caller owns HTML escape.
func (h *AgentFlowHandler) Handle(ctx context.Context, text string) (string, error) {
	result, err := h.flow.Execute(ctx, domain.AgentRequest{
		Instruction: freeTextInstruction(text),
	})
	if err != nil {
		// The AgentFlow pipeline failed (invalid plan, unknown capability or
		// execution failure). The natural-language fallback is the temporary
		// safety net for operations not yet migrated to capabilities; without it
		// the error is surfaced with the standard generic NL-error text so the
		// user never receives an empty message.
		if h.fallback != nil {
			reply, recognized, ferr := h.fallback.HandleMessageStructured(ctx, text)
			if ferr != nil {
				return reply, ferr
			}
			if recognized {
				return reply, nil
			}
		}
		return MsgNLErrInterpret(), err
	}

	switch result.Kind {
	case capability.ResponsePlan:
		// Pi proposed and CapabilityFlow already executed the plan (e.g.
		// list_tasks): deliver the executed results with the shared rendering.
		return application.FlowDeliveryText(result), nil

	case capability.ResponseConversation:
		// Pi answered conversationally. If a natural-language fallback is wired
		// and it recognizes an actionable operation (one still handled by
		// NaturalIntent, not yet a capability), its executed reply wins;
		// otherwise Pi's conversational reply is the answer.
		if h.fallback != nil {
			reply, recognized, ferr := h.fallback.HandleMessageStructured(ctx, text)
			if ferr != nil {
				return reply, ferr
			}
			if recognized {
				return reply, nil
			}
		}
		return result.Response, nil

	default:
		// Defensive: FlowResult.Kind is always conversation or plan; treat
		// anything else as an invalid-plan error and fall back when possible.
		if h.fallback != nil {
			reply, recognized, ferr := h.fallback.HandleMessageStructured(ctx, text)
			if ferr != nil {
				return reply, ferr
			}
			if recognized {
				return reply, nil
			}
		}
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
