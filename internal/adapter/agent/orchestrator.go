// Package agent implements the agent side of alter: the domain.Agent port is
// implemented here by the PiAgent RPC adapter (pi.go), which drives the pi CLI
// RPC mode as a one-shot subprocess per execution. It also holds the
// Orchestrator: a concrete domain.TriggerAction that composes
// Agent -> Channel -> Event(best-effort) on the Scheduler seam.
//
// Dependency direction: this package depends only on internal/domain (plus
// internal/capability for the optional planner context embedded in requests)
// and the standard library. It never imports the scheduler, storage, or
// telegram packages.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// Orchestrator is a concrete domain.TriggerAction that drives the Agent on the
// Scheduler seam. Execution order (Agent -> Channel -> Event) is deliberate:
//
//   - Agent.Execute produces the user-facing response. An error propagates so the
//     Scheduler decides RetryAt (transitory) vs retire (ErrActionPermanent). On
//     failure nothing is sent and no Event is persisted.
//   - Channel.Send delivers the response; it is the real at-least-once consequence.
//     A send failure propagates so the Scheduler applies RetryAt. No Event is
//     persisted before a delivery that may not have happened.
//   - Event.Save persists the agent result as an audit Event only AFTER a confirmed
//     delivery. It is strictly best-effort: a failure is logged and ignored and
//     must never trigger a retry (the delivery already happened).
//
// Orchestrator is intentionally the runtime action only when a real Agent is
// wired (see cmd/alter/main.go: it is chosen over NotifyAction when Pi is
// enabled). The in-memory fakes of its tests are never part of production
// wiring, and no fake Agent exists in the runtime path.
type Orchestrator struct {
	agent    domain.Agent
	channel  domain.Channel
	events   domain.EventStore
	searcher domain.SemanticSearcher // optional: semantic retrieval for the Agent context
	indexer  domain.SemanticIndexer  // optional: derived index write side (agent memory)

	now    func() time.Time
	newID  func() string
	logger *log.Logger
}

var _ domain.TriggerAction = (*Orchestrator)(nil)

