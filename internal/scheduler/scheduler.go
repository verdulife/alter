// Package scheduler implements the V1 one-shot trigger Scheduler.
//
// The Scheduler is an inbound orchestrator. It never holds persistent planning
// state in memory: every cycle it re-reads enabled triggers from SQLite (the
// source of truth) and runs ARMAR -> DISPARAR -> PLANIFICAR. It uses a single
// time.Timer per cycle (no periodic polling) and a coalescing Wake() hint.
//
// Execution order per due trigger: ExecuteTrigger -> TriggerAction -> persist
// fire state -> emit EventTriggerFired -> recalculate deadline. A failed action
// does NOT consume the trigger (it stays due and retries); persistence happens
// only after a successful action (at-least-once: a crash between action and
// persist may produce a duplicate notification on recovery).
package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// Scheduler drives enabled triggers against their persisted NextFireAt.
//
// It is designed to run as a single goroutine via Run(ctx). A non-nil
// TriggerAction is required: without a real consequence a trigger must never be
// marked fired, so construction panics on a nil action.
type Scheduler struct {
	triggers domain.TriggerRepository
	tasks    domain.TaskRepository
	events   domain.EventStore
	action   domain.TriggerAction

	now    func() time.Time
	logger *log.Logger
	newID  func() string

	// wake is a buffered-capacity-1 coalescing hint channel. A send when the
	// buffer is full is dropped: one pending wake is enough to trigger a rescan.
	wake chan struct{}
}

// NewScheduler builds a Scheduler. action is required and must not be nil.
func NewScheduler(
	triggers domain.TriggerRepository,
	tasks domain.TaskRepository,
	events domain.EventStore,
	action domain.TriggerAction,
	opts ...Option,
) *Scheduler {
	if action == nil {
		// A nil/no-op action would let a trigger be marked fired with no real
		// consequence. Refuse to construct rather than silently consume.
		panic("scheduler: TriggerAction is required (nil action would consume triggers without consequence)")
	}
	s := &Scheduler{
		triggers: triggers,
		tasks:    tasks,
		events:   events,
		action:   action,
		now:      time.Now,
		logger:   log.New(io.Discard, "", 0),
		newID:    newLocalID,
		wake:     make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option configures a Scheduler (testing knobs and logging).
type Option func(*Scheduler)

// WithNow overrides the clock used for scheduling decisions (deterministic tests).
func WithNow(f func() time.Time) Option { return func(s *Scheduler) { s.now = f } }

// WithLogger sets the logger used for non-fatal diagnostics.
func WithLogger(l *log.Logger) Option { return func(s *Scheduler) { s.logger = l } }

// WithID overrides the event ID generator.
func WithID(f func() string) Option { return func(s *Scheduler) { s.newID = f } }

// Wake is a coarse, non-blocking, coalescing hint: it tells the Scheduler that
// the state determining the next deadline may have changed and it should rescan
// SQLite now. It never blocks the caller and at most one pending wake is kept.
// Wake is not required for correctness: the next normal cycle (timer, startup,
// or any later wake) re-reads SQLite anyway.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
		// buffer full: a wake is already pending; coalesce.
	}
}

// Run executes the scheduling loop until ctx is cancelled. It blocks the caller.
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		next, err := s.tick(ctx)
		if err != nil {
			// Catastrophic (e.g. ListEnabled failed). Refuse to spin: wait only on
			// wake/ctx until the system recovers or shutdown is requested.
			s.logger.Printf("scheduler: cycle failed: %v", err)
			next = time.Time{}
		}
		if err := s.wait(ctx, next); err != nil {
			return err
		}
	}
}

// tick runs one full ARMAR -> DISPARAR -> PLANIFICAR cycle against a fresh read
// of enabled triggers and returns the next absolute deadline (zero if none).
func (s *Scheduler) tick(ctx context.Context) (time.Time, error) {
	now := s.now()
	enabled, err := s.triggers.ListEnabled(ctx)
	if err != nil {
		return time.Time{}, err
	}

	// Derived scheduling view, rebuilt from SQLite every cycle (no in-memory
	// source of truth).
	scheduled := make([]domain.Trigger, 0, len(enabled))

	// ARMAR: give every enabled trigger without a NextFireAt a persisted deadline.
	for _, t := range enabled {
		if t.NextFireAt != nil {
			scheduled = append(scheduled, t)
			continue
		}
		if next, ok := s.arm(ctx, t); ok {
			t.NextFireAt = &next
			scheduled = append(scheduled, t)
		}
		// !ok: malformed or otherwise unarmable -> left nil (excluded from due
		// and from the deadline), no busy loop.
	}

	// DISPARAR: process ALL due triggers before planning the next deadline.
	for _, t := range scheduled {
		if t.NextFireAt != nil && !t.NextFireAt.After(now) {
			s.fireOne(ctx, t, now)
		}
	}

	// PLANIFICAR: nearest future deadline among surviving (still enabled) triggers.
	var next time.Time
	found := false
	for _, t := range scheduled {
		if t.NextFireAt == nil || !t.NextFireAt.After(now) {
			continue
		}
		if !found || t.NextFireAt.Before(next) {
			next = *t.NextFireAt
			found = true
		}
	}
	return next, nil
}

