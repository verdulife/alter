package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/verdu/alter/internal/capability"
)

// recordingHandler counts invocations and returns a fixed result.
type recordingHandler struct {
	calls *int
}

func (h recordingHandler) Execute(context.Context, json.RawMessage) (capability.CapabilityResult, error) {
	*h.calls++
	return capability.CapabilityResult{Data: "done"}, nil
}

// failingHandler counts invocations and returns a sentinel error.
type failingHandler struct {
	calls *int
	err   error
}

func (h failingHandler) Execute(context.Context, json.RawMessage) (capability.CapabilityResult, error) {
	*h.calls++
	return capability.CapabilityResult{}, h.err
}

var errFlowBoom = errors.New("flow handler boom")

// newFlowHarness builds a CapabilityFlow whose executor runs against a registry
// seeded via the provided callback.
func newFlowHarness(t *testing.T, seed func(*capability.Registry)) *CapabilityFlow {
	t.Helper()
	reg := capability.NewRegistry()
	seed(reg)
	return NewCapabilityFlow(capability.NewPlanExecutor(capability.NewDispatcher(reg)))
}

func registerWithSchema(t *testing.T, reg *capability.Registry, name string, h capability.CapabilityHandler) {
	t.Helper()
	reg.Register(capability.Capability{Name: name, Parameters: []byte(`{"type":"object","properties":{}}`)}, h)
}

func TestFlowConversationReturnsTextAndExecutesNothing(t *testing.T) {
	var calls int
	f := newFlowHarness(t, func(reg *capability.Registry) {
		registerWithSchema(t, reg, "list_tasks", recordingHandler{calls: &calls})
	})

	text := "  tienes 3 tareas pendientes {revisar} mañana  "
	result, err := f.Process(context.Background(), text)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Kind != capability.ResponseConversation {
		t.Errorf("Kind = %v, want conversation", result.Kind)
	}
	if result.Response != text {
		t.Errorf("Response = %q, want the original text %q", result.Response, text)
	}
	if calls != 0 {
		t.Errorf("calls = %d, want 0 (conversation must not execute)", calls)
	}
	if result.Results != nil {
		t.Errorf("Results = %v, want nil for conversation", result.Results)
	}
}

func TestFlowValidPlanExecutesAndReturnsResults(t *testing.T) {
	var calls int
	f := newFlowHarness(t, func(reg *capability.Registry) {
		registerWithSchema(t, reg, "list_tasks", recordingHandler{calls: &calls})
	})

	result, err := f.Process(context.Background(), `{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}`)
	if err != nil {
		t.Fatalf("Process: %v", err)
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
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestFlowEmptyPlanExecutesNothingAndReturnsNonNilEmpty(t *testing.T) {
	var calls int
	f := newFlowHarness(t, func(reg *capability.Registry) {
		registerWithSchema(t, reg, "list_tasks", recordingHandler{calls: &calls})
	})

	result, err := f.Process(context.Background(), `{"calls":[],"clarification":null}`)
	if err != nil {
		t.Fatalf("Process: %v", err)
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
	if calls != 0 {
		t.Errorf("calls = %d, want 0", calls)
	}
}

func TestFlowUnknownCapabilityPropagates(t *testing.T) {
	var calls int
	f := newFlowHarness(t, func(reg *capability.Registry) {
		registerWithSchema(t, reg, "list_tasks", recordingHandler{calls: &calls})
	})

	_, err := f.Process(context.Background(), `{"calls":[{"capability":"missing","args":{}}],"clarification":null}`)
	if err == nil {
		t.Fatal("expected error for unknown capability")
	}
	if !errors.Is(err, capability.ErrUnknownCapability) {
		t.Errorf("errors.Is(err, ErrUnknownCapability) = false, err = %v", err)
	}
	if calls != 0 {
		t.Errorf("calls = %d, want 0", calls)
	}
}

func TestFlowExecutionErrorPropagates(t *testing.T) {
	var calls int
	f := newFlowHarness(t, func(reg *capability.Registry) {
		registerWithSchema(t, reg, "boom", failingHandler{calls: &calls, err: errFlowBoom})
	})

	_, err := f.Process(context.Background(), `{"calls":[{"capability":"boom","args":{}}],"clarification":null}`)
	if err == nil {
		t.Fatal("expected execution error")
	}
	if !errors.Is(err, capability.ErrExecutionFailed) {
		t.Errorf("errors.Is(err, ErrExecutionFailed) = false, err = %v", err)
	}
	if !errors.Is(err, errFlowBoom) {
		t.Errorf("errors.Is(err, inner) = false, err = %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestFlowInvalidPlanPropagatesAndExecutesNothing(t *testing.T) {
	var calls int
	f := newFlowHarness(t, func(reg *capability.Registry) {
		registerWithSchema(t, reg, "list_tasks", recordingHandler{calls: &calls})
	})

	_, err := f.Process(context.Background(), `{"calls":[`)
	if err == nil {
		t.Fatal("expected error for invalid plan")
	}
	if !errors.Is(err, capability.ErrInvalidPlan) {
		t.Errorf("errors.Is(err, ErrInvalidPlan) = false, err = %v", err)
	}
	if calls != 0 {
		t.Errorf("calls = %d, want 0 (invalid plan must not execute)", calls)
	}
}

func TestFlowNilExecutorPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil PlanExecutor")
		}
	}()
	NewCapabilityFlow(nil)
}
