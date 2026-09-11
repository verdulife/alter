package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/verdu/alter/internal/capability"
	"github.com/verdu/alter/internal/domain"
)

// fakeAgent is a local stub for domain.Agent: it returns a canned response or
// error and counts invocations.
type fakeAgent struct {
	response string
	err      error
	calls    *int
}

func (a *fakeAgent) Execute(context.Context, domain.AgentRequest) (domain.AgentResult, error) {
	if a.calls != nil {
		*a.calls++
	}
	if a.err != nil {
		return domain.AgentResult{}, a.err
	}
	return domain.AgentResult{Response: a.response}, nil
}

// flowRecordingHandler counts invocations and returns a fixed result.
type flowRecordingHandler struct {
	calls *int
}

func (h flowRecordingHandler) Execute(context.Context, json.RawMessage) (capability.CapabilityResult, error) {
	*h.calls++
	return capability.CapabilityResult{Data: "done"}, nil
}

var errAgentFlowTest = errors.New("agent flow boom")

func newAgentFlowHarness(t *testing.T, agent domain.Agent) (*AgentFlow, *int) {
	t.Helper()
	reg := capability.NewRegistry()
	var calls int
	reg.Register(
		capability.Capability{Name: "list_tasks", Parameters: []byte(`{"type":"object","properties":{}}`)},
		flowRecordingHandler{calls: &calls},
	)
	flow := NewCapabilityFlow(capability.NewPlanExecutor(capability.NewDispatcher(reg)))
	return NewAgentFlow(agent, flow), &calls
}

var testRequest = domain.AgentRequest{Instruction: "test"}

func TestAgentFlowConversation(t *testing.T) {
	agent := &fakeAgent{response: "  tienes 3 tareas pendientes  "}
	af, calls := newAgentFlowHarness(t, agent)

	result, err := af.Execute(context.Background(), testRequest)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Kind != capability.ResponseConversation {
		t.Errorf("Kind = %v, want conversation", result.Kind)
	}
	if result.Response != agent.response {
		t.Errorf("Response = %q, want %q", result.Response, agent.response)
	}
	if result.Results != nil {
		t.Errorf("Results = %v, want nil", result.Results)
	}
	if *calls != 0 {
		t.Errorf("handler calls = %d, want 0", *calls)
	}
}

func TestAgentFlowValidPlan(t *testing.T) {
	agent := &fakeAgent{response: `{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}`}
	af, calls := newAgentFlowHarness(t, agent)

	result, err := af.Execute(context.Background(), testRequest)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Kind != capability.ResponsePlan {
		t.Errorf("Kind = %v, want plan", result.Kind)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if result.Results[0].Data != "done" {
		t.Errorf("Results[0].Data = %v, want %q", result.Results[0].Data, "done")
	}
	if *calls != 1 {
		t.Errorf("handler calls = %d, want 1", *calls)
	}
}

func TestAgentFlowEmptyPlan(t *testing.T) {
	agent := &fakeAgent{response: `{"calls":[],"clarification":null}`}
	af, calls := newAgentFlowHarness(t, agent)

	result, err := af.Execute(context.Background(), testRequest)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Kind != capability.ResponsePlan {
		t.Errorf("Kind = %v, want plan", result.Kind)
	}
	if result.Results == nil {
		t.Fatal("Results is nil, want non-nil empty slice")
	}
	if len(result.Results) != 0 {
		t.Errorf("len(Results) = %d, want 0", len(result.Results))
	}
	if *calls != 0 {
		t.Errorf("handler calls = %d, want 0", *calls)
	}
}

func TestAgentFlowInvalidPlan(t *testing.T) {
	agent := &fakeAgent{response: `{"calls":[`}
	af, calls := newAgentFlowHarness(t, agent)

	_, err := af.Execute(context.Background(), testRequest)
	if err == nil {
		t.Fatal("expected ErrInvalidPlan")
	}
	if !errors.Is(err, capability.ErrInvalidPlan) {
		t.Errorf("errors.Is(err, ErrInvalidPlan) = false, err = %v", err)
	}
	if *calls != 0 {
		t.Errorf("handler calls = %d, want 0", *calls)
	}
}

func TestAgentFlowAgentErrorPropagatesWithoutFlow(t *testing.T) {
	agent := &fakeAgent{err: errAgentFlowTest}
	af, calls := newAgentFlowHarness(t, agent)

	_, err := af.Execute(context.Background(), testRequest)
	if err == nil {
		t.Fatal("expected agent error")
	}
	if !errors.Is(err, errAgentFlowTest) {
		t.Errorf("errors.Is(err, agent error) = false, err = %v", err)
	}
	if *calls != 0 {
		t.Errorf("handler calls = %d, want 0 (CapabilityFlow must not run)", *calls)
	}
}

func TestAgentFlowCapabilityErrorPropagates(t *testing.T) {
	agent := &fakeAgent{response: `{"calls":[{"capability":"missing","args":{}}],"clarification":null}`}
	af, calls := newAgentFlowHarness(t, agent)

	_, err := af.Execute(context.Background(), testRequest)
	if err == nil {
		t.Fatal("expected capability error")
	}
	if !errors.Is(err, capability.ErrUnknownCapability) {
		t.Errorf("errors.Is(err, ErrUnknownCapability) = false, err = %v", err)
	}
	if *calls != 0 {
		t.Errorf("handler calls = %d, want 0", *calls)
	}
}

func TestAgentFlowNilGuards(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil Agent")
		}
	}()
	NewAgentFlow(nil, NewCapabilityFlow(capability.NewPlanExecutor(capability.NewDispatcher(capability.NewRegistry()))))

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil CapabilityFlow")
		}
	}()
	NewAgentFlow(&fakeAgent{}, nil)
}
