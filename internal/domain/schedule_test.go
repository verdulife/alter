package domain

import (
	"errors"
	"testing"
	"time"
)

// fixed due date used by tests (deterministic, UTC).
var fixedDue = time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)

func TestCalculateNextFireAtAt(t *testing.T) {
	tr := Trigger{Type: TriggerTypeAt, Value: "2024-05-01T08:00:00Z"}
	got, err := CalculateNextFireAt(tr, Task{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2024, 5, 1, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("want %v got %v", want, got)
	}
}

func TestCalculateNextFireAtBeforeDue24h(t *testing.T) {
	tr := Trigger{Type: TriggerTypeBeforeDue, Value: "24h"}
	got, err := CalculateNextFireAt(tr, Task{DueAt: &fixedDue})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2024, 4, 30, 10, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("want %v got %v", want, got)
	}
}

func TestCalculateNextFireAtBeforeDue1h(t *testing.T) {
	tr := Trigger{Type: TriggerTypeBeforeDue, Value: "1h"}
	got, err := CalculateNextFireAt(tr, Task{DueAt: &fixedDue})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("want %v got %v", want, got)
	}
}

func TestCalculateNextFireAtAfterDue(t *testing.T) {
	tr := Trigger{Type: TriggerTypeAfterDue, Value: "30m"}
	got, err := CalculateNextFireAt(tr, Task{DueAt: &fixedDue})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2024, 5, 1, 10, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("want %v got %v", want, got)
	}
}

func TestCalculateNextFireAtTaskWithoutDueAt(t *testing.T) {
	tr := Trigger{Type: TriggerTypeBeforeDue, Value: "1h"}
	if _, err := CalculateNextFireAt(tr, Task{}); !errors.Is(err, ErrTaskDueAtMissing) {
		t.Errorf("expected ErrTaskDueAtMissing, got %v", err)
	}
}

func TestCalculateNextFireAtInvalidTimestamp(t *testing.T) {
	tr := Trigger{Type: TriggerTypeAt, Value: "not-a-time"}
	if _, err := CalculateNextFireAt(tr, Task{}); !errors.Is(err, ErrInvalidTimestamp) {
		t.Errorf("expected ErrInvalidTimestamp, got %v", err)
	}
}

func TestCalculateNextFireAtInvalidDuration(t *testing.T) {
	tr := Trigger{Type: TriggerTypeBeforeDue, Value: "5"}
	if _, err := CalculateNextFireAt(tr, Task{DueAt: &fixedDue}); !errors.Is(err, ErrInvalidDuration) {
		t.Errorf("expected ErrInvalidDuration, got %v", err)
	}
}

func TestCalculateNextFireAtNegativeDuration(t *testing.T) {
	tr := Trigger{Type: TriggerTypeBeforeDue, Value: "-1h"}
	if _, err := CalculateNextFireAt(tr, Task{DueAt: &fixedDue}); !errors.Is(err, ErrNegativeDuration) {
		t.Errorf("expected ErrNegativeDuration, got %v", err)
	}
}

func TestCalculateNextFireAtCustomUnsupported(t *testing.T) {
	tr := Trigger{Type: TriggerTypeCustom, Value: "anything"}
	if _, err := CalculateNextFireAt(tr, Task{DueAt: &fixedDue}); !errors.Is(err, ErrTriggerCustomUnsupported) {
		t.Errorf("expected ErrTriggerCustomUnsupported, got %v", err)
	}
}

func TestCalculateNextFireAtUnknownType(t *testing.T) {
	tr := Trigger{Type: TriggerType("weird"), Value: "1h"}
	if _, err := CalculateNextFireAt(tr, Task{DueAt: &fixedDue}); !errors.Is(err, ErrUnknownTriggerType) {
		t.Errorf("expected ErrUnknownTriggerType, got %v", err)
	}
}
