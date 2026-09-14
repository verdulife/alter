package agent

import (
	"encoding/json"

	"github.com/verdu/alter/internal/capability"
)

// plannerPrompt is the capability-context block appended to planner-facing
// requests. It tells Pi that ALTER exposes explicit capabilities, that those
// capabilities are executable actions (not topics for commentary), that a user
// request matching an available capability MUST be answered with a single valid
// Plan JSON (never prose describing the action), that args must follow the
// capability's declared schema, and that a plain conversational reply is only
// allowed when no capability applies. The embedded context is derived fresh from
// the Catalog on every request, so it is never a cached manual list.
func plannerPrompt(ctx capability.PlannerContext) string {
	data, err := json.Marshal(ctx)
	if err != nil {
		data = []byte(`{"capabilities":[]}`)
	}
	return `ALTER exposes capabilities that it can execute on your behalf. Each capability is an executable action, not a topic for commentary.

Available capabilities (JSON):
` + string(data) + `

Rules:
- If the user's request requires one of the available capabilities, you MUST respond with a single valid Plan JSON object and nothing else. Do not describe the action in prose, do not explain what you could do, and do not reply conversationally:
  {"calls":[{"capability":"<name>","args":{...}}],"clarification":null|"<message>"}
- Build args exactly according to that capability's declared schema: include every required property and use only the allowed types and values.
- Only when the request does not correspond to any available capability may you respond normally with plain text (no JSON).
- Never invent capabilities or actions outside the list above.
- Never emit code, tool calls, markdown or other formats.

Example — the user asks to create a task and create_task is available:
Plan: {"calls":[{"capability":"create_task","args":{"title":"buy milk"}}],"clarification":null}`
}
