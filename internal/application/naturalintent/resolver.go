package naturalintent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// ResolveTime converts a ReminderSpec into a concrete time.Time using the
// current time and the user's timezone. It returns an error when the spec
// cannot be resolved safely (e.g. malformed time, ambiguous date).
//
// Resolution rules:
//   - Relative: now.Add(parsed duration).
//   - AbsoluteTime + AbsoluteDate "today": same day; if the time has already
//     passed, resolve to tomorrow.
//   - AbsoluteTime + AbsoluteDate "tomorrow": next day.
//   - AbsoluteTime + AbsoluteDate "YYYY-MM-DD": that specific date.
//   - AbsoluteTime without AbsoluteDate: defaults to today (with tomorrow
//     fallback if past).
//
// Recurring specs are NOT resolved here: they are built by BuildRecurrenceJSON.
func ResolveTime(spec ReminderSpec, ctx InterpretContext) (time.Time, error) {
	if spec.Relative != "" {
		return resolveRelative(spec.Relative, ctx.Now)
	}
	if spec.AbsoluteTime != "" {
		return resolveAbsolute(spec.AbsoluteTime, spec.AbsoluteDate, ctx)
	}
	return time.Time{}, fmt.Errorf("reminder spec has neither relative nor absolute time")
}

// resolveRelative parses a Go duration string and adds it to now.
func resolveRelative(durStr string, now time.Time) (time.Time, error) {
	d, err := time.ParseDuration(durStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid relative time %q: %w", durStr, err)
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("relative time must be positive, got %q", durStr)
	}
	return now.Add(d), nil
}

// resolveAbsolute parses a local time ("HH:MM") and date expression,
// resolving to a concrete time.Time in the user's timezone.
func resolveAbsolute(timeStr, dateStr string, ctx InterpretContext) (time.Time, error) {
	hour, min, err := parseHHMM(timeStr)
	if err != nil {
		return time.Time{}, err
	}

	// Determine the base date.
	now := ctx.Now.In(ctx.Timezone)
	var baseDate time.Time

	switch {
	case dateStr == "" || dateStr == "today":
		baseDate = time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, ctx.Timezone)
	case dateStr == "tomorrow":
		tomorrow := now.AddDate(0, 0, 1)
		baseDate = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), hour, min, 0, 0, ctx.Timezone)
	default:
		// Try YYYY-MM-DD format.
		d, err := time.ParseInLocation("2006-01-02", dateStr, ctx.Timezone)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid date %q: use 'today', 'tomorrow', or 'YYYY-MM-DD'", dateStr)
		}
		baseDate = time.Date(d.Year(), d.Month(), d.Day(), hour, min, 0, 0, ctx.Timezone)
	}

	// If the resolved time is in the past and we used "today" (or no date),
	// shift to tomorrow. This handles "hoy a las 20h" after 20:00.
	if (dateStr == "" || dateStr == "today") && baseDate.Before(now) {
		tomorrow := now.AddDate(0, 0, 1)
		baseDate = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), hour, min, 0, 0, ctx.Timezone)
	}

	return baseDate, nil
}

// parseHHMM parses "HH:MM" or "HH:MM" with various separators.
// It also handles shorthand like "20h" (implies :00).
// Requires a separator character (h, H, :, ., ,) between hour and minute.
// A bare number like "20" is rejected as ambiguous.
func parseHHMM(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, fmt.Errorf("invalid time: empty string")
	}

	// Check for a separator character before normalizing.
	// This distinguishes "20h" (valid) from "20" (ambiguous).
	hasSep := strings.ContainsAny(s, "hH:.,")

	// Normalize separators: "20h", "20:00", "20.00" → "20:00".
	s = strings.ReplaceAll(s, "h", ":")
	s = strings.ReplaceAll(s, "H", ":")
	s = strings.ReplaceAll(s, ".", ":")
	s = strings.ReplaceAll(s, ",", ":")

	// Remove trailing colon if present (e.g. "20h" → "20:").
	s = strings.TrimRight(s, ":")

	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 0 || parts[0] == "" {
		return 0, 0, fmt.Errorf("invalid time %q: expected HH:MM format", s)
	}

	hour, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour in %q: must be 0-23", s)
	}

	// If no minutes part, default to 0 (e.g. "20h" → 20:00).
	// But only if a separator was present (bare "20" is ambiguous).
	min := 0
	if len(parts) == 2 && parts[1] != "" {
		min, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || min < 0 || min > 59 {
			return 0, 0, fmt.Errorf("invalid minute in %q: must be 0-59", s)
		}
	} else if !hasSep {
		return 0, 0, fmt.Errorf("invalid time %q: expected HH:MM format", s)
	}

	return hour, min, nil
}

