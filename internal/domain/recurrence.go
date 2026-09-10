package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Recurrence support (B3, S0): the deterministic recurring calendar.
//
// A RecurrenceSpec defines an infinite, deterministic sequence of occurrence
// instants — a pure function of the spec alone. It carries no state: nothing
// about previous executions (LastFiredAt, NextFireAt) influences the calendar.
// NextFireAt (added in S1/S3) is only a derived cache of "the next calendar
// occurrence", never the source of truth for cadence.
//
// The spec is transported as a canonical JSON string in Trigger.Value for a
// future TriggerTypeRecurring trigger; ParseRecurrence validates it strictly
// (unknown fields and malformed values are rejected at creation time).
//
// Calendar rules (all candidate dates are in the spec's Timezone, at the
// spec's local time; anchor = date of the very first occurrence):
//   - daily:   candidate is an occurrence when (candidate - anchor) mod
//     interval == 0, any weekday.
//   - weekly:  the candidate's ISO week (Monday-based) must be a phase week:
//     weeks since the Monday of the anchor's week, counted in days, satisfy
//     (days / 7) mod interval == 0 — day arithmetic, no ISO week numbering —
//     AND the candidate's weekday bit must be set in the Weekdays mask.
//   - monthly: months since the anchor's year-month satisfy
//     (months) mod interval == 0 AND candidate.day ==
//     min(DayOfMonth, days-in-month) (clamped to the month's last day).
//
// A NextFireAt computed from any reference point is therefore reproducible
// across restarts: the phase comes from the spec, never from execution state.
//
// DST: occurrences are built with time.Date in the spec's location, so a
// daily "08:00" stays at 08:00 wall-clock across transitions (the UTC instant
// shifts). A nonexistent local time (spring gap) is resolved by Go's
// normalization (02:30 -> 03:30); an ambiguous one (autumn repeat) is resolved
// exactly as time.Date resolves it (as of Go 1.25: the earlier instant), with
// the wall clock preserved either way. Both are documented as the contract:
// NextOccurrence returns exactly time.Date(y, mo, d, hh, mm, 0, 0, loc) for
// the winning candidate date.
//
// The search is bounded and deterministic: for a validated spec (interval >= 1,
// weekly mask non-empty) the first eligible future occurrence is provably at
// most Interval+1 (daily), 7*Interval+7 (weekly) or 31*Interval+31 (monthly)
// calendar days from the reference date, so the scan always terminates.

// RecurrenceFreq identifies the cadence unit of a RecurrenceSpec.
type RecurrenceFreq string

const (
	// RecurrenceFreqDaily fires every Interval days.
	RecurrenceFreqDaily RecurrenceFreq = "daily"
	// RecurrenceFreqWeekly fires every Interval weeks on the Weekdays mask.
	RecurrenceFreqWeekly RecurrenceFreq = "weekly"
	// RecurrenceFreqMonthly fires every Interval months on DayOfMonth (clamped).
	RecurrenceFreqMonthly RecurrenceFreq = "monthly"
)

// Weekday bitmask values (Monday=1 ... Sunday=64). The mask is used by weekly
// recurrences; empty (0) is invalid for weekly and ignored otherwise.
const (
	WeekdayMonday    = 1 << 0 // 1
	WeekdayTuesday   = 1 << 1 // 2
	WeekdayWednesday = 1 << 2 // 4
	WeekdayThursday  = 1 << 3 // 8
	WeekdayFriday    = 1 << 4 // 16
	WeekdaySaturday  = 1 << 5 // 32
	WeekdaySunday    = 1 << 6 // 64

	weekdaysAll = 1<<7 - 1 // 127: all valid bits
)

// RecurrenceSpec defines the recurring calendar. It is created via
// ParseRecurrence (the only validated constructor); the JSON tags document the
// canonical wire encoding stored in Trigger.Value for a recurring trigger.
type RecurrenceSpec struct {
	Freq       RecurrenceFreq `json:"freq"`
	Interval   int            `json:"interval"`     // default 1; must be >= 1
	Weekdays   int            `json:"weekdays"`     // bitmask, required (non-zero) for weekly
	DayOfMonth int            `json:"day_of_month"` // 1..31, used by monthly (clamped); default 1
	Time       string         `json:"time"`         // "HH:MM" local time of day (required)
	Timezone   string         `json:"timezone"`     // IANA name (required)
	Anchor     string         `json:"anchor"`       // "YYYY-MM-DD" local date of the first occurrence (required)
}

