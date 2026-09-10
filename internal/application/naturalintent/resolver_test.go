package naturalintent

import (
	"testing"
	"time"
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
		{"25:00", 0, 0, true},  // invalid hour
		{"20:60", 0, 0, true},  // invalid minute
		{"abc", 0, 0, true},    // not a time
		{"20", 0, 0, true},     // missing minutes
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
