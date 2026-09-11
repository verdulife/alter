package capability

import (
	"encoding/json"
)

// CatalogEntry is the stable, JSON-serializable description of one capability
// exposed to planner-facing layers (e.g. a future prompt builder). It carries
// only descriptive data: no handlers, no registry internals.
type CatalogEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Catalog is the descriptive catalog of available capabilities. It is derived
// from the Registry, which is the single source of truth: there is no separate
// manual list, and a capability registered after Catalog construction appears
// in subsequent Entries() calls automatically.
type Catalog struct {
	registry *Registry
}

// NewCatalog creates a Catalog backed by the given Registry. It panics if
// registry is nil.
func NewCatalog(registry *Registry) *Catalog {
	if registry == nil {
		panic("capability: NewCatalog with nil Registry")
	}
	return &Catalog{registry: registry}
}

// Entries returns the catalog entries in deterministic order (sorted by name).
// A fresh slice is built on every call, so it always reflects the current
// registry contents.
func (c *Catalog) Entries() []CatalogEntry {
	names := c.registry.Names()
	entries := make([]CatalogEntry, 0, len(names))
	for _, name := range names {
		cap, _, err := c.registry.Get(name)
		if err != nil {
			continue // registry invariants forbid this; skip defensively
		}
		entries = append(entries, CatalogEntry{
			Name:        cap.Name,
			Description: cap.Description,
			Parameters:  cap.Parameters,
		})
	}
	return entries
}