// arm computes and persists NextFireAt for an unarmed (NextFireAt==nil) trigger.
// It reports ok=true only when a deadline was computed and persisted.
func (s *Scheduler) arm(ctx context.Context, t domain.Trigger) (time.Time, bool) {
	task, err := s.tasks.GetByID(ctx, t.TaskID)
	if err != nil {
		if isNotFound(err) {
			s.retire(ctx, t, "task not found")
		} else {
			s.logger.Printf("scheduler: load task %s: %v", t.TaskID, err)
		}
		return time.Time{}, false
	}

	next, err := domain.CalculateNextFireAt(t, task)
	if err != nil {
		// Malformed / DueAt missing: terminal but reversible; leave unarmed.
		s.logger.Printf("scheduler: cannot arm trigger %s: %v", t.ID, err)
		return time.Time{}, false
	}

	t.NextFireAt = &next
	if err := s.triggers.Update(ctx, t); err != nil {
		s.logger.Printf("scheduler: persist arm trigger %s: %v", t.ID, err)
		return time.Time{}, false
	}
	return next, true
}

// fireOne executes one due trigger: ExecuteTrigger -> action -> persist -> emit.
func (s *Scheduler) fireOne(ctx context.Context, t domain.Trigger, now time.Time) {
	task, err := s.tasks.GetByID(ctx, t.TaskID)
	if err != nil {
		if isNotFound(err) {
			s.retire(ctx, t, "task not found")
		} else {
			s.logger.Printf("scheduler: load task %s: %v", t.TaskID, err)
		}
		return
	}

	executed, err := domain.ExecuteTrigger(t, task, now)
	if err != nil {
		if errors.Is(err, domain.ErrTaskNotExecutable) {
			// Terminal: completed/cancelled task. Retire so it leaves the active
			// set and cannot busy-loop.
			s.retire(ctx, t, "task not executable")
			return
		}
		// Unexpected (e.g. disabled mid-flight, not scheduled): transient/skip.
		s.logger.Printf("scheduler: execute trigger %s: %v", t.ID, err)
		return
	}

	if err := s.action.Execute(ctx, executed, task); err != nil {
		// Transitory: do NOT persist. The trigger stays due and retries next cycle.
		s.logger.Printf("scheduler: action for trigger %s: %v", t.ID, err)
		return
	}

	if err := s.triggers.Update(ctx, executed); err != nil {
		// at-least-once: the trigger is still due in SQLite; on recovery it may
		// re-fire (possible duplicate, preferred over losing the notification).
		s.logger.Printf("scheduler: persist trigger %s: %v", t.ID, err)
		return
	}

	s.emit(ctx, executed, now)
}

// retire disables a terminal trigger so it leaves the active set. NextFireAt is
// left untouched; Enabled=false removes it from ListEnabled, preventing loops.
func (s *Scheduler) retire(ctx context.Context, t domain.Trigger, reason string) {
	disabled := t
	disabled.Enabled = false
	if err := s.triggers.Update(ctx, disabled); err != nil {
		s.logger.Printf("scheduler: retire trigger %s (%s): %v", t.ID, reason, err)
		return
	}
	s.logger.Printf("scheduler: retired trigger %s (%s)", t.ID, reason)
}

// emit records EventTriggerFired best-effort: a failure never blocks nor rolls
// back the fire.
func (s *Scheduler) emit(ctx context.Context, executed domain.Trigger, firedAt time.Time) {
	_ = s.events.Save(ctx, domain.Event{
		ID:   s.newID(),
		Type: domain.EventTriggerFired,
		Payload: map[string]any{
			"trigger_id": executed.ID,
			"task_id":    executed.TaskID,
			"fired_at":   firedAt.UTC().Format(time.RFC3339),
		},
		CreatedAt: s.now().UTC(),
	})
}

// wait blocks until the next deadline, a wake hint, or ctx cancellation. With no
// future deadline it blocks only on wake/ctx (no timer, no polling).
func (s *Scheduler) wait(ctx context.Context, next time.Time) error {
	var timer *time.Timer
	var timerC <-chan time.Time
	if !next.IsZero() {
		d := next.Sub(s.now())
		if d <= 0 {
			d = 0 // clock granularity: deadline is now; re-tick immediately.
		}
		timer = time.NewTimer(d)
		timerC = timer.C
		defer timer.Stop()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.wake:
		return nil
	case <-timerC:
		return nil
	}
}

// isNotFound reports whether err is a SQL "no rows" sentinel (missing task).
func isNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// newLocalID returns a 32-character hex identifier generated locally with
// crypto/rand, mirroring the service layer's dependency-free V1 ID generator.
func newLocalID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read does not fail on supported platforms.
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
