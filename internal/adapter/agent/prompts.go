package agent

import (
	"encoding/json"

	"github.com/verdu/alter/internal/capability"
)

// plannerPrompt is the capability-context block appended to planner-facing
// requests. It tells Pi that ALTER exposes explicit capabilities, that a
// request needing an ALTER action must answer with a valid Plan JSON, that a
// request needing no action may answer normally, and that capabilities outside
// the catalog are never valid. The embedded context is derived fresh from the
// Catalog on every request, so it is never a cached manual list.
func plannerPrompt(ctx capability.PlannerContext) string {
	data, err := json.Marshal(ctx)
	if err != nil {
		data = []byte(`{"capabilities":[]}`)
	}
	return `ALTER has explicit capabilities it can execute on your behalf.

Available capabilities (JSON):
` + string(data) + `

Rules:
- If the request requires an action in ALTER, respond with a single valid Plan JSON object:
  {"calls":[{"capability":"<name>","args":{...}}],"clarification":null|"<message>"}
- If the request requires no ALTER action, respond normally with plain text (no JSON).
- Never invent capabilities or actions outside the list above.
- Never emit code, tool calls, markdown or other formats.`
}
