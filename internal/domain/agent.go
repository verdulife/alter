package domain

import (
	"context"
	"errors"
)

// Agent is an outbound port that turns the context of a fired trigger into a
// user-facing response. It is deliberately neutral: it knows nothing about
// Telegram, the Scheduler, SQLite, the concrete transport (e.g. Pi Agent) or
// semantic search. A concrete implementation (e.g. a Pi Agent adapter) lives in
// the adapter layer; see internal/adapter/agent.
//
// Agent is intentionally NOT a TriggerAction. The Scheduler seam remains
// TriggerAction; an orchestrator that composes Agent + Channel + EventStore
// implements TriggerAction and drives the Agent (see internal/adapter/agent).
type Agent interface {
	Execute(ctx context.Context, request AgentRequest) (AgentResult, error)
}

// AgentRequest is the neutral context handed to an Agent. It carries the
// structured Task and the Trigger that fired (reusing existing domain types, so
// no generic map is needed) plus a derived Instruction. Semantic context is a
// future addition and is deliberately absent in V1: no generic context bag.
type AgentRequest struct {
	// Task is the structured task data relevant to the request.
	Task Task
	// Trigger is the reason the Agent was invoked (the fired trigger). Its
	// scheduling bookkeeping (NextFireAt/RetryAt) is harmless noise for V1.
	Trigger Trigger
	// Instruction is the user-facing intent/request. In V1 it is derived by the
	// orchestrator from the Task; it is not read from the data model.
	Instruction string
}

// AgentResult is the minimal output of an Agent execution: the user-facing text
// that a domain.Channel will deliver. It is independent of Telegram: Channel is
// the port that decides the concrete transport.
type AgentResult struct {
	// Response is the text to send via Channel.
	Response string
}

// ErrActionPermanent marks a TriggerAction failure that must NOT be retried: the
// failure is permanent (auth, billing, subscription, invalid request, quota)
// rather than transitory. The Scheduler uses a single errors.Is check to retire
// the trigger instead of applying a RetryAt backoff, keeping the Scheduler
// generic and AI-agnostic. An adapter/orchestrator classifies errors at its own
// boundary and wraps permanent ones with this sentinel; everything else is
// treated as retryable by default.
var ErrActionPermanent = errors.New("action failed permanently")
