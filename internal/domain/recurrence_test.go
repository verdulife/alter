package domain

import (
	"errors"
	"testing"
	"time"
)

// madrid is the IANA location used by DST tests (Europe/Madrid).
var madrid = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		panic(err)
	}
	return loc
}()

// mustParse parses a valid recurrence JSON or fails the test.
func mustParse(t *testing.T, s string) RecurrenceSpec {
	t.Helper()
	spec, err := ParseRecurrence(s)
	if err != nil {
		t.Fatalf("ParseRecurrence(%q) unexpected error: %v", s, err)
	}
	return spec
}

// walkOccurrences returns the first n occurrences of spec strictly after from,
// walking the calendar by repeated NextOccurrence calls (the cadence advance).
func walkOccurrences(t *testing.T, spec RecurrenceSpec, from time.Time, n int) []time.Time {
	t.Helper()
	out := make([]time.Time, 0, n)
	next := from
	for i := 0; i < n; i++ {
		occ, err := NextOccurrence(spec, next)
		if err != nil {
			t.Fatalf("NextOccurrence walk %d: %v", i, err)
		}
		out = append(out, occ)
		next = occ
	}
	return out
}

// expectEquals asserts got equals want as instants (ignoring location).
func expectEquals(t *testing.T, name string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s: want %s got %s", name, want.UTC().Format(time.RFC3339Nano), got.UTC().Format(time.RFC3339Nano))
	}
}

// expectErr asserts err is (wraps) the named sentinel.
func expectErr(t *testing.T, name string, err error, sentinel error) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Errorf("%s: expected %v, got %v", name, sentinel, err)
	}
}

// --- ParseRecurrence: valid specs ---

func TestParseRecurrenceDailyDefaults(t *testing.T) {
	spec := mustParse(t, `{"freq":"daily","time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`)
	if spec.Freq != RecurrenceFreqDaily {
		t.Errorf("Freq: want daily, got %q", spec.Freq)
	}
	if spec.Interval != 1 {
		t.Errorf("Interval: want default 1, got %d", spec.Interval)
	}
	if spec.Weekdays != 0 {
		t.Errorf("Weekdays: want 0 (unused), got %d", spec.Weekdays)
	}
	if spec.DayOfMonth != 1 {
		t.Errorf("DayOfMonth: want default 1, got %d", spec.DayOfMonth)
	}
	if spec.Time != "08:00" || spec.Timezone != "UTC" || spec.Anchor != "2026-01-05" {
		t.Errorf("unexpected spec fields: %+v", spec)
	}
}

func TestParseRecurrenceWeeklyExplicit(t *testing.T) {
	spec := mustParse(t, `{"freq":"weekly","interval":2,"weekdays":5,"time":"09:00","timezone":"Europe/Madrid","anchor":"2026-01-05"}`)
	if spec.Freq != RecurrenceFreqWeekly || spec.Interval != 2 || spec.Weekdays != (WeekdayMonday|WeekdayWednesday) {
		t.Errorf("unexpected weekly spec: %+v", spec)
	}
	if spec.Time != "09:00" || spec.Timezone != "Europe/Madrid" || spec.Anchor != "2026-01-05" {
		t.Errorf("unexpected spec fields: %+v", spec)
	}
}

// --- ParseRecurrence: validation errors ---