// NewOrchestrator builds the composite action. agent and channel must be non-nil.
func NewOrchestrator(agent domain.Agent, channel domain.Channel, events domain.EventStore, opts ...Option) *Orchestrator {
	o := &Orchestrator{
		agent:   agent,
		channel: channel,
		events:  events,
		now:     time.Now,
		newID:   newLocalID,
		logger:  log.New(io.Discard, "", 0),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Option configures an Orchestrator (testing knobs and logging).
type Option func(*Orchestrator)

// WithNow overrides the clock used for Event timestamps (deterministic tests).
func WithNow(f func() time.Time) Option { return func(o *Orchestrator) { o.now = f } }

// WithID overrides the Event ID generator (deterministic tests).
func WithID(f func() string) Option { return func(o *Orchestrator) { o.newID = f } }

// WithLogger sets the logger used for non-fatal diagnostics.
func WithLogger(l *log.Logger) Option { return func(o *Orchestrator) { o.logger = l } }

// WithSearcher wires the optional semantic search read side. When absent (or
// failing), the Orchestrator enriches nothing and behaves exactly as V1: the
// fire is never blocked by search.
func WithSearcher(s domain.SemanticSearcher) Option {
	return func(o *Orchestrator) { o.searcher = s }
}

// WithIndexer wires the optional derived semantic index write side: after a
// confirmed delivery, the agent.result Event is also fed to the index so past
// agent responses become retrievable memory. Best-effort: a failure never
// breaks the delivery.
func WithIndexer(i domain.SemanticIndexer) Option {
	return func(o *Orchestrator) { o.indexer = i }
}

// Execute runs the Agent -> Channel -> Event(best-effort) flow for a fired
// trigger and its task. It returns only an error when the consequence has not
// happened: an Agent failure or a Channel failure. After a confirmed delivery the
// audit Event failure never surfaces (best-effort), so the Scheduler must not
// retry.
func (o *Orchestrator) Execute(ctx context.Context, trigger domain.Trigger, task domain.Task) error {
	result, err := o.agent.Execute(ctx, domain.AgentRequest{
		Task:        task,
		Trigger:     trigger,
		Instruction: o.instruction(ctx, task),
	})
	if err != nil {
		// Agent failed: no consequence happened. Propagate so the Scheduler applies
		// RetryAt (transitory) or retires (ErrActionPermanent).
		return err
	}

	if err := o.channel.Send(ctx, result.Response); err != nil {
		// Delivery failed: the consequence did not happen. Propagate (Scheduler
		// RetryAt). No Event is persisted for an unconfirmed delivery. A retry
		// re-runs the whole composite (at-least-once); a duplicate response/event is
		// accepted in V1 — no dedup or idempotency is introduced.
		return err
	}

	// Delivery confirmed. Persist the audit Event best-effort; failure is logged and
	// the delivery is NOT retried. The semantic memory write (IndexEvent) happens
	// only after a confirmed delivery and only when an indexer is wired.
	event := domain.Event{
		ID:   o.newID(),
		Type: domain.EventAgentResult,
		Payload: map[string]any{
			"task_id":    task.ID,
			"trigger_id": trigger.ID,
			"response":   result.Response,
		},
		CreatedAt: o.now().UTC(),
	}
	if err := o.events.Save(ctx, event); err != nil {
		o.logger.Printf("orchestrator: persist agent result event for trigger %s: %v", trigger.ID, err)
	}
	o.indexEvent(ctx, event)
	return nil
}

// instruction derives the Agent instruction for a fired task: the neutral V1
// default plus, when a searcher is wired and returns related context, a bounded
// digest of the best results with the task itself excluded. Retrieval is
// fail-open: any error (including domain.ErrSemanticUnavailable) or an empty
// result set falls back to the default instruction, and the fire is never
// blocked by search. The Orchestrator only ever sees the domain port: it never
// touches SQLite, the vector tables or the embedding provider.
func (o *Orchestrator) instruction(ctx context.Context, task domain.Task) string {
	var base string
	if o.searcher == nil {
		base = defaultInstruction(task)
	} else {
		results, err := o.searcher.Search(ctx, searchQuery(task), domain.SearchOptions{
			// Limit 0: the adapter applies its configured default (runtime 5).
			Exclude: []domain.Ref{{Kind: domain.RefKindTask, ID: task.ID}},
		})
		if err != nil {
			if !errors.Is(err, domain.ErrSemanticUnavailable) {
				// Unavailable is the expected degraded state (already logged at
				// startup); log only genuine failures.
				o.logger.Printf("orchestrator: semantic search skipped: %v", err)
			}
			base = defaultInstruction(task)
		} else {
			base = assembleInstruction(task, results)
		}
	}
	return reminderPrompt + "\n\nUser message: " + base
}

// indexEvent feeds a confirmed agent.result Event to the derived semantic index
// (best-effort, same resilience as EventStore.Save: a failure never breaks the
// delivery and is not retried; the boot rebuild converges any gap).
func (o *Orchestrator) indexEvent(ctx context.Context, event domain.Event) {
	if o.indexer == nil {
		return
	}
	if err := o.indexer.IndexEvent(ctx, event); err != nil && !errors.Is(err, domain.ErrSemanticUnavailable) {
		o.logger.Printf("orchestrator: index agent result event %s: %v", event.ID, err)
	}
}

// defaultInstruction derives the V1 Agent instruction from the task. No schema
// change: it is computed here, not read from the data model.
func defaultInstruction(task domain.Task) string {
	return fmt.Sprintf("Handle the reminder for task %q.", task.Title)
}

// newLocalID returns a 32-character hex identifier generated locally with
// crypto/rand, mirroring the service and scheduler layers' dependency-free V1 ID
// generators.
func newLocalID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read does not fail on supported platforms.
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
