package capability

// PlannerContext is the stable, serializable description of the capabilities
// available to the planner (the future Pi prompt builder). It is a snapshot
// document: it only describes what the planner MAY call. It is deliberately
// separate from Plan, which carries what the planner WANTS to call.
type PlannerContext struct {
	Capabilities []CatalogEntry `json:"capabilities"`
}

// PlannerContextBuilder derives PlannerContext documents from the Catalog,
// which remains the single source of truth for capability descriptions. It
// holds no capability data of its own: no duplicated names, descriptions or
// schemas, and no per-capability instructions for the planner.
type PlannerContextBuilder struct {
	catalog *Catalog
}

// NewPlannerContextBuilder creates a builder backed by the given Catalog. It
// panics if catalog is nil.
func NewPlannerContextBuilder(catalog *Catalog) *PlannerContextBuilder {
	if catalog == nil {
		panic("capability: NewPlannerContextBuilder with nil Catalog")
	}
	return &PlannerContextBuilder{catalog: catalog}
}

// Build returns the current context document derived from the catalog,
// deterministic and in the catalog's entry order. A fresh document is built on
// every call, so capabilities registered after builder construction appear
// automatically. An empty catalog yields a valid, empty context.
func (b *PlannerContextBuilder) Build() PlannerContext {
	return PlannerContext{Capabilities: b.catalog.Entries()}
}
