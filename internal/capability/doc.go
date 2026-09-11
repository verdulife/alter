// Package capability provides the capability framework for ALTER.
//
// # Allowlist semantics
//
// A registered capability is a capability available to callers. The catalog
// exposed to the planner IS the allowlist. There is no dynamic configuration,
// no environment toggles, and no runtime enable/disable — the set of registered
// capabilities at build time defines exactly what callers may invoke.
package capability
