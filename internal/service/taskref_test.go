package service

import (
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

func pendingTask(id, title string) domain.Task {
	return domain.Task{ID: id, Title: title, Status: domain.TaskStatusPending, CreatedAt: time.Now(), UpdatedAt: time.Now()}
}

// --- Classification helpers -------------------------------------------------

func TestTaskRefMatchNotFound(t *testing.T) {
	m := MatchTaskRef([]domain.Task{pendingTask("t1", "comprar leche")}, "SSD")
	if !m.IsNotFound() {
		t.Error("IsNotFound = false, want true")
	}
	if m.IsAmbiguous() {
		t.Error("IsAmbiguous = true, want false")
	}
	if _, ok := m.ResolvedTask(); ok {
		t.Error("ResolvedTask ok = true, want false")
	}
	if len(m.Matches) != 0 {
		t.Errorf("Matches = %d, want 0", len(m.Matches))
	}
}

// --- Matching rules --------------------------------------------------------

func TestMatchTaskRefSingleExact(t *testing.T) {
	tt := pendingTask("t1", "comprar leche")
	m := MatchTaskRef([]domain.Task{tt}, "comprar leche")
	if m.IsNotFound() || m.IsAmbiguous() {
		t.Errorf("classification = notfound=%v ambiguous=%v, want single", m.IsNotFound(), m.IsAmbiguous())
	}
	got, ok := m.ResolvedTask()
	if !ok || got.ID != "t1" {
		t.Errorf("ResolvedTask = (%v, %v), want (t1, true)", got.ID, ok)
	}
}

func TestMatchTaskRefCaseInsensitive(t *testing.T) {
	tt := pendingTask("t1", "Comprar Leche")
	m := MatchTaskRef([]domain.Task{tt}, "comprar")
	if got, ok := m.ResolvedTask(); !ok || got.ID != "t1" {
		t.Errorf("case-insensitive match failed: (%v, %v)", got.ID, ok)
	}
}

func TestMatchTaskRefSubstring(t *testing.T) {
	tt := pendingTask("t1", "comprar SSD de 1TB")
	m := MatchTaskRef([]domain.Task{tt}, "SSD")
	if got, ok := m.ResolvedTask(); !ok || got.ID != "t1" {
		t.Errorf("substring match failed: (%v, %v)", got.ID, ok)
	}
}

func TestMatchTaskRefNoTrim(t *testing.T) {
	tt := pendingTask("t1", "comprar SSD")
	// Without trimming, a padded reference does not match a title without those spaces.
	m := MatchTaskRef([]domain.Task{tt}, "  SSD  ")
	if !m.IsNotFound() {
		t.Errorf("IsNotFound = false, want true (whitespace-padded ref must not match)")
	}
}

func TestMatchTaskRefPreservesRef(t *testing.T) {
	tt := pendingTask("t1", "comprar SSD")
	m := MatchTaskRef([]domain.Task{tt}, "SSD")
	if m.Ref != "SSD" {
		t.Errorf("Ref = %q, want the exact original reference %q", m.Ref, "SSD")
	}
}

func TestMatchTaskRefIgnoresCompletedAndCancelled(t *testing.T) {
	tasks := []domain.Task{
		{ID: "t-done", Title: "comprar SSD", Status: domain.TaskStatusCompleted, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "t-cancelled", Title: "comprar SSD", Status: domain.TaskStatusCancelled, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "t-inprogress", Title: "comprar SSD", Status: domain.TaskStatusInProgress, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	m := MatchTaskRef(tasks, "SSD")
	if !m.IsNotFound() {
		t.Errorf("IsNotFound = false, want true (only pending tasks match), Matches = %d", len(m.Matches))
	}
}

// --- Ambiguity -------------------------------------------------------------

func TestMatchTaskRefAmbiguousConservesMatches(t *testing.T) {
	t1 := pendingTask("t1", "comprar SSD")
	t2 := pendingTask("t2", "instalar SSD")
	m := MatchTaskRef([]domain.Task{t1, t2}, "SSD")

	if !m.IsAmbiguous() {
		t.Error("IsAmbiguous = false, want true")
	}
	if m.IsNotFound() {
		t.Error("IsNotFound = true, want false")
	}
	if _, ok := m.ResolvedTask(); ok {
		t.Error("ResolvedTask ok = true, want false for an ambiguous ref")
	}
	// All matches are conserved (full tasks, not just titles) for future
	// clarification rendering, in input order.
	if len(m.Matches) != 2 {
		t.Fatalf("Matches = %d, want 2", len(m.Matches))
	}
	if m.Matches[0].ID != "t1" || m.Matches[1].ID != "t2" {
		t.Errorf("Matches order = [%s %s], want [t1 t2]", m.Matches[0].ID, m.Matches[1].ID)
	}
}

func TestMatchTaskRefAmbiguousSkipsNonPending(t *testing.T) {
	t1 := pendingTask("t1", "revisar SSD")
	t2 := pendingTask("t2", "instalar SSD")
	done := domain.Task{ID: "t3", Title: "comprar SSD", Status: domain.TaskStatusCompleted, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	m := MatchTaskRef([]domain.Task{t1, t2, done}, "SSD")
	if len(m.Matches) != 2 {
		t.Errorf("Matches = %d, want 2 (completed excluded)", len(m.Matches))
	}
	if m.Matches[0].ID != "t1" || m.Matches[1].ID != "t2" {
		t.Errorf("Matches order = [%s %s], want [t1 t2] (input order)", m.Matches[0].ID, m.Matches[1].ID)
	}
}

// --- Edge cases ------------------------------------------------------------

func TestMatchTaskRefNilTasks(t *testing.T) {
	m := MatchTaskRef(nil, "SSD")
	if !m.IsNotFound() {
		t.Error("IsNotFound = false, want true for nil tasks")
	}
	if len(m.Matches) != 0 {
		t.Errorf("Matches = %d, want 0", len(m.Matches))
	}
}

func TestMatchTaskRefEmptyTaskList(t *testing.T) {
	m := MatchTaskRef([]domain.Task{}, "SSD")
	if !m.IsNotFound() {
		t.Error("IsNotFound = false, want true for empty task list")
	}
}
