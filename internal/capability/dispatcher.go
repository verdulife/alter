package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
)

// Dispatcher routes capability invocations through the registry.
type Dispatcher struct {
	registry *Registry
}

// NewDispatcher creates a Dispatcher backed by the given Registry. It panics if
// registry is nil.
func NewDispatcher(registry *Registry) *Dispatcher {
	if registry == nil {
		panic("capability: NewDispatcher with nil registry")
	}
	return &Dispatcher{registry: registry}
}

// Dispatch resolves the named capability, validates args against the
// capability's Parameters schema (the single source of truth), and executes the
// handler. Validation failures are returned as ErrInvalidArgs; handler failures
// are wrapped with ErrExecutionFailed, preserving the full error chain.
func (d *Dispatcher) Dispatch(ctx context.Context, name string, args json.RawMessage) (CapabilityResult, error) {
	capDef, handler, err := d.registry.Get(name)
	if err != nil {
		return CapabilityResult{}, err
	}

	if err := validateArgs(capDef.Parameters, args); err != nil {
		return CapabilityResult{}, err
	}

	result, err := handler.Execute(ctx, args)
	if err != nil {
		return CapabilityResult{}, fmt.Errorf("%w: %w", ErrExecutionFailed, err)
	}
	return result, nil
}

// ExecutePlan executes a plan's calls exactly in the received order, reusing
// Dispatch for each call. Successful calls accumulate their CapabilityResult;
// on the first failing call the plan stops and the error is returned as-is
// (preserving the ErrUnknownCapability / ErrInvalidArgs / ErrExecutionFailed
// semantics of Dispatch). A plan with no calls produces zero results and no
// error. There is no rollback, compensation or parallel execution in V1.
func (d *Dispatcher) ExecutePlan(ctx context.Context, plan Plan) ([]CapabilityResult, error) {
	results := make([]CapabilityResult, 0, len(plan.Calls))
	for _, call := range plan.Calls {
		result, err := d.Dispatch(ctx, call.Capability, call.Args)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

// ---------------------------------------------------------------------------
// V1 argument validation
// ---------------------------------------------------------------------------

// validateArgs validates args against the capability's JSON Schema.
//
// V1 supports a deliberately small subset of JSON Schema, and it fails closed
// on anything outside that subset:
//   - the schema must declare type "object"
//   - declared properties may carry type checks for "string", "number",
//     "integer", "boolean", "object", "array", "null", or an any-of list
//     ([]string) of those type names
//   - property values are NOT validated recursively (V1 bound): structures
//     inside object/array values are not checked
//   - unknown properties are rejected unless the schema explicitly sets
//     "additionalProperties": true
//   - an empty or nil schema behaves as an object schema with no allowed
//     properties (strict)
//
// Fail-closed invariants: if the schema bytes cannot be parsed, if the top-level
// type is not the string "object", or if any declared property schema is
// malformed, args are rejected with ErrInvalidArgs regardless of their content.
func validateArgs(schema json.RawMessage, args json.RawMessage) error {
	s, err := parseArgSchema(schema)
	if err != nil {
		return err
	}

	m, err := parseArgsObject(args)
	if err != nil {
		return err
	}

	for _, name := range s.required {
		raw, ok := m[name]
		if !ok || isJSONNull(raw) {
			return fmt.Errorf("%w: missing required property %q", ErrInvalidArgs, name)
		}
	}

	for name, raw := range m {
		ps, declared := s.properties[name]
		if !declared {
			if !s.allowUnknown {
				return fmt.Errorf("%w: unknown property %q", ErrInvalidArgs, name)
			}
			continue
		}
		if err := validatePropertyValue(ps, raw); err != nil {
			return err
		}
	}
	return nil
}

// argSchema is the parsed form of a capability's Parameters schema.
type argSchema struct {
	properties   map[string]propSchema
	required     []string
	allowUnknown bool // set only by an explicit "additionalProperties": true
}

// propSchema describes a single declared property.
type propSchema struct {
	// types holds the allowed type names. An any-of list (length > 1) matches
	// when the value satisfies at least one alternative. Length is always >= 1.
	types []string
}

// supportedTypes are the only property types V1 validates.
var supportedTypes = map[string]bool{
	"string":  true,
	"number":  true,
	"integer": true,
	"boolean": true,
	"object":  true,
	"array":   true,
	"null":    true,
}

// parseArgSchema parses and validates the capability schema, failing closed on
// anything outside the V1 subset. An empty or nil schema is a valid strict
// object schema with no allowed properties.
func parseArgSchema(raw json.RawMessage) (argSchema, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return argSchema{properties: map[string]propSchema{}}, nil
	}

	var s struct {
		Type                 json.RawMessage            `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties json.RawMessage            `json:"additionalProperties"`
		Required             []string                   `json:"required"`
	}
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return argSchema{}, fmt.Errorf("%w: unparseable schema", ErrInvalidArgs)
	}

	var topType string
	if err := json.Unmarshal(s.Type, &topType); err != nil {
		return argSchema{}, fmt.Errorf("%w: schema type must be the single string %q", ErrInvalidArgs, "object")
	}
	if topType != "object" {
		return argSchema{}, fmt.Errorf("%w: schema type must be %q, got %q", ErrInvalidArgs, "object", topType)
	}

	schema := argSchema{
		properties: make(map[string]propSchema, len(s.Properties)),
		required:   s.Required,
	}
	var additional bool
	if err := json.Unmarshal(s.AdditionalProperties, &additional); err == nil && additional {
		schema.allowUnknown = true
	}

	for name, raw := range s.Properties {
		ps, err := parsePropSchema(raw)
		if err != nil {
			return argSchema{}, err
		}
		schema.properties[name] = ps
	}
	return schema, nil
}

// parsePropSchema parses a single declared property schema, failing closed if
// its type is missing, malformed, or outside the supported subset.
func parsePropSchema(raw json.RawMessage) (propSchema, error) {
	var ps struct {
		Type json.RawMessage `json:"type"`
	}
	if err := json.Unmarshal(raw, &ps); err != nil {
		return propSchema{}, fmt.Errorf("%w: unparseable property schema", ErrInvalidArgs)
	}
	rawType := bytes.TrimSpace(ps.Type)
	if len(rawType) == 0 {
		return propSchema{}, fmt.Errorf("%w: property schema without a type is not supported", ErrInvalidArgs)
	}

	var types []string
	if err := json.Unmarshal(rawType, &types); err == nil {
		if len(types) == 0 {
			return propSchema{}, fmt.Errorf("%w: empty any-of type list", ErrInvalidArgs)
		}
	} else {
		var t string
		if err := json.Unmarshal(rawType, &t); err != nil {
			return propSchema{}, fmt.Errorf("%w: property type must be a string or []string", ErrInvalidArgs)
		}
		types = []string{t}
	}

	for _, t := range types {
		if !supportedTypes[t] {
			return propSchema{}, fmt.Errorf("%w: unsupported property type %q", ErrInvalidArgs, t)
		}
	}
	return propSchema{types: types}, nil
}

// validatePropertyValue type-checks one declared property against its schema.
// Nested structures are NOT validated recursively (V1 bound).
func validatePropertyValue(ps propSchema, raw json.RawMessage) error {
	got, num, ok := valueType(raw)
	if !ok {
		return fmt.Errorf("%w: property has invalid JSON value", ErrInvalidArgs)
	}
	for _, want := range ps.types {
		if typeMatches(want, got, num) {
			return nil
		}
	}
	return fmt.Errorf("%w: property type %q not allowed", ErrInvalidArgs, got)
}

// typeMatches reports whether a JSON value (got, plus its decoded number num
// for numeric checks) satisfies the declared type name want.
func typeMatches(want, got string, num float64) bool {
	if want == "integer" {
		return got == "number" && math.Trunc(num) == num
	}
	return got == want
}

// valueType classifies a JSON value. It returns the JSON type name and, for
// numbers, the decoded float64 value (used by the integer check). json.Unmarshal
// decodes every JSON number as float64, so integrality must be tested explicitly.
func valueType(raw json.RawMessage) (kind string, num float64, ok bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", 0, false
	}
	switch x := v.(type) {
	case string:
		return "string", 0, true
	case float64:
		return "number", x, true
	case bool:
		return "boolean", 0, true
	case nil:
		return "null", 0, true
	case []any:
		return "array", 0, true
	case map[string]any:
		return "object", 0, true
	default:
		return "", 0, false
	}
}

// parseArgsObject normalizes args into a JSON object map. nil, empty,
// whitespace-only and literal "null" args are treated as absent (an empty
// object). Any other non-object JSON is rejected with ErrInvalidArgs.
func parseArgsObject(args json.RawMessage) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, fmt.Errorf("%w: args must be a JSON object", ErrInvalidArgs)
	}
	return m, nil
}

// isJSONNull reports whether the raw value is the JSON literal null.
func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}
