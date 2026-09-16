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
// exclusive. The `recurrence` object carries the cadence extracted by Pi and is
// strictly decoded into service.RecurrenceParams; Go derives and validates the
// whole RecurrenceSpec.
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

// CreateReminderHandler executes the three scheduling cases of the
// create_reminder capability. One-shot reminders (relative and absolute)
// resolve their fire time with the shared service.ResolveTime logic; recurring
// reminders are built with service.BuildRecurrenceJSON. Go owns all time and
// calendar derivation (Pi never computes a time.Time), and the Task + Trigger
// composition is delegated to ReminderService.CreateOneShot / CreateRecurring,
// reusing the existing reminder logic with zero duplication.
//
// The clock (WithNow) and the user's timezone (WithTimezone) are injected so
// resolution is deterministic: the default clock is time.Now and the default
// timezone is time.Local.
type CreateReminderHandler struct {
	reminders *service.ReminderService
	now       func() time.Time
	timezone  *time.Location
}

// Option configures a CreateReminderHandler.
type Option func(*CreateReminderHandler)

// WithNow overrides the clock used to resolve relative durations and to derive
// recurrence anchors (defaults to time.Now). Tests inject a fixed clock for
// deterministic resolution.
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

// Execute creates a reminder whose scheduling case is either `relative` from
// the injected clock, an absolute local wall-clock time (`absolute_time` plus
// the optional `absolute_date`), or a `recurrence` cadence. Args have already
// been validated against the capability schema by the dispatcher before this
// method is called.
//
// Semantic validation stays in Go (the resolution logic is not duplicated here):
//   - the three scheduling cases are mutually exclusive per the contract:
//     recurrence combined with relative/absolute, or relative combined with
//     absolute_time/absolute_date → ErrInvalidArgs;
//   - the recurring case is strictly decoded into service.RecurrenceParams
//     (unknown fields and type mismatches → ErrInvalidArgs) and the canonical
//     RecurrenceSpec is built exclusively by service.BuildRecurrenceJSON
//     (invalid cadences → ErrInvalidArgs);
//   - relative with an invalid or non-positive duration → ErrInvalidArgs
//     (service.ResolveTime is the single authority for duration parsing);
//   - absolute_date without absolute_time → ErrInvalidArgs;
//   - malformed absolute_time or absolute_date → ErrInvalidArgs (parsing is
//     delegated to service.ParseHHMM / service.ResolveTime, the single
//     authority for the supported formats and the today→tomorrow fallback);
//   - no scheduling case at all → ErrInvalidArgs.
func (h *CreateReminderHandler) Execute(ctx context.Context, args json.RawMessage) (CapabilityResult, error) {
	var in createReminderArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return CapabilityResult{}, fmt.Errorf("%w: unparseable args", ErrInvalidArgs)
	}

	rel := strings.TrimSpace(in.Relative)
	timeStr := strings.TrimSpace(in.AbsoluteTime)
	dateStr := strings.TrimSpace(in.AbsoluteDate)

	if in.Recurrence != nil {
		if rel != "" || timeStr != "" || dateStr != "" {
			return CapabilityResult{}, fmt.Errorf(
				"%w: recurrence is mutually exclusive with relative, absolute_time and absolute_date", ErrInvalidArgs)
		}

		params, err := decodeRecurrenceParams(in.Recurrence)
		if err != nil {
			return CapabilityResult{}, fmt.Errorf("%w: %v", ErrInvalidArgs, err)
		}

		// Go owns the whole canonical RecurrenceSpec: timezone from the
		// injected TimeContext, interval default, anchor derivation and final
		// validation via domain.ParseRecurrence (service.BuildRecurrenceJSON
		// is the single authority).
		recurrenceJSON, err := service.BuildRecurrenceJSON(params, service.TimeContext{
			Now:      h.now(),
			Timezone: h.timezone,
		})
		if err != nil {
			return CapabilityResult{}, fmt.Errorf("%w: %v", ErrInvalidArgs, err)
		}

		task, err := h.reminders.CreateRecurring(ctx, in.Title, sourceCapabilityReminder, recurrenceJSON)
		if err != nil {
			return CapabilityResult{}, err
		}

		return CapabilityResult{Data: CreateReminderResult{Task: toTaskView(task)}}, nil
	}

	if rel != "" && (timeStr != "" || dateStr != "") {
		return CapabilityResult{}, fmt.Errorf(
			"%w: relative is mutually exclusive with absolute_time and absolute_date", ErrInvalidArgs)
	}

	var spec service.OneShotSpec
	switch {
	case rel != "":
		spec = service.OneShotSpec{Relative: rel}
	case timeStr != "":
		// AbsoluteTime without AbsoluteDate defaults to today, with the
		// "tomorrow when already past" fallback; AbsoluteDate is "today",
		// "tomorrow" or "YYYY-MM-DD". service.ResolveTime owns that semantics.
		spec = service.OneShotSpec{AbsoluteTime: timeStr, AbsoluteDate: dateStr}
	case dateStr != "":
		return CapabilityResult{}, fmt.Errorf("%w: absolute_time is required when absolute_date is provided", ErrInvalidArgs)
	default:
		return CapabilityResult{}, fmt.Errorf("%w: one of relative or absolute_time is required", ErrInvalidArgs)
	}

	at, err := service.ResolveTime(spec, service.TimeContext{
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

// decodeRecurrenceParams strictly decodes the `recurrence` object into the
// shared service.RecurrenceParams wire contract (the exact type the NL layer
// uses, so there is no duplicated contract that can drift). Rejecting unknown
// fields and type mismatches fails closed, mirroring the strict constructor of
// the domain (domain.ParseRecurrence).
func decodeRecurrenceParams(raw map[string]json.RawMessage) (service.RecurrenceParams, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return service.RecurrenceParams{}, err
	}
	var p service.RecurrenceParams
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return service.RecurrenceParams{}, err
	}
	return p, nil
}
