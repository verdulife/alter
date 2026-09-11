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

// AgentErrorKind classifies an Agent execution failure so application layers
// can tell an infrastructure/provider problem apart from a non-recognized
// input and choose the right user-facing reply. Classification happens at the
// adapter boundary (where the failure originates); consumers read it with
// AgentErrorKindOf, never by parsing error text.
type AgentErrorKind int

const (
	// AgentErrorKindInternal is the default kind: an unexpected failure inside
	// the agent transport (spawn, IO, protocol, decode).
	AgentErrorKindInternal AgentErrorKind = iota
	// AgentErrorKindTimeout means the agent execution exceeded its time bound.
	AgentErrorKindTimeout
	// AgentErrorKindEmptyResponse means the agent produced no response text.
	AgentErrorKindEmptyResponse
	// AgentErrorKindProvider means the underlying model/provider failed (after
	// the agent's own internal retries) or the run was rejected for an
	// environment/configuration reason. Some of these are also marked permanent
	// via ErrActionPermanent (the two classifications are independent:
	// ErrActionPermanent drives scheduler retire/retry, the kind drives the
	// user-facing reply).
	AgentErrorKindProvider
)

// AgentError is a classified Agent failure. It implements error and preserves
// its cause through Unwrap, so errors.Is/errors.As keep working on the whole
// wrapped chain (e.g. a Provider failure that is also ErrActionPermanent).
type AgentError struct {
	// Kind is the classification of the failure.
	Kind AgentErrorKind
	// Err is the original failure; non-nil in practice.
	Err error
}

// Error implements error. The text is the underlying failure's text, so
// existing log/journal output keeps its current shape.
func (e *AgentError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "agent execution failed"
}

// Unwrap exposes the cause so classification and sentinel checks survive the
// wrapping layers between adapter and application.
func (e *AgentError) Unwrap() error { return e.Err }

// AgentErrorKindOf returns the classified kind of err. Errors without an
// attached classification default to AgentErrorKindInternal, except a bare (or
// wrapped) context deadline, which is a timeout: callers that do not attach
// AgentError still get the right visible outcome.
func AgentErrorKindOf(err error) AgentErrorKind {
	var ae *AgentError
	if errors.As(err, &ae) {
		return ae.Kind
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return AgentErrorKindTimeout
	}
	return AgentErrorKindInternal
}