// Sentinel errors returned by ParseRecurrence and NextOccurrence.
var (
	// ErrInvalidRecurrenceJSON means the spec JSON is malformed (syntax or trailing data).
	ErrInvalidRecurrenceJSON = errors.New("invalid recurrence JSON")
	// ErrRecurrenceUnknownField means the spec JSON carries a field this version does not know.
	ErrRecurrenceUnknownField = errors.New("recurrence JSON has an unknown field")
	// ErrMissingRecurrenceField means a required field (freq, time, timezone, anchor) is absent.
	ErrMissingRecurrenceField = errors.New("missing recurrence field")
	// ErrInvalidRecurrenceFreq means Freq is not daily/weekly/monthly.
	ErrInvalidRecurrenceFreq = errors.New("unknown recurrence frequency")
	// ErrInvalidRecurrenceInterval means Interval is < 1.
	ErrInvalidRecurrenceInterval = errors.New("recurrence interval must be >= 1")
	// ErrInvalidRecurrenceWeekdays means a weekly recurrence has an empty or out-of-range mask.
	ErrInvalidRecurrenceWeekdays = errors.New("weekly recurrence requires weekdays in 1..127 (Monday=1..Sunday=64 bitmask)")
	// ErrInvalidRecurrenceDayOfMonth means DayOfMonth is outside 1..31.
	ErrInvalidRecurrenceDayOfMonth = errors.New("day_of_month must be between 1 and 31")
	// ErrInvalidRecurrenceTime means Time is not a valid "HH:MM".
	ErrInvalidRecurrenceTime = errors.New("invalid recurrence time (expected HH:MM)")
	// ErrInvalidRecurrenceTimezone means Timezone is not a loadable IANA name.
	ErrInvalidRecurrenceTimezone = errors.New("invalid recurrence timezone (IANA name)")
	// ErrInvalidRecurrenceAnchor means Anchor is not "YYYY-MM-DD" or is not an
	// occurrence date of its own spec (weekly: weekday in mask; monthly: day matches the clamp).
	ErrInvalidRecurrenceAnchor = errors.New("invalid recurrence anchor (expected YYYY-MM-DD as an occurrence date)")
	// ErrRecurrenceSearchExceeded is an internal invariant guard: the bounded
	// scan ran out of days, which is unreachable for a validated spec.
	ErrRecurrenceSearchExceeded = errors.New("recurrence search exceeded its analytical bound (internal error)")
)

// ParseRecurrence parses and validates the canonical recurrence JSON. It is the
// only validated constructor of RecurrenceSpec: unknown fields, missing required
// fields, malformed values and anchors that are not occurrence dates are all
// rejected here so a bad spec never reaches the Scheduler.
func ParseRecurrence(s string) (RecurrenceSpec, error) {
	var raw struct {
		Freq       string `json:"freq"`
		Interval   *int   `json:"interval"`
		Weekdays   *int   `json:"weekdays"`
		DayOfMonth *int   `json:"day_of_month"`
		Time       string `json:"time"`
		Timezone   string `json:"timezone"`
		Anchor     string `json:"anchor"`
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return RecurrenceSpec{}, fmt.Errorf("%w: %v", ErrRecurrenceUnknownField, err)
		}
		return RecurrenceSpec{}, fmt.Errorf("%w: %v", ErrInvalidRecurrenceJSON, err)
	}
	if dec.More() {
		return RecurrenceSpec{}, fmt.Errorf("%w: trailing data after first JSON value", ErrInvalidRecurrenceJSON)
	}
	if raw.Freq == "" || raw.Time == "" || raw.Timezone == "" || raw.Anchor == "" {
		return RecurrenceSpec{}, fmt.Errorf("%w: freq, time, timezone and anchor are required", ErrMissingRecurrenceField)
	}

	spec := RecurrenceSpec{
		Freq:       RecurrenceFreq(raw.Freq),
		Interval:   1, // default
		DayOfMonth: 1, // default
		Time:       raw.Time,
		Timezone:   raw.Timezone,
		Anchor:     raw.Anchor,
	}
	if raw.Interval != nil {
		spec.Interval = *raw.Interval
	}
	if raw.Weekdays != nil {
		spec.Weekdays = *raw.Weekdays
	}
	if raw.DayOfMonth != nil {
		spec.DayOfMonth = *raw.DayOfMonth
	}

	if err := spec.validate(); err != nil {
		return RecurrenceSpec{}, err
	}
	return spec, nil
}

