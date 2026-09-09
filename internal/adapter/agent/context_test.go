package agent

import (
	"strings"
	"testing"

	"github.com/verdu/alter/internal/domain"
)

func TestSearchQuery(t *testing.T) {
	task := domain.Task{Title: "  buy milk  ", Description: "  urgent  "}
	if got := searchQuery(task); got != "buy milk urgent" {
		t.Errorf("searchQuery = %q, want title+description", got)
	}
	if got := searchQuery(domain.Task{Title: "alone"}); got != "alone" {
		t.Errorf("searchQuery without description = %q, want %q", got, "alone")
	}
}

func TestAssembleInstructionNoResultsIsDefault(t *testing.T) {
	task := domain.Task{Title: "buy milk"}
	if got := assembleInstruction(task, nil); got != defaultInstruction(task) {
		t.Errorf("assembleInstruction(nil) = %q, want %q", got, defaultInstruction(task))
	}
	if got := assembleInstruction(task, []domain.SearchResult{}); got != defaultInstruction(task) {
		t.Errorf("assembleInstruction([]) = %q, want %q", got, defaultInstruction(task))
	}
}

func TestAssembleInstructionLabelsByKind(t *testing.T) {
	task := domain.Task{Title: "buy milk"}
	results := []domain.SearchResult{
		{Ref: domain.Ref{Kind: domain.RefKindTask, ID: "t1"}, Text: "related task text", Score: 0.9},
		{Ref: domain.Ref{Kind: domain.RefKindEvent, ID: "e1"}, Text: "previous response text", Score: 0.8},
	}
	got := assembleInstruction(task, results)

	if !strings.HasPrefix(got, defaultInstruction(task)) {
		t.Errorf("instruction must start with the default: %q", got)
	}
	if !strings.Contains(got, "Related context:") {
		t.Errorf("instruction lacks the context header: %q", got)
	}
	if !strings.Contains(got, "Task: related task text") {
		t.Errorf("instruction lacks the task label: %q", got)
	}
	if !strings.Contains(got, "Previous agent response: previous response text") {
		t.Errorf("instruction lacks the event label: %q", got)
	}
}

func TestAssembleInstructionTruncatesOversizedFragment(t *testing.T) {
	task := domain.Task{Title: "t"}
	results := []domain.SearchResult{
		{Ref: domain.Ref{Kind: domain.RefKindEvent, ID: "e1"}, Text: strings.Repeat("x", 1000), Score: 1},
	}
	got := assembleInstruction(task, results)
	// The default instruction plus one bounded fragment: must never exceed a
	// clearly bounded budget.
	if len(got) > len(defaultInstruction(task))+defaultContextChars+64 {
		t.Errorf("instruction too large: %d bytes", len(got))
	}
	if !strings.Contains(got, "…") {
		t.Errorf("oversized fragment must be truncated with a marker")
	}
}

func TestTruncateText(t *testing.T) {
	if got := truncateText("short", 200); got != "short" {
		t.Errorf("short text must pass through, got %q", got)
	}
	if got := truncateText("  a\n\tb  ", 200); got != "a b" {
		t.Errorf("whitespace must collapse, got %q", got)
	}
	// Multi-byte safe: cutting on runes, never splitting a UTF-8 character.
	long := "héllo wörld " + strings.Repeat("ñ", 100)
	got := truncateText(long, 20)
	if strings.IndexRune(got, 0xFFFD) >= 0 {
		t.Errorf("truncation produced a replacement rune: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated text must end with the marker: %q", got)
	}
	if got := truncateText("anything", 0); got != "" {
		t.Errorf("max 0 must yield empty, got %q", got)
	}
}
