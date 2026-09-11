package capability

import (
	"errors"
	"testing"
)

func TestClassifyResponseNormalText(t *testing.T) {
	text := "  Here are your reminders for today.  "
	cls, err := ClassifyResponse(text)
	if err != nil {
		t.Fatalf("ClassifyResponse: %v", err)
	}
	if cls.Kind != ResponseConversation {
		t.Errorf("Kind = %v, want conversation", cls.Kind)
	}
	// The text is returned unmodified (outer whitespace preserved).
	if cls.Text != text {
		t.Errorf("Text = %q, want the original %q", cls.Text, text)
	}
	if cls.Plan != nil {
		t.Error("Plan must be nil for a conversation")
	}
}

func TestClassifyResponseBraceInMiddle(t *testing.T) {
	text := "tienes 3 tareas {comprar leche} pendientes y un plan para mañana"
	cls, err := ClassifyResponse(text)
	if err != nil {
		t.Fatalf("ClassifyResponse: %v", err)
	}
	if cls.Kind != ResponseConversation {
		t.Errorf("Kind = %v, want conversation", cls.Kind)
	}
	if cls.Text != text {
		t.Errorf("Text = %q, want original", cls.Text)
	}
}

func TestClassifyResponseValidPlan(t *testing.T) {
	input := `{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}`
	cls, err := ClassifyResponse(input)
	if err != nil {
		t.Fatalf("ClassifyResponse: %v", err)
	}
	if cls.Kind != ResponsePlan {
		t.Fatalf("Kind = %v, want plan", cls.Kind)
	}
	if cls.Plan == nil {
		t.Fatal("Plan is nil, want parsed plan")
	}
	if len(cls.Plan.Calls) != 1 || cls.Plan.Calls[0].Capability != "list_tasks" {
		t.Errorf("unexpected plan: %+v", cls.Plan.Calls)
	}
}

func TestClassifyResponseEmptyPlan(t *testing.T) {
	input := `{"calls":[],"clarification":null}`
	cls, err := ClassifyResponse(input)
	if err != nil {
		t.Fatalf("ClassifyResponse: %v", err)
	}
	if cls.Kind != ResponsePlan {
		t.Fatalf("Kind = %v, want plan", cls.Kind)
	}
	if cls.Plan == nil || len(cls.Plan.Calls) != 0 {
		t.Errorf("unexpected plan: %+v", cls.Plan)
	}
}

func TestClassifyResponseInvalidPlan(t *testing.T) {
	// Starts with '{' but is not a valid Plan: must be InvalidPlan, never
	// downgraded to a conversation.
	for _, input := range []string{
		`{"calls":`,
		`{"calls":[{"capability":123,"args":{}}]}`,
		`{"unexpected":true}`,
		`{this is plain text starting with a brace{}`,
	} {
		cls, err := ClassifyResponse(input)
		if err == nil {
			t.Errorf("ClassifyResponse(%q): expected error, got nil", input)
			continue
		}
		if !errors.Is(err, ErrInvalidPlan) {
			t.Errorf("ClassifyResponse(%q): errors.Is(err, ErrInvalidPlan) = false, err = %v", input, err)
		}
		if cls.Kind != ResponseInvalidPlan {
			t.Errorf("ClassifyResponse(%q): Kind = %v, want invalid_plan", input, cls.Kind)
		}
	}
}

func TestClassifyResponseUnknownCapabilityIsStillPlan(t *testing.T) {
	// Existence of capabilities belongs to the Dispatcher; this layer only
	// checks the plan contract.
	input := `{"calls":[{"capability":"does_not_exist","args":{}}],"clarification":null}`
	cls, err := ClassifyResponse(input)
	if err != nil {
		t.Fatalf("ClassifyResponse: %v", err)
	}
	if cls.Kind != ResponsePlan {
		t.Errorf("Kind = %v, want plan (dispatcher owns existence check)", cls.Kind)
	}
	if cls.Plan == nil || cls.Plan.Calls[0].Capability != "does_not_exist" {
		t.Errorf("unexpected plan: %+v", cls.Plan)
	}
}

func TestClassifyResponseOuterWhitespace(t *testing.T) {
	planInput := "  \n\t" + `{"calls":[],"clarification":null}` + "\n  "
	cls, err := ClassifyResponse(planInput)
	if err != nil {
		t.Fatalf("ClassifyResponse (plan): %v", err)
	}
	if cls.Kind != ResponsePlan {
		t.Errorf("plan with outer whitespace: Kind = %v, want plan", cls.Kind)
	}

	textInput := "  \n\thello\n  "
	cls2, err := ClassifyResponse(textInput)
	if err != nil {
		t.Fatalf("ClassifyResponse (text): %v", err)
	}
	if cls2.Kind != ResponseConversation {
		t.Errorf("text with outer whitespace: Kind = %v, want conversation", cls2.Kind)
	}
	if cls2.Text != textInput {
		t.Errorf("Text = %q, want original (whitespace preserved)", cls2.Text)
	}
}

func TestClassifyResponseNeverExecutes(t *testing.T) {
	// ClassifyResponse has no dispatcher access and performs no side effects:
	// classifying a plan must not execute anything. This test documents that
	// contract — a plan with a real-sounding capability is only parsed.
	cls, err := ClassifyResponse(`{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}`)
	if err != nil {
		t.Fatalf("ClassifyResponse: %v", err)
	}
	if cls.Kind != ResponsePlan || cls.Plan == nil {
		t.Fatalf("unexpected classification: %+v", cls)
	}
}