// validate checks the spec invariants. It is shared by ParseRecurrence and
// NextOccurrence: NextOccurrence re-validates so the bounded search can rely on
// Interval >= 1 and a non-empty weekly mask even for hand-built specs.
func (s RecurrenceSpec) validate() error {
	switch s.Freq {
	case RecurrenceFreqDaily, RecurrenceFreqWeekly, RecurrenceFreqMonthly:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidRecurrenceFreq, s.Freq)
	}
	if s.Interval < 1 {
		return fmt.Errorf("%w: %d", ErrInvalidRecurrenceInterval, s.Interval)
	}
	if s.Freq == RecurrenceFreqWeekly && (s.Weekdays < 1 || s.Weekdays > weekdaysAll) {
		return fmt.Errorf("%w: mask %d", ErrInvalidRecurrenceWeekdays, s.Weekdays)
	}
	if s.DayOfMonth < 1 || s.DayOfMonth > 31 {
		return fmt.Errorf("%w: %d", ErrInvalidRecurrenceDayOfMonth, s.DayOfMonth)
	}
	if _, _, err := parseRecTime(s.Time); err != nil {
		return err
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("%w: %q", ErrInvalidRecurrenceTimezone, s.Timezone)
	}
	anchor, err := parseRecAnchor(s.Anchor)
	if err != nil {
		return err
	}
	switch s.Freq {
	case RecurrenceFreqWeekly:
		if s.Weekdays&weekdayBit(anchor.Weekday()) == 0 {
			return fmt.Errorf("%w: anchor weekday is not in the weekdays mask", ErrInvalidRecurrenceAnchor)
		}
	case RecurrenceFreqMonthly:
		if anchor.Day() != clampDay(s.DayOfMonth, anchor.Year(), anchor.Month()) {
			return fmt.Errorf("%w: anchor day does not match day_of_month (with clamp)", ErrInvalidRecurrenceAnchor)
		}
	}
	return nil
}

// NextOccurrence returns the minimum calendar occurrence strictly after `after`.
// It is a pure function: no storage, no state, no side effects. Only the spec
// and the reference instant matter, so the result is reproducible across
// restarts and independent of when a fire actually executes.
//
// Callers pick the reference per the S0 advance contract:
//   - ARMAR (NextFireAt == nil): reference = now.
//   - After a successful fire of the occurrence cached in NextFireAt (S1):
//     reference = max(NextFireAt, now) — advance from the scheduled occurrence,
//     skipping any occurrences that were already past (downtime: at most one
//     late fire, no catch-up burst).
//
// The returned instant carries the spec's location; callers convert to UTC when
// persisting a NextFireAt cache.
func NextOccurrence(spec RecurrenceSpec, after time.Time) (time.Time, error) {
	if err := spec.validate(); err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidRecurrenceTimezone, spec.Timezone)
	}
	hh, mm, err := parseRecTime(spec.Time)
	if err != nil {
		return time.Time{}, err
	}
	anchor, err := parseRecAnchorIn(spec.Anchor, loc)
	if err != nil {
		return time.Time{}, err
	}

	local := after.In(loc)
	// Scan from the local calendar day of `after`; every candidate is built as
	// time.Date(y, mo, d, hh, mm, 0, 0, loc), which also resolves DST gaps and
	// repeats exactly as documented above.
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	for i := 0; i <= spec.scanBound(); i++ {
		d := start.AddDate(0, 0, i)
		if !occursOn(spec, anchor, d) {
			continue
		}
		occ := time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, loc)
		if occ.After(after) {
			return occ, nil
		}
		// Equal is excluded (strictly after). A candidate on the reference day
		// whose time already passed is skipped; the loop continues to the next
		// eligible day within the bound.
	}
	return time.Time{}, fmt.Errorf("%w: maxDays=%d", ErrRecurrenceSearchExceeded, spec.scanBound())
}

