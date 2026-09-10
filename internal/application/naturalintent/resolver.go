package naturalintent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
