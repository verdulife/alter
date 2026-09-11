package capability

import (
	"fmt"
	"sort"
)

// Registry holds all registered capabilities and their handlers.
type Registry struct {
	capabilities map[string]Capability
	handlers     map[string]CapabilityHandler
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		capabilities: make(map[string]Capability),
		handlers:     make(map[string]CapabilityHandler),
	}
}

// Register adds a capability and its handler to the registry. It panics if the
// name is empty or already registered, or if the handler is nil.
func (r *Registry) Register(cap Capability, handler CapabilityHandler) {
	if cap.Name == "" {
		panic("capability: Register with empty name")
	}
	if handler == nil {
		panic(fmt.Sprintf("capability: Register with nil handler for %q", cap.Name))
	}
	if _, exists := r.capabilities[cap.Name]; exists {
		panic(fmt.Sprintf("capability: duplicate registration %q", cap.Name))
	}
	r.capabilities[cap.Name] = cap
	r.handlers[cap.Name] = handler
}

// Get returns the capability and handler for the given name. Returns
// ErrUnknownCapability if not found.
func (r *Registry) Get(name string) (Capability, CapabilityHandler, error) {
	cap, ok := r.capabilities[name]
	if !ok {
		return Capability{}, nil, fmt.Errorf("%w: %s", ErrUnknownCapability, name)
	}
	return cap, r.handlers[name], nil
}

// Has reports whether a capability with the given name is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.capabilities[name]
	return ok
}

// Names returns all registered capability names in sorted order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.capabilities))
	for name := range r.capabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
