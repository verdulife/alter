package capability

import (
	"encoding/json"
	"testing"
)

func TestPlannerContextEmptyRegistry(t *testing.T) {
	b := NewPlannerContextBuilder(NewCatalog(NewRegistry()))
	ctx := b.Build()
	if ctx.Capabilities == nil {
		t.Fatal("Capabilities is nil, want non-nil empty slice")
	}
	if len(ctx.Capabilities) != 0 {
		t.Errorf("len(Capabilities) = %d, want 0", len(ctx.Capabilities))
	}
	got, err := json.Marshal(ctx)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != `{"capabilities":[]}` {
		t.Errorf("JSON = %s, want {\"capabilities\":[]}", got)
	}
}

func TestPlannerContextSingleCapability(t *testing.T) {
	r := NewRegistry()
	r.Register(
		Capability{Name: "list_tasks", Description: "list all tasks",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
		fixedHandler{},
	)
	b := NewPlannerContextBuilder(NewCatalog(r))

	ctx := b.Build()
	if len(ctx.Capabilities) != 1 {
		t.Fatalf("len(Capabilities) = %d, want 1", len(ctx.Capabilities))
	}
	e := ctx.Capabilities[0]
	if e.Name != "list_tasks" || e.Description != "list all tasks" {
		t.Errorf("entry = %+v, want list_tasks/list all tasks", e)
	}
	if string(e.Parameters) != `{"type":"object","properties":{}}` {
		t.Errorf("Parameters = %s, want the registered schema", e.Parameters)
	}
}

func TestPlannerContextDeterministicOrder(t *testing.T) {
	r := NewRegistry()
	r.Register(Capability{Name: "zeta", Description: "z"}, fixedHandler{})
	r.Register(Capability{Name: "beta", Description: "b"}, fixedHandler{})
	r.Register(Capability{Name: "alpha", Description: "a"}, fixedHandler{})
	b := NewPlannerContextBuilder(NewCatalog(r))

	first := b.Build()
	second := b.Build()
	want := []string{"alpha", "beta", "zeta"}
	for i, name := range want {
		if first.Capabilities[i].Name != name {
			t.Errorf("first[%d].Name = %q, want %q", i, first.Capabilities[i].Name, name)
		}
		if first.Capabilities[i].Name != second.Capabilities[i].Name {
			t.Errorf("non-deterministic: %+v vs %+v", first, second)
		}
	}
}

func TestPlannerContextLateRegistrationAppears(t *testing.T) {
	r := NewRegistry()
	r.Register(Capability{Name: "alpha", Description: "a"}, fixedHandler{})
	b := NewPlannerContextBuilder(NewCatalog(r))

	if len(b.Build().Capabilities) != 1 {
		t.Fatal("expected 1 capability before late registration")
	}
	r.Register(Capability{Name: "gamma", Description: "g"}, fixedHandler{})
	ctx := b.Build()
	if len(ctx.Capabilities) != 2 {
		t.Fatalf("len(Capabilities) = %d, want 2 after late registration", len(ctx.Capabilities))
	}
	if ctx.Capabilities[0].Name != "alpha" || ctx.Capabilities[1].Name != "gamma" {
		t.Errorf("capabilities = %+v, want [alpha gamma]", ctx.Capabilities)
	}
}

func TestPlannerContextStableJSONShape(t *testing.T) {
	r := NewRegistry()
	r.Register(
		Capability{Name: "list_tasks", Description: "list all tasks",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
		fixedHandler{},
	)
	b := NewPlannerContextBuilder(NewCatalog(r))

	ctx := b.Build()
	got, err := json.Marshal(ctx)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(doc) != 1 {
		t.Errorf("document keys = %v, want exactly [capabilities]", doc)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(doc["capabilities"], &entries); err != nil {
		t.Fatalf("Unmarshal capabilities: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if len(entries[0]) != 3 {
		t.Errorf("entry keys = %v, want exactly [name description parameters]", entries[0])
	}
	for _, k := range []string{"name", "description", "parameters"} {
		if _, ok := entries[0][k]; !ok {
			t.Errorf("missing entry key %q in %s", k, got)
		}
	}
}

func TestPlannerContextNilCatalogPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil Catalog")
		}
	}()
	NewPlannerContextBuilder(nil)
}