func TestParseRecurrenceErrors(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		sentinel error
	}{
		{"broken json", `{"freq":"daily","time":"08:00","timezone":"UTC","anchor":"2026-01-05"`, ErrInvalidRecurrenceJSON},
		{"not json", `not json`, ErrInvalidRecurrenceJSON},
		{"empty", ``, ErrInvalidRecurrenceJSON},
		{"trailing data", `{"freq":"daily","time":"08:00","timezone":"UTC","anchor":"2026-01-05"}{}`, ErrInvalidRecurrenceJSON},
		{"unknown field", `{"freq":"daily","time":"08:00","timezone":"UTC","anchor":"2026-01-05","bogus":1}`, ErrRecurrenceUnknownField},
		{"missing freq", `{"time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`, ErrMissingRecurrenceField},
		{"missing time", `{"freq":"daily","timezone":"UTC","anchor":"2026-01-05"}`, ErrMissingRecurrenceField},
		{"unknown freq", `{"freq":"yearly","time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceFreq},
		{"interval zero", `{"freq":"daily","interval":0,"time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceInterval},
		{"interval negative", `{"freq":"daily","interval":-2,"time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceInterval},
		{"weekly empty mask", `{"freq":"weekly","time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceWeekdays},
		{"weekly mask out of range", `{"freq":"weekly","weekdays":256,"time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceWeekdays},
		{"day_of_month zero", `{"freq":"monthly","day_of_month":0,"time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceDayOfMonth},
		{"day_of_month out of range", `{"freq":"monthly","day_of_month":32,"time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceDayOfMonth},
		{"time hour out of range", `{"freq":"daily","time":"25:00","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceTime},
		{"time minute out of range", `{"freq":"daily","time":"08:60","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceTime},
		{"time missing minutes", `{"freq":"daily","time":"08","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceTime},
		{"time single digit minute", `{"freq":"daily","time":"08:0","timezone":"UTC","anchor":"2026-01-05"}`, ErrInvalidRecurrenceTime},
		{"bad timezone", `{"freq":"daily","time":"08:00","timezone":"Mars/Olympus","anchor":"2026-01-05"}`, ErrInvalidRecurrenceTimezone},
		{"anchor not zero padded", `{"freq":"daily","time":"08:00","timezone":"UTC","anchor":"2026-1-5"}`, ErrInvalidRecurrenceAnchor},
		{"anchor month out of range", `{"freq":"daily","time":"08:00","timezone":"UTC","anchor":"2026-13-40"}`, ErrInvalidRecurrenceAnchor},
		{"anchor day out of range", `{"freq":"daily","time":"08:00","timezone":"UTC","anchor":"2026-02-30"}`, ErrInvalidRecurrenceAnchor},
		{"weekly anchor not in mask", `{"freq":"weekly","weekdays":5,"time":"08:00","timezone":"UTC","anchor":"2026-01-08"}`, ErrInvalidRecurrenceAnchor},
		{"monthly anchor not clamped day", `{"freq":"monthly","day_of_month":31,"time":"08:00","timezone":"UTC","anchor":"2026-02-15"}`, ErrInvalidRecurrenceAnchor},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseRecurrence(c.input)
			expectErr(t, c.name, err, c.sentinel)
		})
	}
}

func TestParseRecurrenceWeeklyAnchorInMaskOK(t *testing.T) {
	// anchor Wednesday IS in the Mon|Wed mask -> valid.
	mustParse(t, `{"freq":"weekly","weekdays":5,"time":"08:00","timezone":"UTC","anchor":"2026-01-07"}`)
}

func TestParseRecurrenceMonthlyAnchorClampedOK(t *testing.T) {
	// day_of_month 31 with anchor 2026-02-28: February clamps to 28 -> valid.
	mustParse(t, `{"freq":"monthly","day_of_month":31,"time":"08:00","timezone":"UTC","anchor":"2026-02-28"}`)
	// and a regular day match
	mustParse(t, `{"freq":"monthly","day_of_month":10,"time":"08:00","timezone":"UTC","anchor":"2026-01-10"}`)
}

// --- NextOccurrence: daily ---

func TestNextOccurrenceDailyBase(t *testing.T) {
	spec := mustParse(t, `{"freq":"daily","time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`)
	cases := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{"before time of day", time.Date(2026, 1, 5, 7, 0, 0, 0, time.UTC), time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)},
		{"after time of day", time.Date(2026, 1, 5, 8, 30, 0, 0, time.UTC), time.Date(2026, 1, 6, 8, 0, 0, 0, time.UTC)},
		{"strictly after exactly on occurrence", time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC), time.Date(2026, 1, 6, 8, 0, 0, 0, time.UTC)},
		{"reference before anchor", time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 12, 1, 8, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextOccurrence(spec, c.after)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expectEquals(t, "occurrence", got, c.want)
		})
	}
}

func TestNextOccurrenceDailyInterval(t *testing.T) {
	// every 3 days, anchored to 2026-01-05 -> 05, 08, 11, 14, ...
	spec := mustParse(t, `{"freq":"daily","interval":3,"time":"08:00","timezone":"UTC","anchor":"2026-01-05"}`)
	cases := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{"day after anchor", time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 8, 8, 0, 0, 0, time.UTC)},
		{"skips ineligible day", time.Date(2026, 1, 9, 9, 0, 0, 0, time.UTC), time.Date(2026, 1, 11, 8, 0, 0, 0, time.UTC)},
		{"strictly after on occurrence", time.Date(2026, 1, 11, 8, 0, 0, 0, time.UTC), time.Date(2026, 1, 14, 8, 0, 0, 0, time.UTC)},
		{"same phase future day", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextOccurrence(spec, c.after)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expectEquals(t, "occurrence", got, c.want)
		})
	}
}