// BuildRecurrenceJSON turns the natural-language RecurrenceParams into the
// canonical RecurrenceSpec JSON stored in Trigger.Value (B3 S4). It is the Go
// side of the contract:
//   - timezone always comes from InterpretContext (the user's), never from Pi;
//   - interval defaults to 1;
//   - the anchor is derived when the user gave no explicit start date (daily:
//     today; weekly: today if its weekday is in the mask, else the next
//     compatible day; monthly: the current month with the domain's clamp;
//     yearly: the current year for the given month+day, walking to the next
//     leap year for Feb 29); an explicit anchor year is honored verbatim;
//   - the result is ALWAYS validated with domain.ParseRecurrence — the domain
//     remains the single authority for calendar rules.
func BuildRecurrenceJSON(p RecurrenceParams, ctx InterpretContext) (string, error) {
	if p.Freq == "" {
		return "", fmt.Errorf("recurrence frequency is required")
	}
	switch p.Freq {
	case domain.RecurrenceFreqDaily, domain.RecurrenceFreqWeekly, domain.RecurrenceFreqMonthly, domain.RecurrenceFreqYearly:
	default:
		return "", fmt.Errorf("unsupported recurrence frequency %q", p.Freq)
	}

	// Time is required and normalized to canonical HH:MM.
	hh, mm, err := parseHHMM(p.Time)
	if err != nil {
		return "", err
	}
	timeStr := fmt.Sprintf("%02d:%02d", hh, mm)

	interval := 1
	if p.Interval != nil {
		interval = *p.Interval
		if interval < 1 {
			return "", fmt.Errorf("recurrence interval must be >= 1")
		}
	}

	// NL weekdays (1=monday..7=sunday) -> domain bitmask (Monday=1<<0..Sunday=1<<6).
	weekdaysMask := 0
	for _, wd := range p.Weekdays {
		if wd < 1 || wd > 7 {
			return "", fmt.Errorf("recurrence weekday out of range 1..7")
		}
		weekdaysMask |= 1 << (wd - 1)
	}
	if p.Freq == domain.RecurrenceFreqWeekly && len(p.Weekdays) == 0 {
		return "", fmt.Errorf("weekly recurrence requires weekdays")
	}
	if p.Freq == domain.RecurrenceFreqMonthly && (p.DayOfMonth == nil || *p.DayOfMonth < 1 || *p.DayOfMonth > 31) {
		return "", fmt.Errorf("monthly recurrence requires day_of_month in 1..31")
	}
	if p.Freq == domain.RecurrenceFreqYearly && (p.AnchorMonth == nil || p.AnchorDay == nil) {
		return "", fmt.Errorf("yearly recurrence requires an anchor month and day")
	}

	anchor, err := resolveAnchor(p, weekdaysMask, ctx)
	if err != nil {
		return "", err
	}

	spec := domain.RecurrenceSpec{
		Freq:       p.Freq,
		Interval:   interval,
		Weekdays:   weekdaysMask,
		DayOfMonth: 1, // default; overwritten below for monthly
		Time:       timeStr,
		Timezone:   ctx.Timezone.String(),
		Anchor:     anchor,
	}
	if p.DayOfMonth != nil {
		spec.DayOfMonth = *p.DayOfMonth
	}

	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	// The domain constructor is the final authority: a spec that does not
	// validate is rejected before anything is persisted.
	if _, err := domain.ParseRecurrence(string(data)); err != nil {
		return "", err
	}
	return string(data), nil
}

// resolveAnchor derives or completes the canonical YYYY-MM-DD anchor (years are
// ALWAYS completed by Go from InterpretContext.Now; Pi never provides them).
func resolveAnchor(p RecurrenceParams, weekdaysMask int, ctx InterpretContext) (string, error) {
	now := ctx.Now.In(ctx.Timezone)

	// Explicit start date components from the user message.
	if p.AnchorMonth != nil && p.AnchorDay != nil {
		year := now.Year()
		if p.AnchorYear != nil {
			year = *p.AnchorYear
		}
		// Feb 29 only exists in leap years: walk to the next valid leap year.
		if *p.AnchorMonth == 2 && *p.AnchorDay == 29 {
			for !isLeapYear(year) {
				year++
			}
		}
		return fmt.Sprintf("%04d-%02d-%02d", year, *p.AnchorMonth, *p.AnchorDay), nil
	}
	if p.AnchorYear != nil {
		return "", fmt.Errorf("recurrence anchor year requires an anchor month and day")
	}

	switch p.Freq {
	case domain.RecurrenceFreqDaily:
		return dateString(now), nil

	case domain.RecurrenceFreqWeekly:
		// Today if its weekday is in the mask, otherwise the next compatible day.
		d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, ctx.Timezone)
		for i := 0; i < 7; i++ {
			if weekdaysMask&nlWeekdayBit(d.Weekday()) != 0 {
				return dateString(d), nil
			}
			d = d.AddDate(0, 0, 1)
		}
		return "", fmt.Errorf("weekly anchor derivation failed")

	case domain.RecurrenceFreqMonthly:
		// Current month, day_of_month clamped to the month's last day (the
		// domain's monthly clamp semantics, so the anchor always validates).
		day := 1
		if p.DayOfMonth != nil {
			day = *p.DayOfMonth
		}
		if last := daysInMonth(now.Year(), int(now.Month())); day > last {
			day = last
		}
		return fmt.Sprintf("%04d-%02d-%02d", now.Year(), int(now.Month()), day), nil

	case domain.RecurrenceFreqYearly:
		return "", fmt.Errorf("yearly recurrence requires an anchor month and day")
	}
	return "", fmt.Errorf("unsupported recurrence frequency %q", p.Freq)
}

// dateString formats a time as the canonical YYYY-MM-DD anchor string.
func dateString(t time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
}

// isLeapYear reports whether y is a leap year per the proleptic Gregorian rules.
// It is plain civil arithmetic needed to complete a Feb-29 anchor year; the
// recurrence calendar rules themselves stay exclusively in the domain.
func isLeapYear(y int) bool {
	return y%4 == 0 && (y%100 != 0 || y%400 == 0)
}

// daysInMonth returns the number of days in the given (year, month).
func daysInMonth(year int, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// nlWeekdayBit maps a time.Weekday to the domain weekday bitmask bit. It mirrors
// the domain's bit encoding (Monday=1<<0 .. Sunday=1<<6): the NL contract maps
// 1=monday..7=sunday onto exactly those bits, so the two layers stay compatible
// without duplicating any calendar rule.
func nlWeekdayBit(wd time.Weekday) int {
	if wd == time.Sunday {
		return 1 << 6 // domain.WeekdaySunday
	}
	return 1 << (wd - 1) // Monday=1<<0 .. Saturday=1<<5
}
