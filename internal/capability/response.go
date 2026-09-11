package capability

import (
	"fmt"
	"strings"
)

// ResponseKind classifies the textual response returned by Pi.
type ResponseKind int

const (
	// ResponseConversation is a normal textual answer: respond to the user.
	ResponseConversation ResponseKind = iota
	// ResponsePlan is a valid Plan JSON: hand it to the PlanExecutor.
	ResponsePlan
	// ResponseInvalidPlan is text that looks like JSON/plan but is not a
	// valid Plan: surface a controlled error.
	ResponseInvalidPlan
)

func (k ResponseKind) String() string {
	switch k {
	case ResponseConversation:
		return "conversation"
	case ResponsePlan:
		return "plan"
	case ResponseInvalidPlan:
		return "invalid_plan"
	default:
		return fmt.Sprintf("ResponseKind(%d)", int(k))
	}
}

// ResponseClassification is the result of classifying a Pi response.
type ResponseClassification struct {
	Kind ResponseKind
	// Text is the original response, unmodified. Meaningful for
	// ResponseConversation (the exact text to answer with).
	Text string
	// Plan is the parsed plan. Non-nil only for ResponsePlan.
	Plan *Plan
}

// ClassifyResponse classifies a Pi response without intent heuristics and
// without executing anything.
//
// V1 rules:
//   - After stripping only outer whitespace, if the text does not start with
//     '{', it is a conversation (a normal text containing braces or the word
//     "plan" in the middle stays a conversation).
//   - If it starts with '{', ParsePlan is attempted on the whole trimmed
//     content. Success → ResponsePlan with the parsed plan. Failure →
//     ResponseInvalidPlan with the ParsePlan error (distinguishable via
//     errors.Is with ErrInvalidPlan); it is never downgraded to a
//     conversation.
//
// Neither embedded JSON extraction nor semantic intent inference is performed.
// Plan validation is delegated entirely to ParsePlan — nothing here re-checks
// capability existence or argument schemas (that is the Dispatcher's job).
func ClassifyResponse(text string) (ResponseClassification, error) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return ResponseClassification{Kind: ResponseConversation, Text: text}, nil
	}

	plan, err := ParsePlan([]byte(trimmed))
	if err != nil {
		return ResponseClassification{Kind: ResponseInvalidPlan}, err
	}
	return ResponseClassification{Kind: ResponsePlan, Plan: &plan}, nil
}
