package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/verdu/alter/internal/service"
)

// createReminderArgs is the JSON-validated input for the create_reminder
// capability. It mirrors the NL V2 contract (ReminderSpec): title is required
// and the three scheduling cases (relative, absolute, recurring) are mutually
// exclusive. Only the one-shot relative case is implemented in this step; the
// other fields are declared by the contract and rejected by the handler until
// implemented.
type createReminderArgs struct {
	Title        string                     `json:"title"`
	Relative     string                     `json:"relative"`
	AbsoluteTime string                     `json:"absolute_time"`
	AbsoluteDate string                     `json:"absolute_date"`
	Recurrence   map[string]json.RawMessage `json:"recurrence"`
}

// CreateReminderResult is the Data payload produced by the create_reminder
// capability. It reuses the existing TaskView stable external representation,
// exactly like the other shipped capabilities.
type CreateReminderResult struct {
	Task TaskView `json:"task"`
}

// sourceCapabilityReminder is the task Source stamped on every task created
// through the create_reminder capability (mirrors "capability:create_task").
const sourceCapabilityReminder = "capability:create_reminder"

// CreateReminderHandler executes the create_reminder capability for the
// one-shot relative case: it resolves the relative duration with the shared
// service.ResolveTime logic (Go owns the resolution; Pi never computes a
// time.Time) and delegates the Task + Trigger composition to
// ReminderService.CreateOneShot. It reuses the existing reminder logic with
// zero duplication.
//
// The clock (WithNow) and the user's timezone (WithTimezone) are injected so
// resolution is deterministic and independent of NaturalIntent: the default
// clock is time.Now and the default timezone is time.Local.
type CreateReminderHandler struct {
	reminders *service.ReminderService
	now       func() time.Time
	timezone  *time.Location
}

// Option configures a CreateReminderHandler.
type Option func(*CreateReminderHandler)

// WithNow overrides the clock used to resolve relative durations (defaults to
// time.Now). Tests inject a fixed clock for deterministic resolution.
func WithNow(f func() time.Time) Option {
	return func(h *CreateReminderHandler) { h.now = f }
}

// WithTimezone sets the user's timezone used in time resolution (defaults to
// time.Local). Passing nil falls back to time.Local.
func WithTimezone(loc *time.Location) Option {
	return func(h *CreateReminderHandler) {
		if loc == nil {
			h.timezone = time.Local
			return
		}
		h.timezone = loc
	}
}

// NewCreateReminderHandler creates a handler backed by the given ReminderService.
// It panics if rs is nil.
func NewCreateReminderHandler(rs *service.ReminderService, opts ...Option) *CreateReminderHandler {
	if rs == nil {
		panic("capability: NewCreateReminderHandler with nil ReminderService")
	}
	h := &CreateReminderHandler{reminders: rs, now: time.Now, timezone: time.Local}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Execute creates a one-shot reminder whose fire time is `relative` from the
// injected clock. Args have already been validated against the capability
// schema by the dispatcher before this method is called.
//
// Semantic validation stays in Go (the resolution logic is not duplicated here):
//   - relative missing or empty → ErrInvalidArgs;
//   - relative with an invalid or non-positive duration → ErrInvalidArgs
//     (service.ResolveTime is the single authority for duration parsing);
//   - any absolute_time / absolute_date / recurrence field (implemented by
//     later steps, and mutually exclusive with relative per the contract) →
//     ErrInvalidArgs.
func (h *CreateReminderHandler) Execute(ctx context.Context, args json.RawMessage) (CapabilityResult, error) {
	var in createReminderArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return CapabilityResult{}, fmt.Errorf("%w: unparseable args", ErrInvalidArgs)
	}

	if in.AbsoluteTime != "" || in.AbsoluteDate != "" || in.Recurrence != nil {
		return CapabilityResult{}, fmt.Errorf(
			"%w: only the one-shot relative case is implemented; absolute_time, absolute_date and recurrence are rejected", ErrInvalidArgs)
	}

	rel := strings.TrimSpace(in.Relative)
	if rel == "" {
		return CapabilityResult{}, fmt.Errorf("%w: relative is required", ErrInvalidArgs)
	}

	at, err := service.ResolveTime(service.OneShotSpec{Relative: rel}, service.TimeContext{
		Now:      h.now(),
		Timezone: h.timezone,
	})
	if err != nil {
		return CapabilityResult{}, fmt.Errorf("%w: %v", ErrInvalidArgs, err)
	}

	task, err := h.reminders.CreateOneShot(ctx, in.Title, sourceCapabilityReminder, at)
	if err != nil {
		return CapabilityResult{}, err
	}

	return CapabilityResult{Data: CreateReminderResult{Task: toTaskView(task)}}, nil
}
