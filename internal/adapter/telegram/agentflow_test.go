package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/verdu/alter/internal/application"
	"github.com/verdu/alter/internal/capability"
	"github.com/verdu/alter/internal/domain"
)

// --- Test-only fakes ---------------------------------------------------------

// stubAgent is a domain.Agent that returns a fixed response/error and records
// the requests it received.
type stubAgent struct {
	calls []domain.AgentRequest
	resp  string
	err   error
}

func (s *stubAgent) Execute(_ context.Context, req domain.AgentRequest) (domain.AgentResult, error) {
	s.calls = append(s.calls, req)
	if s.err != nil {
		return domain.AgentResult{}, s.err
	}
	return domain.AgentResult{Response: s.resp}, nil
}

// stubCapability is a test capability handler.
type stubCapability struct {
	data any
}

func (s *stubCapability) Execute(_ context.Context, _ json.RawMessage) (capability.CapabilityResult, error) {
	return capability.CapabilityResult{Data: s.data}, nil
}

// seedListTasks registers a fake list_tasks capability producing test tasks.
func seedListTasks(reg *capability.Registry) {
	reg.Register(capability.Capability{
		Name:        "list_tasks",
		Description: "List all tasks",
		Parameters:  []byte(`{"type":"object","properties":{}}`),
	}, &stubCapability{data: capability.ListTasksResult{Tasks: []capability.TaskView{
		{ID: "t1", Title: "comprar leche", Status: "pending"},
		{ID: "t2", Title: "pasear al perro", Status: "pending"},
	}}})
}

// newTestFlow builds a real AgentFlow over a stub Agent and a real capability
// pipeline with the given registry seed.
func newTestFlow(t *testing.T, agent domain.Agent, seed func(*capability.Registry)) *application.AgentFlow {
	t.Helper()
	reg := capability.NewRegistry()
	if seed != nil {
		seed(reg)
	}
	return application.NewAgentFlow(
		agent,
		application.NewCapabilityFlow(capability.NewPlanExecutor(capability.NewDispatcher(reg))),
	)
}

// --- Tests -------------------------------------------------------------------

func TestAgentFlowHandlerPlanExecutesCapability(t *testing.T) {
	agent := &stubAgent{resp: `{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}`}
	flow := newTestFlow(t, agent, seedListTasks)
	h := NewAgentFlowHandler(flow)

	reply, err := h.Handle(context.Background(), "muestra mis tareas", nil)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	// The plan was executed: the reply is the human-readable task list.
	want := "Tus tareas (2):\n1) comprar leche\n2) pasear al perro"
	if reply != want {
		t.Errorf("reply = %q, want %q", reply, want)
	}
	if len(agent.calls) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(agent.calls))
	}
	if !strings.HasPrefix(agent.calls[0].Instruction, "User message: ") {
		t.Errorf("instruction = %q, want the neutral free-text framing", agent.calls[0].Instruction)
	}
}

func TestAgentFlowHandlerConversationKeepsPi(t *testing.T) {
	agent := &stubAgent{resp: "hola, ¿en qué te ayudo?"}
	flow := newTestFlow(t, agent, seedListTasks)
	h := NewAgentFlowHandler(flow)

	reply, err := h.Handle(context.Background(), "hola", nil)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if reply != "hola, ¿en qué te ayudo?" {
		t.Errorf("reply = %q, want the conversational Pi reply", reply)
	}
}

func TestAgentFlowHandlerFlowErrorSurfacesError(t *testing.T) {
	agent := &stubAgent{resp: "anything", err: errors.New("provider timeout")}
	flow := newTestFlow(t, agent, seedListTasks)
	h := NewAgentFlowHandler(flow)

	reply, err := h.Handle(context.Background(), "recuérdame algo", nil)
	if err == nil {
		t.Fatal("expected the Agent error to propagate")
	}
	if reply == "" {
		t.Error("expected a non-empty user-facing reply even on error")
	}
	if !strings.Contains(reply, MsgNLErrInterpret()) {
		t.Errorf("reply = %q, want the generic NL error text", reply)
	}
}

var _ = domain.AgentRequest{} // keep the domain import used (fakeAgent construction)
