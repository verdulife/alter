package capability

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Table-driven tests for validateArgs
// ---------------------------------------------------------------------------

func TestValidateArgsSchemaEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		args    string // "" → nil
		wantErr bool
		errIs   error
		wantMsg string // substring expected in the error message
	}{
		{
			name:   "nil schema, nil args → OK (strict object, no properties)",
			schema: "", args: "", wantErr: false,
		},
		{
			name:   "nil schema, empty object → OK",
			schema: "", args: `{}`, wantErr: false,
		},
		{
			name:   "nil schema, whitespace args → OK (treated absent)",
			schema: "", args: "  \t\n  ", wantErr: false,
		},
		{
			name:   "nil schema, null → OK",
			schema: "", args: `null`, wantErr: false,
		},
		{
			name:   "nil schema, unknown key → reject",
			schema: "", args: `{"x":1}`, wantErr: true, errIs: ErrInvalidArgs,
			wantMsg: "unknown property",
		},
		{
			name:   "nil schema, array args → reject",
			schema: "", args: `[]`, wantErr: true, errIs: ErrInvalidArgs,
		},
		{
			name:   "nil schema, number args → reject",
			schema: "", args: `123`, wantErr: true, errIs: ErrInvalidArgs,
		},
		{
			name:   "nil schema, boolean args → reject",
			schema: "", args: `true`, wantErr: true, errIs: ErrInvalidArgs,
		},
		{
			name:   "nil schema, string args → reject",
			schema: "", args: `"hello"`, wantErr: true, errIs: ErrInvalidArgs,
		},
		{
			name:   "nil schema, malformed args → reject",
			schema: "", args: `{bad`, wantErr: true, errIs: ErrInvalidArgs,
		},
		{
			name:   "unparseable schema → fail closed",
			schema: `{broken`, args: `{}`, wantErr: true, errIs: ErrInvalidArgs,
			wantMsg: "unparseable schema",
		},
		{
			name:   "schema type string → reject",
			schema: `{"type":"string"}`, args: `{}`, wantErr: true, errIs: ErrInvalidArgs,
			wantMsg: "must be \"object\"",
		},
		{
			name:   "schema missing type → reject",
			schema: `{"properties":{"a":{"type":"string"}}}`, args: `{"a":"x"}`, wantErr: true, errIs: ErrInvalidArgs,
		},
		{
			name:   "schema type array → reject (V1 top-level is scalar only)",
			schema: `{"type":["object"]}`, args: `{}`, wantErr: true, errIs: ErrInvalidArgs,
		},
		{
			name:   "schema type null → reject",
			schema: `{"type":"null"}`, args: `{}`, wantErr: true, errIs: ErrInvalidArgs,
		},
		{
			name:   "additionalProperties object form → treated as false",
			schema: `{"type":"object","additionalProperties":{"type":"string"}}`, args: `{"a":"x"}`, wantErr: true, errIs: ErrInvalidArgs,
			wantMsg: "unknown property",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var schema, args json.RawMessage
			if tt.schema != "" {
				schema = json.RawMessage(tt.schema)
			}
			if tt.args != "" {
				args = json.RawMessage(tt.args)
			} else {
				args = nil
			}
			err := validateArgs(schema, args)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr {
				if tt.errIs != nil && !errIsRecursive(err, tt.errIs) {
					t.Errorf("errors.Is(err, %v) = false, err = %v", tt.errIs, err)
				}
				if tt.wantMsg != "" && !contains(err.Error(), tt.wantMsg) {
					t.Errorf("error message %q does not contain %q", err.Error(), tt.wantMsg)
				}
			}
		})
	}
}

