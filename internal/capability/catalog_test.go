package capability

import (
	"encoding/json"
	"testing"
)

func TestCatalogEmptyRegistry(t *testing.T) {
	c := NewCatalog(NewRegistry())
	first := c.Entries()
	if len(first) != 0 {
		t.Fatalf("len(Entries) = %d, want 0", len(first))
	}
	second := c.Entries()
	if len(second) != 0 {
		t.Fatalf("second Entries len = %d, want 0", len(second))
	}
}

func TestCatalogSingleEntry(t *testing.T) {
	r := NewRegistry()
	r.Register(
		Capability{Name: "list_tasks", Description: "list all tasks",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
		fixedHandler{},
	)
	c := NewCatalog(r)

	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Name != "list_tasks" {
		t.Errorf("Name = %q, want %q", e.Name, "list_tasks")
	}
	if e.Description != "list all tasks" {
		t.Errorf("Description = %q, want %q", e.Description, "list all tasks")
	}
	if string(e.Parameters) != `{"type":"object","properties":{}}` {
		t.Errorf("Parameters = %s, want the registered schema", e.Parameters)
	}

	// The entry must serialize to exactly the three descriptive keys.
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(got, &keys); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(keys) != 3 {
		t.Errorf("serialized keys = %v, want exactly [name description parameters]", keys)
	}
	for _, k := range []string{"name", "description", "parameters"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("missing serialized key %q in %s", k, got)
		}
	}
}

func TestCatalogDeterministicOrder(t *testing.T) {
	r := NewRegistry()
	r.Register(Capability{Name: "zeta", Description: "z"}, fixedHandler{})
	r.Register(Capability{Name: "beta", Description: "b"}, fixedHandler{})
	r.Register(Capability{Name: "alpha", Description: "a"}, fixedHandler{})
	c := NewCatalog(r)

	first := c.Entries()
	second := c.Entries()
	if len(first) != 3 {
		t.Fatalf("len(Entries) = %d, want 3", len(first))
	}
	want := []string{"alpha", "beta", "zeta"}
	for i, name := range want {
		if first[i].Name != name {
			t.Errorf("first[%d].Name = %q, want %q (full %v)", i, first[i].Name, name, namesOf(first))
		}
		if second[i].Name != first[i].Name {
			t.Errorf("non-deterministic order: %v vs %v", namesOf(first), namesOf(second))
		}
	}
}

func TestCatalogReflectsLateRegistration(t *testing.T) {
	r := NewRegistry()
	r.Register(Capability{Name: "alpha", Description: "a"}, fixedHandler{})
	c := NewCatalog(r)

	if len(c.Entries()) != 1 {
		t.Fatalf("len(Entries) = %d, want 1 before late registration", len(c.Entries()))
	}

	r.Register(Capability{Name: "gamma", Description: "g"}, fixedHandler{})
	entries := c.Entries()
	if len(entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2 after late registration", len(entries))
	}
	if entries[0].Name != "alpha" || entries[1].Name != "gamma" {
		t.Errorf("entries = %v, want [alpha gamma]", namesOf(entries))
	}
}

func TestCatalogNoHandlerExposure(t *testing.T) {
	r := NewRegistry()
	r.Register(Capability{Name: "list_tasks", Description: "d",
		Parameters: json.RawMessage(`{"type":"object"}`)}, fixedHandler{})
	c := NewCatalog(r)
	e := c.Entries()[0]
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	str := string(got)
	if len(c.Entries()) != 1 {
		t.Errorf("entries changed unexpectedly: %v", namesOf(c.Entries()))
	}
	if e.Name != "list_tasks" {
		t.Errorf("Name = %q, want list_tasks", e.Name)
	}
	if len(str) == 0 {
		t.Error("empty JSON")
	}
}

func namesOf(entries []CatalogEntry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}
