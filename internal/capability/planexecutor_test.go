package capability

import (
	"context"
	"errors"
	"testing"
)

func TestPlanExecutorValidOneCall(t *testing.T) {
	ts, _ := buildTaskService()
	reg := registerListTasks(t, ts)
	pe := NewPlanExecutor(NewDispatcher(reg))

	results, err := pe.Execute(context.Background(), []byte(`{"calls":[{"capability":"list_tasks","args":{}}],"clarification":null}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if _, ok := results[0].Data.(ListTasksResult); !ok {
		t.Errorf("Data is %T, want ListTasksResult", results[0].Data)
	}
}

func TestPlanExecutorEmptyCalls(t *testing.T) {
	pe := NewPlanExecutor(NewDispatcher(NewRegistry()))
	results, err := pe.Execute(context.Background(), []byte(`{"calls":[],"clarification":null}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if results == nil {
		t.Fatal("results is nil, want non-nil empty slice")
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

func TestPlanExecutorInvalidJSON(t *testing.T) {
	pe := NewPlanExecutor(NewDispatcher(NewRegistry()))
	results, err := pe.Execute(context.Background(), []byte(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !errors.Is(err, ErrInvalidPlan) {
		t.Errorf("errors.Is(err, ErrInvalidPlan) = false, err = %v", err)
	}
	if results != nil {
		t.Errorf("results = %v, want nil", results)
	}
}

func TestPlanExecutorUnknownCapability(t *testing.T) {
	pe := NewPlanExecutor(NewDispatcher(NewRegistry()))
	results, err := pe.Execute(context.Background(), []byte(`{"calls":[{"capability":"missing","args":{}}],"clarification":null}`))
	if err == nil {
		t.Fatal("expected error for unknown capability")
	}
	if !errors.Is(err, ErrUnknownCapability) {
		t.Errorf("errors.Is(err, ErrUnknownCapability) = false, err = %v", err)
	}
	if results != nil {
		t.Errorf("results = %v, want nil", results)
	}
}

func TestPlanExecutorNilDispatcherPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil Dispatcher")
		}
	}()
	NewPlanExecutor(nil)
}
