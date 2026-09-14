package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/verdu/alter/internal/capability"
)

// TestPlannerPromptContract locks the planner instruction contract: a user
// request matching an available capability MUST be answered with a Plan JSON
// (never prose), args must follow the declared schema, and a conversational
// reply is only allowed when no capability applies. This is the contract the
// create_task E2E regression depended on: Pi's response was conversational and
// NaturalIntent executed the operation as fallback.
func TestPlannerPromptContract(t *testing.T) {
	ctx := capability.PlannerContext{Capabilities: []capability.CatalogEntry{
		{
			Name:        "create_task",
			Description: "Create a new task",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`),
		},
	}}

	prompt := plannerPrompt(ctx)

	// The catalog is embedded with the capability visible to the planner.
	for _, want := range []string{
		`"name":"create_task"`,
		`"description":"Create a new task"`,
		`"parameters":{"type":"object"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("planner prompt missing catalog fragment %q", want)
		}
	}

	// The capability must be treated as an executable action, not commentary.
	if !strings.Contains(prompt, "executable action") {
		t.Errorf("planner prompt does not frame capabilities as executable actions:\n%s", prompt)
	}

	// A matching request MUST produce a Plan JSON and never conversation.
	for _, want := range []string{
		"MUST respond",
		"Plan JSON object",
		"Do not describe the action in prose",
		"do not reply conversationally",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("planner prompt missing mandatory Plan requirement %q", want)
		}
	}

	// Args must follow the capability's declared schema.
	if !strings.Contains(prompt, "declared schema") || !strings.Contains(prompt, "required property") {
		t.Errorf("planner prompt does not bind args to the capability schema:\n%s", prompt)
	}

	// Conversation is only the allowed reply when NO capability applies.
	if !strings.Contains(prompt, "does not correspond to any available capability") ||
		!strings.Contains(prompt, "respond normally") {
		t.Errorf("planner prompt does not reserve conversation for unmatched requests:\n%s", prompt)
	}

	// Inventing capabilities stays forbidden.
	if !strings.Contains(prompt, "Never invent capabilities") {
		t.Errorf("planner prompt lost the no-invented-capabilities rule:\n%s", prompt)
	}

	// The minimal create_task example must show a Plan carrying only the required
	// args, so the planner can mirror the exact shape.
	if !strings.Contains(prompt, `"capability":"create_task"`) ||
		!strings.Contains(prompt, `"args":{"title":"buy milk"}`) {
		t.Errorf("planner prompt missing the create_task Plan example:\n%s", prompt)
	}
}

// TestPlannerPromptEmptyCatalog keeps the no-capability case deterministic: with
// an empty catalog the prompt still carries the rules, and the embedded context
// is the empty list.
func TestPlannerPromptEmptyCatalog(t *testing.T) {
	// Match the builder: an empty catalog yields an empty (non-nil) slice, so the
	// rendered context is the empty list, never null.
	prompt := plannerPrompt(capability.PlannerContext{Capabilities: []capability.CatalogEntry{}})

	if !strings.Contains(prompt, `{"capabilities":[]}`) {
		t.Errorf("empty catalog not rendered as empty list:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Rules:") {
		t.Errorf("rules block missing with empty catalog:\n%s", prompt)
	}
	if strings.Contains(prompt, `"name":`) {
		t.Errorf("empty catalog rendered phantom capabilities:\n%s", prompt)
	}
}