// occursOn reports whether the candidate date d is on the spec's calendar.
// All arithmetic is civil (calendar-day) arithmetic on the Y/M/D components,
// never wall-clock duration subtraction — DST must not skew phase math.
func occursOn(spec RecurrenceSpec, anchor time.Time, d time.Time) bool {
	switch spec.Freq {
	case RecurrenceFreqDaily:
		return daysBetween(anchor, d)%spec.Interval == 0

	case RecurrenceFreqWeekly:
		anchorMonday := mondayOf(anchor)
		dMonday := mondayOf(d)
		if daysBetween(anchorMonday, dMonday)%(7*spec.Interval) != 0 {
			return false
		}
		return spec.Weekdays&weekdayBit(d.Weekday()) != 0

	case RecurrenceFreqMonthly:
		months := (d.Year()-anchor.Year())*12 + int(d.Month()) - int(anchor.Month())
		if months%spec.Interval != 0 {
			return false
		}
		return d.Day() == clampDay(spec.DayOfMonth, d.Year(), d.Month())

	default:
		return false // unreachable: spec is validated
	}
}

// scanBound returns the maximum number of candidate days the search may need.
// Derived from the membership rules (validated spec: Interval >= 1, weekly mask
// non-empty): the first eligible date is at most Interval days (daily),
// 7*Interval days (weekly) or 31*Interval months' worth of days (monthly) after
// the reference date; the +1/+7/+31 term covers the same-day time-passed case
// (next eligible date one full cycle later). The loop is therefore always
// finite and deterministic.
func (s RecurrenceSpec) scanBound() int {
	switch s.Freq {
	case RecurrenceFreqWeekly:
		return 7*s.Interval + 7
	case RecurrenceFreqMonthly:
		return 31*s.Interval + 31
	default:
		return s.Interval + 1
	}
}

// weekdayBit maps a time.Weekday to the RecurrenceSpec bitmask bit.
func weekdayBit(wd time.Weekday) int {
	if wd == time.Sunday {
		return WeekdaySunday
	}
	return 1 << (wd - 1) // Monday=1, ..., Saturday=32
}

// mondayOf returns the Monday of the ISO (Monday-based) week containing d.
// The weekday is enough to walk back: Sunday wraps to the previous week.
func mondayOf(d time.Time) time.Time {
	back := (int(d.Weekday()) + 6) % 7 // Monday=0 ... Sunday=6
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -back)
}

// daysBetween counts whole calendar days between two date instants using their
// (Y, M, D) components in UTC — immune to DST offsets and 23h/25h days.
func daysBetween(a, b time.Time) int {
	dayA := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	dayB := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(dayB.Sub(dayA) / (24 * time.Hour))
}

// clampDay clamps dayOfMonth to the last day of the given (year, month).
func clampDay(dayOfMonth int, year int, month time.Month) int {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if dayOfMonth > last {
		return last
	}
	return dayOfMonth
}

// parseRecTime strictly parses "HH:MM" (hour 0-23, minute 0-59, one or two
// digit hour, zero-padded minute). It deliberately avoids time.Parse, which
// would accept "24:00".
func parseRecTime(s string) (hour, minute int, err error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%w: %q", ErrInvalidRecurrenceTime, s)
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 || len(parts[1]) != 2 {
		// len(parts[1]) != 2: the minute must be zero-padded ("08:0" is not canonical).
		return 0, 0, fmt.Errorf("%w: %q", ErrInvalidRecurrenceTime, s)
	}
	return h, m, nil
}

// parseRecAnchor strictly parses "YYYY-MM-DD" (zero-padded; a plain
// time.Parse would accept "2026-1-5", which the canonical wire format forbids).
func parseRecAnchor(s string) (time.Time, error) {
	if !isZeroPaddedDate(s) {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidRecurrenceAnchor, s)
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidRecurrenceAnchor, s)
	}
	return t, nil
}

// parseRecAnchorIn parses the anchor into the spec's location (used by
// NextOccurrence so the calendar date lives in the spec's timezone).
func parseRecAnchorIn(s string, loc *time.Location) (time.Time, error) {
	t, err := parseRecAnchor(s)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc), nil
}

// isZeroPaddedDate reports whether s has the exact shape YYYY-MM-DD (10 chars,
// digits, dashes at positions 4 and 7).
func isZeroPaddedDate(s string) bool {
	if len(s) != 10 {
		return false
	}
	for i := 0; i < 10; i++ {
		switch i {
		case 4, 7:
			if s[i] != '-' {
				return false
			}
		default:
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
	}
	return true
}
