package capability

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// recordingHandler is a programmable CapabilityHandler that records each
// invocation in a shared order slice and optionally fails.
type recordingHandler struct {
	order *[]string
	mark  string
	err   error
}

func (h recordingHandler) Execute(context.Context, json.RawMessage) (CapabilityResult, error) {
	*h.order = append(*h.order, h.mark)
	if h.err != nil {
		return CapabilityResult{}, h.err
	}
	return CapabilityResult{Data: h.mark}, nil
}

func registerRecording(t *testing.T, r *Registry, name string, h CapabilityHandler) {
	t.Helper()
	r.Register(Capability{Name: name, Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}, h)
}

func TestExecutePlanEmpty(t *testing.T) {
	d := NewDispatcher(NewRegistry())
	results, err := d.ExecutePlan(context.Background(), Plan{Calls: []Call{}})
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	if results == nil {
		t.Fatal("results is nil, want non-nil empty slice")
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

func TestExecutePlanSingleValidCall(t *testing.T) {
	ts, _ := buildTaskService()
	r := registerListTasks(t, ts)
	d := NewDispatcher(r)

	results, err := d.ExecutePlan(context.Background(), Plan{
		Calls: []Call{{Capability: "list_tasks", Args: json.RawMessage(`{}`)}},
	})
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if _, ok := results[0].Data.(ListTasksResult); !ok {
		t.Errorf("Data is %T, want ListTasksResult", results[0].Data)
	}
}

func TestExecutePlanPreservesOrder(t *testing.T) {
	var order []string
	r := NewRegistry()
	registerRecording(t, r, "first", recordingHandler{order: &order, mark: "first"})
	registerRecording(t, r, "second", recordingHandler{order: &order, mark: "second"})
	registerRecording(t, r, "third", recordingHandler{order: &order, mark: "third"})
	d := NewDispatcher(r)

	plan := Plan{Calls: []Call{
		{Capability: "third", Args: json.RawMessage(`{}`)},
		{Capability: "first", Args: json.RawMessage(`{}`)},
		{Capability: "second", Args: json.RawMessage(`{}`)},
		{Capability: "first", Args: json.RawMessage(`{}`)},
	}}
	results, err := d.ExecutePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	wantOrder := []string{"third", "first", "second", "first"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	for i, mark := range wantOrder {
		if order[i] != mark {
			t.Errorf("order[%d] = %q, want %q (full order %v)", i, order[i], mark, order)
		}
	}
	if len(results) != 4 {
		t.Errorf("len(results) = %d, want 4", len(results))
	}
}

func TestExecutePlanUnknownCallStops(t *testing.T) {
	var order []string
	r := NewRegistry()
	registerRecording(t, r, "first", recordingHandler{order: &order, mark: "first"})
	d := NewDispatcher(r)

	plan := Plan{Calls: []Call{
		{Capability: "first", Args: json.RawMessage(`{}`)},
		{Capability: "missing", Args: json.RawMessage(`{}`)},
	}}
	results, err := d.ExecutePlan(context.Background(), plan)
	if err == nil {
		t.Fatal("expected error for unknown capability")
	}
	if !errors.Is(err, ErrUnknownCapability) {
		t.Errorf("errors.Is(err, ErrUnknownCapability) = false, err = %v", err)
	}
	if results != nil {
		t.Errorf("results = %v, want nil on stop", results)
	}
	if len(order) != 1 || order[0] != "first" {
		t.Errorf("order = %v, want [first] (call after the unknown one must not run)", order)
	}
}

func TestExecutePlanExecutionErrorStopsLaterCalls(t *testing.T) {
	var order []string
	r := NewRegistry()
	registerRecording(t, r, "ok", recordingHandler{order: &order, mark: "ok"})
	registerRecording(t, r, "boom", recordingHandler{order: &order, mark: "boom", err: errInner})
	registerRecording(t, r, "after", recordingHandler{order: &order, mark: "after"})
	d := NewDispatcher(r)

	plan := Plan{Calls: []Call{
		{Capability: "ok", Args: json.RawMessage(`{}`)},
		{Capability: "boom", Args: json.RawMessage(`{}`)},
		{Capability: "after", Args: json.RawMessage(`{}`)},
	}}
	results, err := d.ExecutePlan(context.Background(), plan)
	if err == nil {
		t.Fatal("expected execution error")
	}
	if !errors.Is(err, ErrExecutionFailed) {
		t.Errorf("errors.Is(err, ErrExecutionFailed) = false, err = %v", err)
	}
	if !errors.Is(err, errInner) {
		t.Errorf("errors.Is(err, errInner) = false, err = %v", err)
	}
	if results != nil {
		t.Errorf("results = %v, want nil on stop", results)
	}
	wantOrder := []string{"ok", "boom"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	for i, mark := range wantOrder {
		if order[i] != mark {
			t.Errorf("order[%d] = %q, want %q (full order %v)", i, order[i], mark, order)
		}
	}
}
