package capability

import (
	"context"
	"encoding/json"
	"errors"
)

// Sentinel errors for capability operations.
var (
	// ErrUnknownCapability is returned when a capability name is not registered.
	ErrUnknownCapability = errors.New("unknown capability")
	// ErrInvalidArgs is returned when the provided arguments do not match the
	// capability's schema.
	ErrInvalidArgs = errors.New("invalid arguments")
	// ErrExecutionFailed wraps errors returned by a capability handler during
	// execution.
	ErrExecutionFailed = errors.New("capability execution failed")
	// ErrInvalidPlan is returned when input cannot be parsed as a Plan.
	ErrInvalidPlan = errors.New("invalid plan")
)

// Capability describes a registered capability: its name, human-readable
// description, and the JSON Schema of its accepted arguments.
//
// Capability is the single source of truth for the schema. The Parameters field
// holds the JSON Schema that the dispatcher validates against; handlers do not
// carry their own schema.
type Capability struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema for the capability's arguments.
}

// CapabilityResult is the uniform return type for all capability executions.
// It represents a successful result. Failures travel as Go errors only.
type CapabilityResult struct {
	Data any `json:"data"`
}

// CapabilityHandler is the interface every capability must implement.
type CapabilityHandler interface {
	// Execute runs the capability with the given arguments and returns a result.
	// Args have already been validated against the capability's Parameters schema
	// before Execute is called.
	Execute(ctx context.Context, args json.RawMessage) (CapabilityResult, error)
}
