package naturalintent

import (
	"time"

	appservice "github.com/verdu/alter/internal/service"
)

// This file holds the natural-language adapters over the pure reminder
// resolution/building logic. The logic itself lives in the service layer
// (service.ResolveTime / service.BuildRecurrenceJSON) so future capability
// callers can reuse it without depending on this package; the NL layer only
// adapts its NL-typed spec (ReminderSpec) to the pure input types.

// ResolveTime converts a ReminderSpec into a concrete time.Time using the
// current time and the user's timezone. It returns an error when the spec
// cannot be resolved safely (e.g. malformed time, ambiguous date).
//
// It is a thin adapter over service.ResolveTime: the NL layer strips its
// recurrence field (recurring specs are built by BuildRecurrenceJSON, and the
// caller dispatches on Recurrence before reaching here) and passes the
// one-shot fields through, preserving the documented resolution rules:
//   - Relative: now.Add(parsed duration).
//   - AbsoluteTime + AbsoluteDate "today": same day; if the time has already
//     passed, resolve to tomorrow.
//   - AbsoluteTime + AbsoluteDate "tomorrow": next day.
//   - AbsoluteTime + AbsoluteDate "YYYY-MM-DD": that specific date.
//   - AbsoluteTime without AbsoluteDate: defaults to today (with tomorrow
//     fallback if past).
func ResolveTime(spec ReminderSpec, ctx InterpretContext) (time.Time, error) {
	return appservice.ResolveTime(appservice.OneShotSpec{
		Relative:     spec.Relative,
		AbsoluteTime: spec.AbsoluteTime,
		AbsoluteDate: spec.AbsoluteDate,
	}, ctx)
}

// BuildRecurrenceJSON turns the natural-language RecurrenceParams into the
// canonical RecurrenceSpec JSON stored in Trigger.Value (B3 S4). It delegates
// to service.BuildRecurrenceJSON: timezone always comes from InterpretContext
// (the user's), the anchor is derived when the user gave no explicit start
// date, and the result is ALWAYS validated with domain.ParseRecurrence — the
// domain remains the single authority for calendar rules.
func BuildRecurrenceJSON(p RecurrenceParams, ctx InterpretContext) (string, error) {
	return appservice.BuildRecurrenceJSON(p, ctx)
}
