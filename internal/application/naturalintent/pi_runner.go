package naturalintent

import (
	"context"
	"fmt"

	"github.com/verdu/alter/internal/domain"
)

// PiRunnerAdapter wraps an existing domain.Agent (concretely, agent.PiAgent)
// to satisfy the piRunner interface. It translates the simple prompt→text
// contract into the AgentRequest/AgentResult protocol that PiAgent expects.
//
// This adapter lives in the naturalintent package (application layer) because
// it bridges the interpreter to the existing Pi infrastructure without coupling
// the interpreter to the concrete agent package.
type PiRunnerAdapter struct {
	agent domain.Agent
}

// NewPiRunnerAdapter wraps a domain.Agent as a piRunner for intent interpretation.
func NewPiRunnerAdapter(agent domain.Agent) *PiRunnerAdapter {
	return &PiRunnerAdapter{agent: agent}
}

// RunPi sends a prompt to the Pi agent and returns the response text.
// The AgentRequest uses a minimal Task/Trigger as carriers for the instruction;
// PiAgent only reads the Instruction field when constructing its RPC prompt.
func (a *PiRunnerAdapter) RunPi(ctx context.Context, prompt string) (string, error) {
	result, err := a.agent.Execute(ctx, domain.AgentRequest{
		// Task and Trigger are intentionally empty stubs: PiAgent only uses
		// the Instruction field for the RPC prompt. The zero values are harmless.
		Task:        domain.Task{},
		Trigger:     domain.Trigger{},
		Instruction: prompt,
	})
	if err != nil {
		return "", fmt.Errorf("pi runner: %w", err)
	}
	return result.Response, nil
}
