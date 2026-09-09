package agent

import (
	"strings"

	"github.com/verdu/alter/internal/domain"
)

// defaultContextChars bounds each context fragment appended to the Agent
// instruction, so a large indexed text cannot blow up the prompt budget. The
// fragment is the indexed text of a hit; V1 keeps it small and flat.
const defaultContextChars = 200

// searchQuery derives the retrieval query for a fired task from its title and
// description. Source and scheduling bookkeeping are deliberately excluded:
// they carry little semantic value for relatedness, and the fired task itself
// is excluded from the results anyway (see assembleInstruction's caller).
func searchQuery(task domain.Task) string {
	// Title + description, whitespace-collapsed so the query is a clean phrase
	// for the embedding provider. Source and scheduling bookkeeping are
	// deliberately excluded: they carry little semantic value for relatedness,
	// and the fired task itself is excluded from the results anyway.
	return strings.Join(strings.Fields(task.Title+" "+task.Description), " ")
}

// assembleInstruction builds the Agent instruction for a fired task: the V1
// neutral default instruction plus, when semantic search returned related
// context, a bounded digest of the best results. It is a pure function: no
// storage access, no side effects, no dependency on the Orchestrator's ports.
//
// Each fragment is labeled by its ref kind ("Task" vs "Previous agent
// response") so the agent can weigh fresh memory (past responses) against the
// task inventory. Empty or absent results produce exactly the default
// instruction.
func assembleInstruction(task domain.Task, results []domain.SearchResult) string {
	var b strings.Builder
	b.WriteString(defaultInstruction(task))
	if len(results) == 0 {
		return b.String()
	}

	b.WriteString("\n\nRelated context:")
	for _, r := range results {
		b.WriteString("\n- ")
		switch r.Ref.Kind {
		case domain.RefKindTask:
			b.WriteString("Task")
		case domain.RefKindEvent:
			b.WriteString("Previous agent response")
		default:
			// Unknown kind: label neutrally instead of failing; the fragment
			// still carries the indexed text.
			b.WriteString("Context")
		}
		b.WriteString(": ")
		b.WriteString(truncateText(r.Text, defaultContextChars))
	}
	return b.String()
}

// truncateText collapses whitespace and bounds a fragment to max runes so a
// prompt cannot be flooded by one oversized hit.
func truncateText(s string, max int) string {
	if max <= 0 {
		return ""
	}
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
