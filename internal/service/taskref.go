package service

import (
	"strings"

	"github.com/verdu/alter/internal/domain"
)

// TaskRefMatch is the result of resolving a textual task reference against a
// set of tasks. It classifies the resolution without deciding on errors, so
// every caller keeps its own error semantics (user-facing messages in the
// command service, sentinel errors in the capability layer).
type TaskRefMatch struct {
	// Ref is the original reference the match was computed for.
	Ref string
	// Matches are all pending tasks whose title contains the reference
	// (case-insensitive substring), in input order. Conserved in full so an
	// ambiguous result can be turned into a clarification downstream.
	Matches []domain.Task
}

// MatchTaskRef finds every pending task whose title contains ref as a
// case-insensitive substring. It is the single resolution rule shared by all
// task_ref consumers:
//
//   - only pending tasks are considered (completed/cancelled never match);
//   - the match is a plain substring of the title, case-insensitive;
//   - the reference is used as-is (whitespace handling is the caller's
//     responsibility); the original ref is preserved in TaskRefMatch.Ref;
//   - the caller classifies the result: 0 matches → not found, 1 → resolved,
//     2+ → ambiguous.
//
// Callers are expected to guarantee a non-empty ref (the NL interpreter and
// the capability schema both already require it).
func MatchTaskRef(tasks []domain.Task, ref string) TaskRefMatch {
	refLower := strings.ToLower(ref)
	var matches []domain.Task
	for _, t := range tasks {
		if t.Status == domain.TaskStatusPending && strings.Contains(strings.ToLower(t.Title), refLower) {
			matches = append(matches, t)
		}
	}
	return TaskRefMatch{Ref: ref, Matches: matches}
}

// IsNotFound reports whether no task matched the reference.
func (m TaskRefMatch) IsNotFound() bool {
	return len(m.Matches) == 0
}

// IsAmbiguous reports whether more than one task matched the reference.
func (m TaskRefMatch) IsAmbiguous() bool {
	return len(m.Matches) > 1
}

// ResolvedTask returns the single matching task when the reference resolves
// uniquely. The bool is false otherwise (0 or 2+ matches).
func (m TaskRefMatch) ResolvedTask() (domain.Task, bool) {
	if len(m.Matches) == 1 {
		return m.Matches[0], true
	}
	return domain.Task{}, false
}
