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

// reminderPrompt is the functional system prompt prepended to the Orchestrator
// instruction when a reminder trigger fires. It replaces Pi's default coding
// assistant role with a personal assistant role for the duration of one RPC
// execution. The prompt is deliberately short and explicit so that the model
// produces a user-facing Telegram message, not code or tool calls.
//
// The format follows the same pattern used by naturalintent: the prompt is
// prepended to the instruction and separated by "\n\nUser message: ". Pi
// receives the combined text as a single user-level message; no
// --append-system-prompt flag is involved.
const reminderPrompt = `You are ALTER, a personal virtual secretary. You are generating a short notification message for the user about a task reminder.

Rules:
- Reply ONLY with the notification text. No code, no tool calls, no file inspection.
- Be brief and warm. One to two sentences max.
- Write in the same language as the task title.
- Plain text only: no HTML, no markdown, no formatting, no emoji decorations, no bullet lists.
- Do not execute actions, access databases, or read the filesystem.
- Do not attempt to understand the project or its codebase.`
