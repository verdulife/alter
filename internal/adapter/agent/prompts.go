package agent

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
- Do not execute actions, access databases, or read the filesystem.
- Do not attempt to understand the project or its codebase.`
