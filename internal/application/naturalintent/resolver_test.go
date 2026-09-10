package naturalintent

import (
	"strings"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

func TestResolveRelative(t *testing.T) {
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	ctx := InterpretContext{Now: now, Timezone: time.UTC}

	tests := []struct {
		name    string
		spec    ReminderSpec
		want    time.Time
		wantErr bool
	}{
		{
			name: "30 minutes",
			spec: ReminderSpec{Relative: "30m"},
			want: now.Add(30 * time.Minute),
		},
		{
			name: "1 hour",
			spec: ReminderSpec{Relative: "1h"},
			want: now.Add(1 * time.Hour),
		},
		{
			name: "2 hours 30 minutes",
			spec: ReminderSpec{Relative: "2h30m"},
			want: now.Add(2*time.Hour + 30*time.Minute),
		},
		{
			name:    "invalid duration",
			spec:    ReminderSpec{Relative: "not_a_duration"},
			wantErr: true,
		},
		{
			name:    "negative duration",
			spec:    ReminderSpec{Relative: "-30m"},
			wantErr: true,
		},
		{
			name:    "zero duration",
			spec:    ReminderSpec{Relative: "0s"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveTime(tt.spec, ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveTime() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !got.Equal(tt.want) {
				t.Errorf("ResolveTime() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveAbsoluteToday(t *testing.T) {
	// 14:00 UTC, requesting 20:00 → same day
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	ctx := InterpretContext{Now: now, Timezone: time.UTC}

	spec := ReminderSpec{AbsoluteTime: "20:00", AbsoluteDate: "today"}
	got, err := ResolveTime(spec, ctx)
	if err != nil {
		t.Fatalf("ResolveTime() error = %v", err)
	}

	want := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ResolveTime() = %v, want %v", got, want)
	}
}

func TestResolveAbsoluteTodayPastShiftsToTomorrow(t *testing.T) {
	// 21:00 UTC, requesting 20:00 → already passed → tomorrow
	now := time.Date(2026, 9, 10, 21, 0, 0, 0, time.UTC)
	ctx := InterpretContext{Now: now, Timezone: time.UTC}

	spec := ReminderSpec{AbsoluteTime: "20:00", AbsoluteDate: "today"}
	got, err := ResolveTime(spec, ctx)
	if err != nil {
		t.Fatalf("ResolveTime() error = %v", err)
	}

	want := time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ResolveTime() = %v, want %v (should shift to tomorrow when today's time has passed)", got, want)
	}
}

func TestResolveAbsoluteTomorrow(t *testing.T) {
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	ctx := InterpretContext{Now: now, Timezone: time.UTC}

	spec := ReminderSpec{AbsoluteTime: "09:00", AbsoluteDate: "tomorrow"}
	got, err := ResolveTime(spec, ctx)
	if err != nil {
		t.Fatalf("ResolveTime() error = %v", err)
	}

	want := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ResolveTime() = %v, want %v", got, want)
	}
}

func TestResolveAbsoluteSpecificDate(t *testing.T) {
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	ctx := InterpretContext{Now: now, Timezone: time.UTC}

	spec := ReminderSpec{AbsoluteTime: "15:30", AbsoluteDate: "2026-09-15"}
	got, err := ResolveTime(spec, ctx)
	if err != nil {
		t.Fatalf("ResolveTime() error = %v", err)
	}

	want := time.Date(2026, 9, 15, 15, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ResolveTime() = %v, want %v", got, want)
	}
}

func TestResolveAbsoluteNoDateDefaultsToToday(t *testing.T) {
	// No date specified → defaults to today (with tomorrow fallback if past)
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	ctx := InterpretContext{Now: now, Timezone: time.UTC}

	spec := ReminderSpec{AbsoluteTime: "18:00"}
	got, err := ResolveTime(spec, ctx)
	if err != nil {
		t.Fatalf("ResolveTime() error = %v", err)
	}

	want := time.Date(2026, 9, 10, 18, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ResolveTime() = %v, want %v", got, want)
	}
}

func TestResolveAbsoluteWithTimezone(t *testing.T) {
	// User is in Buenos Aires (UTC-3)
	// 14:00 UTC = 11:00 ART
	// Requesting 20:00 ART today
	buenosAires, _ := time.LoadLocation("America/Argentina/Buenos_Aires")
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	ctx := InterpretContext{Now: now, Timezone: buenosAires}

	spec := ReminderSpec{AbsoluteTime: "20:00", AbsoluteDate: "today"}
	got, err := ResolveTime(spec, ctx)
	if err != nil {
		t.Fatalf("ResolveTime() error = %v", err)
	}

	// 20:00 ART = 23:00 UTC on Sep 10
	want := time.Date(2026, 9, 10, 23, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ResolveTime() = %v, want %v", got, want)
	}
}

func TestResolveAbsoluteWithTimezoneShiftsToTomorrow(t *testing.T) {
	// Buenos Aires (UTC-3)
	// 14:00 UTC = 11:00 ART
	// Requesting 09:00 ART today → already passed (11:00 > 09:00) → tomorrow
	buenosAires, _ := time.LoadLocation("America/Argentina/Buenos_Aires")
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	ctx := InterpretContext{Now: now, Timezone: buenosAires}

	spec := ReminderSpec{AbsoluteTime: "09:00", AbsoluteDate: "today"}
	got, err := ResolveTime(spec, ctx)
	if err != nil {
		t.Fatalf("ResolveTime() error = %v", err)
	}

	// 09:00 ART tomorrow = 12:00 UTC on Sep 11
	want := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ResolveTime() = %v, want %v", got, want)
	}
}

func TestParseHHMMFormats(t *testing.T) {
	tests := []struct {
		input   string
		wantH   int
		wantM   int
		wantErr bool
	}{
		{"20:00", 20, 0, false},
		{"09:30", 9, 30, false},
		{"20h00", 20, 0, false},
		{"20h", 20, 0, false},
		{"20.00", 20, 0, false},
		{"20,30", 20, 30, false},
		{" 20:00 ", 20, 0, false},
		{"25:00", 0, 0, true}, // invalid hour
		{"20:60", 0, 0, true}, // invalid minute
		{"abc", 0, 0, true},   // not a time
		{"20", 0, 0, true},    // missing minutes
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			h, m, err := parseHHMM(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseHHMM(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && (h != tt.wantH || m != tt.wantM) {
				t.Errorf("parseHHMM(%q) = (%d, %d), want (%d, %d)", tt.input, h, m, tt.wantH, tt.wantM)
			}
		})
	}
}

func TestResolveTimeNoSpec(t *testing.T) {
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}
	spec := ReminderSpec{} // empty
	_, err := ResolveTime(spec, ctx)
	if err == nil {
		t.Error("ResolveTime() should error on empty spec")
	}
}

func TestResolveRelativeExactMinute(t *testing.T) {
	// Verify exact minute arithmetic (no seconds drift)
	now := time.Date(2026, 9, 10, 14, 30, 45, 123456789, time.UTC)
	ctx := InterpretContext{Now: now, Timezone: time.UTC}

	spec := ReminderSpec{Relative: "15m"}
	got, err := ResolveTime(spec, ctx)
	if err != nil {
		t.Fatalf("ResolveTime() error = %v", err)
	}

	// Go's Add handles sub-second precision correctly
	want := now.Add(15 * time.Minute)
	if !got.Equal(want) {
		t.Errorf("ResolveTime() = %v, want %v", got, want)
	}
}

// --- Recurrence: BuildRecurrenceJSON -----------------------------------------

// recurParams is a helper building per-test recurrence params with sane defaults.
func recurParams(freq domain.RecurrenceFreq, timeStr string) RecurrenceParams {
	return RecurrenceParams{Freq: freq, Time: timeStr}
}

func TestBuildRecurrenceJSONDaily(t *testing.T) {
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}

	got, err := BuildRecurrenceJSON(recurParams(domain.RecurrenceFreqDaily, "21:00"), ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"daily","interval":1,"weekdays":0,"day_of_month":1,"time":"21:00","timezone":"UTC","anchor":"2026-09-10"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
	// The canonical value must always pass the domain constructor.
	if _, err := domain.ParseRecurrence(got); err != nil {
		t.Errorf("canonical JSON rejected by ParseRecurrence: %v", err)
	}
}

func TestBuildRecurrenceJSONWeeklyTodayInMask(t *testing.T) {
	// 2026-09-10 is a Thursday (4). Weekdays [4] -> today is compatible.
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqWeekly, "09:00")
	p.Weekdays = []int{4}

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"weekly","interval":1,"weekdays":8,"day_of_month":1,"time":"09:00","timezone":"UTC","anchor":"2026-09-10"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONWeeklyNextCompatibleDay(t *testing.T) {
	// 2026-09-10 is a Thursday; weekdays [6] (Saturday) -> next Saturday is the 12th.
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqWeekly, "09:00")
	p.Weekdays = []int{6}

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"weekly","interval":1,"weekdays":32,"day_of_month":1,"time":"09:00","timezone":"UTC","anchor":"2026-09-12"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONWeeklyMultiDayMask(t *testing.T) {
	// Monday (1) + Wednesday (3) = mask bits 1|4 = 5. Thursday 2026-09-10 is
	// not in the mask, so the next compatible day is the following Monday (14th).
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqWeekly, "09:30")
	p.Weekdays = []int{1, 3}

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"weekly","interval":1,"weekdays":5,"day_of_month":1,"time":"09:30","timezone":"UTC","anchor":"2026-09-14"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONMonthlyClamp(t *testing.T) {
	// Day 31 in February 2026 (not a leap year) clamps to the 28th: the anchor
	// month is the current month, so the anchor day follows the domain clamp.
	ctx := InterpretContext{Now: time.Date(2026, 2, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqMonthly, "08:00")
	day := 31
	p.DayOfMonth = &day

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"monthly","interval":1,"weekdays":0,"day_of_month":31,"time":"08:00","timezone":"UTC","anchor":"2026-02-28"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONMonthlyLeapClamp(t *testing.T) {
	// February 2024 is a leap year: day 31 clamps to the 29th.
	ctx := InterpretContext{Now: time.Date(2024, 2, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqMonthly, "08:00")
	day := 31
	p.DayOfMonth = &day

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"monthly","interval":1,"weekdays":0,"day_of_month":31,"time":"08:00","timezone":"UTC","anchor":"2024-02-29"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONYearlyCurrentYear(t *testing.T) {
	// "cada año el 10 de septiembre": Go completes the year from Now (2026).
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqYearly, "09:00")
	m, d := 9, 10
	p.AnchorMonth, p.AnchorDay = &m, &d

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"yearly","interval":1,"weekdays":0,"day_of_month":1,"time":"09:00","timezone":"UTC","anchor":"2026-09-10"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONYearlyLeapDay(t *testing.T) {
	// Feb 29 in a non-leap year: Go walks forward to the next leap year (2028).
	ctx := InterpretContext{Now: time.Date(2025, 7, 1, 12, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqYearly, "09:00")
	m, d := 2, 29
	p.AnchorMonth, p.AnchorDay = &m, &d

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"yearly","interval":1,"weekdays":0,"day_of_month":1,"time":"09:00","timezone":"UTC","anchor":"2028-02-29"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONYearlyLeapDayCurrentLeapYear(t *testing.T) {
	// 2024 is a leap year: the anchor stays in the current year.
	ctx := InterpretContext{Now: time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqYearly, "09:00")
	m, d := 2, 29
	p.AnchorMonth, p.AnchorDay = &m, &d

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"yearly","interval":1,"weekdays":0,"day_of_month":1,"time":"09:00","timezone":"UTC","anchor":"2024-02-29"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONIntervalAndExplicitYear(t *testing.T) {
	// interval 2 + explicit anchor year 2027 ("a partir de 2027").
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqYearly, "10:30")
	iv, y, m, d := 2, 2027, 3, 15
	p.Interval, p.AnchorYear, p.AnchorMonth, p.AnchorDay = &iv, &y, &m, &d

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"yearly","interval":2,"weekdays":0,"day_of_month":1,"time":"10:30","timezone":"UTC","anchor":"2027-03-15"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONYearlyExplicitAnchorYearRespected(t *testing.T) {
	// «cada 2 años a partir de 2028 el 10 de septiembre»: the explicit anchor
	// year wins over InterpretContext.Now (2026), even when the current year is
	// different.
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqYearly, "09:00")
	iv, y, m, d := 2, 2028, 9, 10
	p.Interval, p.AnchorYear, p.AnchorMonth, p.AnchorDay = &iv, &y, &m, &d

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"yearly","interval":2,"weekdays":0,"day_of_month":1,"time":"09:00","timezone":"UTC","anchor":"2028-09-10"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONYearlyLeapDayExplicitNonLeapYear(t *testing.T) {
	// «cada año el 29 de febrero a partir de 2025»: 2025 is not a leap year, so
	// the anchor walks to the next valid leap year (2028), exactly like the
	// derived-year case — the explicit year does not change the Feb-29 rule.
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	p := recurParams(domain.RecurrenceFreqYearly, "09:00")
	y, m, d := 2025, 2, 29
	p.AnchorYear, p.AnchorMonth, p.AnchorDay = &y, &m, &d

	got, err := BuildRecurrenceJSON(p, ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	want := `{"freq":"yearly","interval":1,"weekdays":0,"day_of_month":1,"time":"09:00","timezone":"UTC","anchor":"2028-02-29"}`
	if got != want {
		t.Errorf("canonical JSON = %s, want %s", got, want)
	}
}

func TestBuildRecurrenceJSONUserTimezone(t *testing.T) {
	// The timezone always comes from Go (InterpretContext), never from Pi.
	ba, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		t.Fatal(err)
	}
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: ba}

	got, err := BuildRecurrenceJSON(recurParams(domain.RecurrenceFreqDaily, "21:00"), ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	if !strings.Contains(got, `"timezone":"America/Argentina/Buenos_Aires"`) {
		t.Errorf("canonical JSON = %s, want user timezone", got)
	}
}

func TestBuildRecurrenceJSONTimeNormalized(t *testing.T) {
	// "20h" is accepted by parseHHMM and normalized to the canonical "20:00".
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	got, err := BuildRecurrenceJSON(recurParams(domain.RecurrenceFreqDaily, "20h"), ctx)
	if err != nil {
		t.Fatalf("BuildRecurrenceJSON() error = %v", err)
	}
	if !strings.Contains(got, `"time":"20:00"`) {
		t.Errorf("canonical JSON = %s, want normalized time 20:00", got)
	}
}

func TestBuildRecurrenceJSONErrors(t *testing.T) {
	ctx := InterpretContext{Now: time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC), Timezone: time.UTC}
	cases := []struct {
		name string
		p    RecurrenceParams
	}{
		{"bad time", recurParams(domain.RecurrenceFreqDaily, "25:00")},
		{"no time", recurParams(domain.RecurrenceFreqDaily, "")},
		{"unknown freq", recurParams("fortnightly", "09:00")},
		{"weekly without weekdays", recurParams(domain.RecurrenceFreqWeekly, "09:00")},
		{"weekly weekday out of range", func() RecurrenceParams {
			p := recurParams(domain.RecurrenceFreqWeekly, "09:00")
			p.Weekdays = []int{8}
			return p
		}()},
		{"monthly without day", recurParams(domain.RecurrenceFreqMonthly, "09:00")},
		{"monthly day zero", func() RecurrenceParams {
			p := recurParams(domain.RecurrenceFreqMonthly, "09:00")
			d := 0
			p.DayOfMonth = &d
			return p
		}()},
		{"yearly without month/day", recurParams(domain.RecurrenceFreqYearly, "09:00")},
		{"anchor month without day", func() RecurrenceParams {
			p := recurParams(domain.RecurrenceFreqYearly, "09:00")
			m := 9
			p.AnchorMonth = &m
			return p
		}()},
		{"anchor year without month/day", func() RecurrenceParams {
			p := recurParams(domain.RecurrenceFreqYearly, "09:00")
			y := 2027
			p.AnchorYear = &y
			return p
		}()},
		{"interval zero", func() RecurrenceParams {
			p := recurParams(domain.RecurrenceFreqDaily, "09:00")
			i := 0
			p.Interval = &i
			return p
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := BuildRecurrenceJSON(c.p, ctx); err == nil {
				t.Errorf("expected error for %q", c.name)
			}
		})
	}
}