// --- NextOccurrence: weekly ---

func TestNextOccurrenceWeeklyMultiDay(t *testing.T) {
	// every week on Monday|Wednesday at 09:00, anchor Monday 2026-01-05.
	spec := mustParse(t, `{"freq":"weekly","weekdays":5,"time":"09:00","timezone":"UTC","anchor":"2026-01-05"}`)
	cases := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{"monday exact", time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC), time.Date(2026, 1, 7, 9, 0, 0, 0, time.UTC)},
		{"wednesday exact", time.Date(2026, 1, 7, 9, 0, 0, 0, time.UTC), time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)},
		{"mid week", time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)},
		{"sunday", time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextOccurrence(spec, c.after)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expectEquals(t, "occurrence", got, c.want)
		})
	}
}

func TestNextOccurrenceWeeklyIntervalStability(t *testing.T) {
	// every 2 weeks on Monday|Wednesday at 09:00, anchor Monday 2026-01-05.
	// Calendar: 01-05, 01-07, 01-19, 01-21, 02-02, 02-04, 02-16, 02-18, 03-02, ...
	spec := mustParse(t, `{"freq":"weekly","interval":2,"weekdays":5,"time":"09:00","timezone":"UTC","anchor":"2026-01-05"}`)

	// The calendar is a pure function of the spec: walking from two different
	// "now" references must produce the same absolute sequence (no phase drift,
	// no dependence on LastFiredAt or on the reference used to start the walk).
	fromA := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fromB := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	a := walkOccurrences(t, spec, fromA, 8)
	b := walkOccurrences(t, spec, fromB, 8)

	wantA := []time.Time{
		time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 7, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 19, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 21, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 2, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 4, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 16, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 18, 9, 0, 0, 0, time.UTC),
	}
	for i := range wantA {
		expectEquals(t, "walk A", a[i], wantA[i])
	}

	// Walk B (started later) must equal the tail of the calendar.
	wantB := []time.Time{
		time.Date(2026, 1, 19, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 21, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 2, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 4, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 16, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 18, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC),
	}
	for i := range wantB {
		expectEquals(t, "walk B", b[i], wantB[i])
	}

	// Negative-span phase: a reference before the anchor must still hit the
	// same calendar. The week of 2025-12-22 is phase-0 (Monday span -14 days),
	// so its Monday (12-22) and Wednesday (12-24) are occurrences; the next
	// after 2025-12-25 is the anchor Monday 2026-01-05.
	got, err := NextOccurrence(spec, time.Date(2025, 12, 25, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectEquals(t, "negative span", got, time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC))
}

// --- NextOccurrence: monthly ---

func TestNextOccurrenceMonthlyClamp(t *testing.T) {
	// day 31 every month at 10:00, anchor 2026-01-31.
	spec := mustParse(t, `{"freq":"monthly","day_of_month":31,"time":"10:00","timezone":"UTC","anchor":"2026-01-31"}`)
	cases := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{"january exact -> february clamp", time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC), time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC)},
		{"february exact -> march", time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC)},
		{"april clamp to 30", time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)},
		{"september clamp to 30", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextOccurrence(spec, c.after)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expectEquals(t, "occurrence", got, c.want)
		})
	}
}

