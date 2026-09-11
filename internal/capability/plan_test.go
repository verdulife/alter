package capability

import (
	"encoding/json"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Wire shape (marshaling) — the contract as emitted by ALTER.
// ---------------------------------------------------------------------------

func TestPlanActionableJSONShape(t *testing.T) {
	plan := Plan{
		Calls: []Call{
			{Capability: "list_tasks", Args: json.RawMessage(`{}`)},
		},
	}
	got, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}`
	if string(got) != want {
		t.Errorf("JSON = %s, want %s", got, want)
	}
}

func TestPlanConversationalJSONShape(t *testing.T) {
	plan := Plan{Calls: []Call{}}
	got, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"calls":[],"clarification":null}`
	if string(got) != want {
		t.Errorf("JSON = %s, want %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// ParsePlan — the parser Pi → Plan.
// ---------------------------------------------------------------------------

func TestParsePlanWithCall(t *testing.T) {
	plan, err := ParsePlan([]byte(`{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}`))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(plan.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want 1", len(plan.Calls))
	}
	if plan.Calls[0].Capability != "list_tasks" {
		t.Errorf("Capability = %q, want %q", plan.Calls[0].Capability, "list_tasks")
	}
	if string(plan.Calls[0].Args) != "{}" {
		t.Errorf("Args = %q, want {}", plan.Calls[0].Args)
	}
	if plan.Clarification != nil {
		t.Errorf("Clarification = %v, want nil", *plan.Clarification)
	}
}

func TestParsePlanNoCalls(t *testing.T) {
	plan, err := ParsePlan([]byte(`{"calls":[],"clarification":null}`))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Calls == nil {
		t.Fatal("Calls is nil, want non-nil empty slice")
	}
	if len(plan.Calls) != 0 {
		t.Errorf("len(Calls) = %d, want 0", len(plan.Calls))
	}
}

func TestParsePlanNormalizesAbsentCalls(t *testing.T) {
	for _, input := range []string{
		`{"clarification":null}`,
		`{"calls":null,"clarification":null}`,
	} {
		plan, err := ParsePlan([]byte(input))
		if err != nil {
			t.Fatalf("ParsePlan(%s): %v", input, err)
		}
		if plan.Calls == nil {
			t.Errorf("ParsePlan(%s): Calls is nil, want non-nil empty slice", input)
		}
	}
}

func TestParsePlanInvalidJSON(t *testing.T) {
	_, err := ParsePlan([]byte(`{"calls":[`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !errors.Is(err, ErrInvalidPlan) {
		t.Errorf("errors.Is(err, ErrInvalidPlan) = false, err = %v", err)
	}
}

func TestParsePlanInvalidRoot(t *testing.T) {
	for _, input := range []string{
		``,
		`   `,
		`[1,2]`,
		`"list_tasks"`,
		`123`,
		`true`,
		`null`,
	} {
		_, err := ParsePlan([]byte(input))
		if err == nil {
			t.Errorf("ParsePlan(%q): expected error, got nil", input)
			continue
		}
		if !errors.Is(err, ErrInvalidPlan) {
			t.Errorf("ParsePlan(%q): errors.Is(err, ErrInvalidPlan) = false, err = %v", input, err)
		}
	}
}

func TestParsePlanUnknownFieldsRejected(t *testing.T) {
	for name, input := range map[string]string{
		"unknown field in Plan": `{"calls":[],"clarification":null,"extra":123}`,
		"unknown field in Call": `{"calls":[{"capability":"list_tasks","args":{},"extra":123}],"clarification":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePlan([]byte(input))
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidPlan) {
				t.Errorf("errors.Is(err, ErrInvalidPlan) = false, err = %v", err)
			}
		})
	}
}

func TestParsePlanValidContractStillAccepted(t *testing.T) {
	input := `{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}`
	plan, err := ParsePlan([]byte(input))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(plan.Calls) != 1 || plan.Calls[0].Capability != "list_tasks" {
		t.Errorf("unexpected plan: %+v", plan)
	}
}
