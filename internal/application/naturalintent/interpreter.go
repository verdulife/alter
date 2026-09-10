package naturalintent

import "context"

// IntentInterpreter defines the contract for natural language interpretation.
// Implementations parse free-text user messages and produce structured intents.
type IntentInterpreter interface {
	// Interpret takes a raw user message and returns a structured intent.
	// It never executes operations or mutates state.
	Interpret(ctx context.Context, text string, ictx InterpretContext) (IntentResult, error)
}