func TestNextOccurrenceMonthlyLeapYear(t *testing.T) {
	// day 31 anchored to a leap-year January -> February 29 (2024 is a leap year).
	spec := mustParse(t, `{"freq":"monthly","day_of_month":31,"time":"10:00","timezone":"UTC","anchor":"2024-01-31"}`)
	cases := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{"leap february", time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 2, 29, 10, 0, 0, 0, time.UTC)},
		{"leap february exact -> march", time.Date(2024, 2, 29, 10, 0, 0, 0, time.UTC), time.Date(2024, 3, 31, 10, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextOccurrence(spec, c.after)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expectEquals(t, "occurrence", got, c.want)
		})
	}
}

func TestNextOccurrenceMonthlyInterval(t *testing.T) {
	// day 31 every 2 months, anchor 2026-01-31 -> Jan, Mar, May, Jul, Sep, Nov.
	spec := mustParse(t, `{"freq":"monthly","interval":2,"day_of_month":31,"time":"10:00","timezone":"UTC","anchor":"2026-01-31"}`)
	cases := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{"skips february", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC)},
		{"skips april", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)},
		{"september clamp in month", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)},
		{"november 30", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 30, 10, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextOccurrence(spec, c.after)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expectEquals(t, "occurrence", got, c.want)
		})
	}
}

func TestNextOccurrenceMonthlyFixedDay(t *testing.T) {
	spec := mustParse(t, `{"freq":"monthly","day_of_month":10,"time":"10:00","timezone":"UTC","anchor":"2026-01-10"}`)
	got, err := NextOccurrence(spec, time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectEquals(t, "next month", got, time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC))
}

// --- NextOccurrence: timezone and DST ---

func TestNextOccurrenceDSTSpring(t *testing.T) {
	// EU spring-forward 2026-03-29 02:00 -> 03:00 (+01 -> +02).
	// A daily 08:00 Madrid stays at 08:00 local: 2026-03-29 08:00 CEST = 06:00 UTC.
	spec := mustParse(t, `{"freq":"daily","time":"08:00","timezone":"Europe/Madrid","anchor":"2026-01-05"}`)
	after := time.Date(2026, 3, 28, 8, 0, 0, 0, madrid) // 07:00 UTC, still CET
	got, err := NextOccurrence(spec, after)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectEquals(t, "08:00 spring day", got, time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC))
	if w := got.In(madrid).Format("15:04"); w != "08:00" {
		t.Errorf("wall clock: want 08:00, got %s", w)
	}
}

func TestNextOccurrenceDSTNonexistentLocalTime(t *testing.T) {
	// A daily 02:30 Madrid does not exist on 2026-03-29 (02:00->03:00 gap).
	// Contract: the occurrence is exactly what time.Date produces for that
	// (date, time) in the spec location — Go's normalization (02:30 -> 03:30
	// CEST), documented as the resolution rule for nonexistent local times.
	spec := mustParse(t, `{"freq":"daily","time":"02:30","timezone":"Europe/Madrid","anchor":"2026-01-05"}`)
	after := time.Date(2026, 3, 29, 0, 0, 0, 0, madrid)
	got, err := NextOccurrence(spec, after)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 3, 29, 2, 30, 0, 0, madrid)
	expectEquals(t, "normalized occurrence", got, want)
	if h := got.In(madrid).Hour(); h != 3 {
		t.Errorf("normalized wall hour: want 3 (02:30 shifted over the gap), got %d", h)
	}
}

func TestNextOccurrenceDSTAutumn(t *testing.T) {
	// EU fall-back 2026-10-25 03:00 -> 02:00 (+02 -> +01).
	spec := mustParse(t, `{"freq":"daily","time":"08:00","timezone":"Europe/Madrid","anchor":"2026-01-05"}`)
	// The fall-back transition (2026-10-25 03:00 -> 02:00, +02 -> +01) happens
	// BEFORE 08:00, so the 2026-10-25 08:00 occurrence is already CET = 07:00
	// UTC. The wall clock stays 08:00: that is the stability contract.
	after := time.Date(2026, 10, 24, 8, 0, 0, 0, madrid)
	got, err := NextOccurrence(spec, after)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectEquals(t, "08:00 after fall-back", got, time.Date(2026, 10, 25, 7, 0, 0, 0, time.UTC))
	if w := got.In(madrid).Format("15:04"); w != "08:00" {
		t.Errorf("wall clock: want 08:00, got %s", w)
	}
}