func TestValidateArgsPropertyTypes(t *testing.T) {
	schema := `{"type":"object","properties":{` +
		`"s":{"type":"string"},` +
		`"n":{"type":"number"},` +
		`"i":{"type":"integer"},` +
		`"b":{"type":"boolean"},` +
		`"o":{"type":"object"},` +
		`"a":{"type":"array"},` +
		`"u":{"type":"null"}` +
		`}}`

	tests := []struct {
		name    string
		args    string
		wantErr bool
	}{
		// string
		{"string OK", `{"s":"hello"}`, false},
		{"string rejects number", `{"s":123}`, true},
		{"string rejects bool", `{"s":true}`, true},
		{"string rejects null", `{"s":null}`, true},
		// number
		{"number OK int", `{"n":5}`, false},
		{"number OK float", `{"n":1.5}`, false},
		{"number OK negative", `{"n":-3.14}`, false},
		{"number rejects string", `{"n":"5"}`, true},
		{"number rejects null", `{"n":null}`, true},
		// integer
		{"integer OK zero", `{"i":0}`, false},
		{"integer OK negative", `{"i":-7}`, false},
		{"integer OK large", `{"i":1e4}`, false},
		{"integer rejects float", `{"i":1.5}`, true},
		{"integer rejects 1.0 tricky", `{"i":1}`, false},     // 1.0 is integral
		{"integer rejects non-int float", `{"i":0.1}`, true}, // 0.1 is not integral
		{"integer rejects string", `{"i":"5"}`, true},
		// boolean
		{"boolean OK true", `{"b":true}`, false},
		{"boolean OK false", `{"b":false}`, false},
		{"boolean rejects 1", `{"b":1}`, true},
		{"boolean rejects string", `{"b":"true"}`, true},
		// object
		{"object OK empty", `{"o":{}}`, false},
		{"object OK nested", `{"o":{"a":1}}`, false},
		{"object rejects array", `{"o":[]}`, true},
		// array
		{"array OK empty", `{"a":[]}`, false},
		{"array OK elements", `{"a":[1,2]}`, false},
		{"array rejects object", `{"a":{}}`, true},
		// null
		{"null OK literal", `{"u":null}`, false},
		{"null rejects 0", `{"u":0}`, true},
		{"null rejects string", `{"u":"null"}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArgs(json.RawMessage(schema), json.RawMessage(tt.args))
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateArgsAnyOfType(t *testing.T) {
	schema := `{"type":"object","properties":{"val":{"type":["string","null"]}}}`

	tests := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"string OK", `{"val":"hello"}`, false},
		{"null OK", `{"val":null}`, false},
		{"number reject", `{"val":123}`, true},
		{"bool reject", `{"val":true}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArgs(json.RawMessage(schema), json.RawMessage(tt.args))
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateArgsRequired(t *testing.T) {
	schema := `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`

	tests := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"missing required", `{}`, true},
		{"present", `{"title":"x"}`, false},
		{"present but null (treated as missing)", `{"title":null}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArgs(json.RawMessage(schema), json.RawMessage(tt.args))
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr && !errIsRecursive(err, ErrInvalidArgs) {
				t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
			}
		})
	}
}

func TestValidateArgsUnknownProperties(t *testing.T) {
	schema := `{"type":"object","properties":{"a":{"type":"string"}}}`

	tests := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"known prop OK", `{"a":"x"}`, false},
		{"known + unknown → reject", `{"a":"x","b":1}`, true},
		{"unknown only → reject", `{"b":1}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArgs(json.RawMessage(schema), json.RawMessage(tt.args))
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateArgsAdditionalPropertiesTrue(t *testing.T) {
	schema := `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":true}`

	tests := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"known prop OK", `{"a":"x"}`, false},
		{"unknown prop accepted when additionalProperties:true", `{"b":1}`, false},
		{"known prop still validated", `{"a":1}`, true},
		{"mixed — unknown passes, known validated", `{"a":"x","c":{"nested":true}}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArgs(json.RawMessage(schema), json.RawMessage(tt.args))
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateArgsAdditionalPropertiesFalseExplicit(t *testing.T) {
	schema := `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`

	err := validateArgs(json.RawMessage(schema), json.RawMessage(`{"b":1}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errIsRecursive(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

func TestValidateArgsPropertySchemaEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		args    string
		wantErr bool
	}{
		{
			name:    "property schema without type → fail closed",
			schema:  `{"type":"object","properties":{"a":{}}}`,
			args:    `{"a":"x"}`,
			wantErr: true,
		},
		{
			name:    "property schema unparseable type → fail closed",
			schema:  `{"type":"object","properties":{"a":{"type":}}}`,
			args:    `{}`,
			wantErr: true,
		},
		{
			name:    "property schema unsupported type name → fail closed",
			schema:  `{"type":"object","properties":{"a":{"type":"bogus"}}}`,
			args:    `{}`,
			wantErr: true,
		},
		{
			name:    "property schema empty any-of list → fail closed",
			schema:  `{"type":"object","properties":{"a":{"type":[]}}}`,
			args:    `{}`,
			wantErr: true,
		},
		{
			name:    "property schema any-of with bad type → fail closed",
			schema:  `{"type":"object","properties":{"a":{"type":["string","bogus"]}}}`,
			args:    `{}`,
			wantErr: true,
		},
		{
			name:    "property schema valid with any value",
			schema:  `{"type":"object","properties":{"a":{"type":"object"}}}`,
			args:    `{"a":{}}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArgs(json.RawMessage(tt.schema), json.RawMessage(tt.args))
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr && !errIsRecursive(err, ErrInvalidArgs) {
				t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
			}
		})
	}
}

func TestValidateArgsEmptyPropertiesSchemaRejectsUnknown(t *testing.T) {
	// This verifies the specific case from the task: list_tasks (empty
	// properties) with {"x":1} → ErrInvalidArgs.
	schema := `{"type":"object","properties":{}}`
	err := validateArgs(json.RawMessage(schema), json.RawMessage(`{"x":1}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errIsRecursive(err, ErrInvalidArgs) {
		t.Errorf("errors.Is(err, ErrInvalidArgs) = false, err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helper: recursive errors.Is check
// ---------------------------------------------------------------------------

// errIsRecursive is a wrapper that uses errors.Is, kept as a named helper for
// test readability.
func errIsRecursive(err, target error) bool {
	return errors.Is(err, target)
}

// contains is a simple substring check.
func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
