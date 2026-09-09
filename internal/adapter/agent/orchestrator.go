// Package agent implements the agent side of alter: the domain.Agent port is
// implemented here (Slice 3 only defines the boundary + an in-memory fake for
// tests of the composite TriggerAction), and the concrete Pi transport protocol
// is explicitly pending until its protocol is known — nothing fictitious is wired
// here. It also holds the Orchestrator: a concrete domain.TriggerAction that
// composes Agent -> Channel -> Event(best-effort) on the Scheduler seam.
//
// Dependency direction: this package depends only on internal/domain and the
// standard library. It never imports the scheduler, storage, or telegram
// packages.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
// Orchestrator is intentionally not wired into main.go in Slice 3: the runtime
// wiring stays on NotifyAction until the real Pi transport exists.
type Orchestrator struct {
	agent   domain.Agent
	channel domain.Channel
	events  domain.EventStore

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

// Execute runs the Agent -> Channel -> Event(best-effort) flow for a fired
// trigger and its task. It returns only an error when the consequence has not
// happened: an Agent failure or a Channel failure. After a confirmed delivery the
// audit Event failure never surfaces (best-effort), so the Scheduler must not
// retry.
func (o *Orchestrator) Execute(ctx context.Context, trigger domain.Trigger, task domain.Task) error {
	result, err := o.agent.Execute(ctx, domain.AgentRequest{
		Task:        task,
		Trigger:     trigger,
		Instruction: defaultInstruction(task),
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
	// the delivery is NOT retried.
	if err := o.events.Save(ctx, domain.Event{
		ID:   o.newID(),
		Type: domain.EventAgentResult,
		Payload: map[string]any{
			"task_id":    task.ID,
			"trigger_id": trigger.ID,
			"response":   result.Response,
		},
		CreatedAt: o.now().UTC(),
	}); err != nil {
		o.logger.Printf("orchestrator: persist agent result event for trigger %s: %v", trigger.ID, err)
	}
	return nil
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
