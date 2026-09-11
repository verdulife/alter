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
	}, &stubCapability{data: map[string]any{
		"tasks": []map[string]any{{"title": "comprar leche"}, {"title": "pasear al perro"}},
	}})
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

// stubRecognizer implements telegram.NaturalRecognizer with a scripted answer.
type stubRecognizer struct {
	reply      string
	recognized bool
	err        error
}

func (s *stubRecognizer) HandleMessageStructured(_ context.Context, _ string) (string, bool, error) {
	return s.reply, s.recognized, s.err
}

// --- Tests -------------------------------------------------------------------

func TestAgentFlowHandlerPlanExecutesCapability(t *testing.T) {
	agent := &stubAgent{resp: `{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}`}
	flow := newTestFlow(t, agent, seedListTasks)
	h := NewAgentFlowHandler(flow, nil)

	reply, err := h.Handle(context.Background(), "muestra mis tareas")
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	// The plan was executed: the reply is the JSON of the capability results.
	if !strings.Contains(reply, "comprar leche") || !strings.Contains(reply, "pasear al perro") {
		t.Errorf("reply = %q, want executed list_tasks results", reply)
	}
	if len(agent.calls) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(agent.calls))
	}
	if !strings.HasPrefix(agent.calls[0].Instruction, "User message: ") {
		t.Errorf("instruction = %q, want the neutral free-text framing", agent.calls[0].Instruction)
	}
}

func TestAgentFlowHandlerConversationFallsBackWhenRecognized(t *testing.T) {
	agent := &stubAgent{resp: "claro, dime qué necesitas"}
	flow := newTestFlow(t, agent, seedListTasks)
	fallback := &stubRecognizer{
		reply:      "Tarea creada: comprar SSD ✓",
		recognized: true,
	}
	h := NewAgentFlowHandler(flow, fallback)

	reply, err := h.Handle(context.Background(), "comprar SSD")
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if reply != "Tarea creada: comprar SSD ✓" {
		t.Errorf("reply = %q, want the recognized fallback reply", reply)
	}
}

func TestAgentFlowHandlerConversationKeepsPiWhenFallbackNotRecognized(t *testing.T) {
	agent := &stubAgent{resp: "buenos días, ¿en qué te ayudo?"}
	flow := newTestFlow(t, agent, seedListTasks)
	fallback := &stubRecognizer{
		reply:      "Hola, soy ALTER. Puedo crear tareas y recordatorios.",
		recognized: false,
	}
	h := NewAgentFlowHandler(flow, fallback)

	reply, err := h.Handle(context.Background(), "hola")
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if reply != "buenos días, ¿en qué te ayudo?" {
		t.Errorf("reply = %q, want the conversational Pi reply", reply)
	}
}

func TestAgentFlowHandlerConversationWithoutFallbackKeepsPi(t *testing.T) {
	agent := &stubAgent{resp: "hola, ¿en qué te ayudo?"}
	flow := newTestFlow(t, agent, seedListTasks)
	h := NewAgentFlowHandler(flow, nil)

	reply, err := h.Handle(context.Background(), "hola")
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if reply != "hola, ¿en qué te ayudo?" {
		t.Errorf("reply = %q, want the conversational Pi reply", reply)
	}
}

func TestAgentFlowHandlerFlowErrorFallsBackWhenRecognized(t *testing.T) {
	// The Agent fails, so Handle() takes the flow-error branch: the fallback is
	// consulted and its recognized reply wins.
	agent := &stubAgent{resp: "irrelevant", err: errors.New("provider timeout")}
	flow := newTestFlow(t, agent, seedListTasks)
	fallback := &stubRecognizer{
		reply:      "Recordatorio creado ✓",
		recognized: true,
	}
	h := NewAgentFlowHandler(flow, fallback)

	reply, err := h.Handle(context.Background(), "recuérdame algo")
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if reply != "Recordatorio creado ✓" {
		t.Errorf("reply = %q, want the fallback reply on flow error", reply)
	}
}

func TestAgentFlowHandlerFlowErrorWithoutFallbackSurfacesError(t *testing.T) {
	agent := &stubAgent{resp: "anything", err: errors.New("provider timeout")}
	flow := newTestFlow(t, agent, seedListTasks)
	h := NewAgentFlowHandler(flow, nil)

	reply, err := h.Handle(context.Background(), "recuérdame algo")
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

var _ NaturalRecognizer = (*stubRecognizer)(nil)
var _ = domain.AgentRequest{} // keep the domain import used (fakeAgent construction)
