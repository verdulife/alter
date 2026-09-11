package capability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Plan is the minimal contract the planner (Pi) proposes to ALTER: an ordered
// batch of capability calls plus an optional clarification message.
//
// The two supported wire shapes are:
//
//	{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null} // actionable
//	{"calls":[],"clarification":null}                                      // conversational, no action
//
// A Plan without calls represents a conversational answer with no capability
// execution. Calls must be a non-nil slice: an empty (non-nil) slice
// serializes as [] and is the no-action shape; nil would serialize as null.
type Plan struct {
	Calls []Call `json:"calls"`
	// Clarification is an optional human-readable message accompanying the
	// plan. It serializes as explicit null when nil (V1 keeps the null in the
	// wire shape; no omitempty).
	Clarification *string `json:"clarification"`
}

// Call is a single capability invocation inside a Plan.
type Call struct {
	// Capability is the registered capability name to execute.
	Capability string `json:"capability"`
	// Args is the raw JSON arguments object for the capability. V1 defines it
	// as an object ("args":{} for no-argument capabilities such as list_tasks);
	// nil raw bytes would serialize as null and must be normalized by the
	// future parser.
	Args json.RawMessage `json:"args"`
}

// ParsePlan parses a JSON plan proposed by Pi into a Plan, or returns an error
// wrapped as ErrInvalidPlan.
//
// Accepted wire shapes:
//
//	{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}
//	{"calls":[],"clarification":null}
//
// The root value must be a JSON object; arrays, scalars and null are rejected.
// Field types are enforced by json.Unmarshal (calls must be an array, each
// capability a string, clarification a string or null). Unknown fields are
// rejected fail-closed at both levels (Plan and Call). No rules about concrete
// capabilities or per-capability argument shapes are applied here: the
// dispatcher owns that validation.
//
// After unmarshaling, Calls is normalized to a non-nil slice: an absent
// "calls" field or an explicit null is treated as the no-action shape ([]).
func ParsePlan(data []byte) (Plan, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Plan{}, fmt.Errorf("%w: root must be a JSON object", ErrInvalidPlan)
	}

	var plan Plan
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("%w: %v", ErrInvalidPlan, err)
	}
	// Reject trailing content after the plan object (json.Unmarshal rejected it too).
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Plan{}, fmt.Errorf("%w: trailing data after plan object", ErrInvalidPlan)
	}
	if plan.Calls == nil {
		plan.Calls = []Call{}
	}
	return plan, nil
}