func TestNextOccurrenceDSTAmbiguousLocalTime(t *testing.T) {
	// A daily 02:30 Madrid is ambiguous on 2026-10-25 (02:30 occurs twice:
	// CEST +02 then CET +01). Contract: the result is exactly what time.Date
	// produces for that (date, time) in the spec location (Go's documented
	// choice: one of the two instants, the earlier one as of Go 1.25) and the
	// wall clock stays 02:30.
	spec := mustParse(t, `{"freq":"daily","time":"02:30","timezone":"Europe/Madrid","anchor":"2026-01-05"}`)
	after := time.Date(2026, 10, 25, 0, 0, 0, 0, madrid)
	got, err := NextOccurrence(spec, after)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 10, 25, 2, 30, 0, 0, madrid)
	expectEquals(t, "ambiguous occurrence", got, want)
	if w := got.In(madrid).Format("15:04"); w != "02:30" {
		t.Errorf("wall clock: want 02:30, got %s", w)
	}
	_, offset := got.Zone()
	if offset != 3600 && offset != 7200 {
		t.Errorf("offset: want +01:00 or +02:00, got %d", offset)
	}
}

func TestNextOccurrenceDSTDailyStability(t *testing.T) {
	// The calendar is wall-clock based: a daily 08:00 Madrid occurrence is
	// always 08:00 local, so its UTC instant shifts across transitions.
	spec := mustParse(t, `{"freq":"daily","time":"08:00","timezone":"Europe/Madrid","anchor":"2026-01-05"}`)

	check := func(t *testing.T, from time.Time, wantOffsets map[string]int, days int) {
		t.Helper()
		prev := from
		for i := 0; i < days; i++ {
			occ, err := NextOccurrence(spec, prev)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if w := occ.In(madrid).Format("15:04"); w != "08:00" {
				t.Errorf("wall clock: want 08:00, got %s (%s)", w, occ.In(madrid))
			}
			key := occ.In(madrid).Format("2006-01-02")
			if wantOff, ok := wantOffsets[key]; ok {
				_, off := occ.Zone()
				if off != wantOff {
					t.Errorf("%s: offset: want %d, got %d", key, wantOff, off)
				}
			}
			prev = occ
		}
	}

	// 2026-03-27 08:00 CET; the following days stay 08:00 local with the
	// offset flipping +01 -> +02 on the 29th.
	check(t, time.Date(2026, 3, 27, 8, 0, 0, 0, madrid), map[string]int{
		"2026-03-28": 3600 * 1,
		"2026-03-29": 3600 * 2,
		"2026-03-30": 3600 * 2,
	}, 3)

	// 2026-10-23 08:00 CEST; the offset flips +02 -> +01 on the 25th.
	check(t, time.Date(2026, 10, 23, 8, 0, 0, 0, madrid), map[string]int{
		"2026-10-24": 3600 * 2,
		"2026-10-25": 3600 * 1,
		"2026-10-26": 3600 * 1,
	}, 3)
}

// --- NextOccurrence: total function on invalid specs ---

func TestNextOccurrenceRejectsInvalidSpec(t *testing.T) {
	// A hand-built spec that never passed ParseRecurrence must still fail
	// cleanly (the search bound depends on validation, so NextOccurrence
	// re-validates before scanning).
	spec := RecurrenceSpec{
		Freq:     RecurrenceFreqDaily,
		Interval: 0, // invalid: would break the bounded search
		Time:     "08:00",
		Timezone: "UTC",
		Anchor:   "2026-01-05",
	}
	_, err := NextOccurrence(spec, time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC))
	expectErr(t, "invalid interval", err, ErrInvalidRecurrenceInterval)
}
