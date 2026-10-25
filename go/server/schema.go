package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file implements the subset of JSON Schema that MCP tool arguments
// actually use.
//
// Why validate at all? Because a tool that declares `{"city": {"type":"string"}}`
// and receives `{"city": 42}` will either crash with a type assertion panic or,
// worse, silently coerce and return nonsense. MCP leaves validation to the
// server, so if you do not do it, nobody does.
//
// The subset covers: type (object, string, number, integer, boolean, array),
// properties, required, items, and enum. That is enough for real tools and
// small enough to read in one sitting.
//
// It does not cover $ref, allOf/anyOf/oneOf/not, or the numeric and string
// constraints (minimum, pattern, minItems, format). Anything using those is
// silently accepted here and must be checked by the handler; adding them costs
// more lines than this whole file and teaches nothing new about MCP.

// schemaNode is one node of a JSON Schema document. The same shape is used for
// the root object and for every property, which is what makes the validator
// recursive with no special cases.
type schemaNode struct {
	// Type is raw because JSON Schema allows either "string" or a union such
	// as ["string","null"]. Decoding it into string is what most people write,
	// and it fails on the union form — not by reporting a bad tool argument
	// but by failing to unmarshal the schema at all, which every call then
	// reports as invalid arguments.
	Type       json.RawMessage   `json:"type"`
	Properties json.RawMessage   `json:"properties"`
	Required   []string          `json:"required"`
	Items      json.RawMessage   `json:"items"`
	Enum       []json.RawMessage `json:"enum"`
}

// types expands this node's declared type into one or more names.
func (n schemaNode) types() []string {
	if len(n.Type) == 0 {
		return nil
	}
	var single string
	if err := json.Unmarshal(n.Type, &single); err == nil {
		return []string{single}
	}
	var union []string
	if err := json.Unmarshal(n.Type, &union); err == nil {
		return union
	}
	return nil
}

// typeMatches reports whether value satisfies any of the declared types.
//
// "integer" is a constraint on a JSON number rather than a type of its own:
// there is one number type on the wire, and "integer" says the fraction is
// empty. Being lenient about 3.0 here matters, because every JSON encoder
// that has ever seen a whole float writes it without the ".0" only sometimes.
func typeMatches(names []string, value any) bool {
	got := jsonTypeOf(value)
	for _, want := range names {
		if want == got {
			return true
		}
		if want == "integer" && got == "number" && isInteger(value) {
			return true
		}
	}
	return false
}

// SchemaError describes one validation failure, with the path to the offending
// value so the error message is actionable instead of "invalid params".
type SchemaError struct {
	Path string
	Want string
	Got  string
}

func (e *SchemaError) Error() string {
	where := e.Path
	if where == "" {
		where = "arguments"
	}
	return fmt.Sprintf("%s: expected %s, got %s", where, e.Want, e.Got)
}

// jsonTypeOf returns the JSON Schema type name for a decoded value.
func jsonTypeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// isInteger reports whether a decoded number has no fractional part.
// encoding/json decodes every number as float64 unless UseNumber is set, so
// 3.0 counts as an integer here. That is deliberate: JSON has one number type
// and "integer" is a constraint on its value, not a separate type.
func isInteger(v any) bool {
	f, ok := v.(float64)
	if !ok {
		return false
	}
	return f == float64(int64(f))
}

// ValidateSchema checks the arguments in raw against schema.
func ValidateSchema(schema json.RawMessage, raw json.RawMessage) error {
	if len(schema) == 0 {
		return nil // no schema declared: nothing to check
	}
	var node schemaNode
	if err := json.Unmarshal(schema, &node); err != nil {
		// An unparseable schema is the server author's bug, not the caller's.
		return fmt.Errorf("tool schema is not valid JSON: %w", err)
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return &SchemaError{Want: "valid JSON", Got: "unparseable"}
	}
	return validateNode(node, value, "")
}

// validateNode is the recursive core.
func validateNode(node schemaNode, value any, path string) error {
	// Skipping empty follows JSON Schema: a node that declares no type accepts
	// anything, which is how {} says "any value". Note this is checked against
	// the declared type(s) rather than compared to a single string.
	if names := node.types(); len(names) > 0 && !typeMatches(names, value) {
		return &SchemaError{Path: path, Want: strings.Join(names, " | "), Got: jsonTypeOf(value)}
	}

	if len(node.Enum) > 0 && !enumContains(node.Enum, value) {
		return &SchemaError{Path: path, Want: "one of " + enumList(node.Enum), Got: fmt.Sprintf("%v", value)}
	}

	// Array elements. Declaring items and not checking it is the kind of bug
	// that survives review: the "type":"array" check above passes, the
	// property loop below has nothing to do with a list, and the elements walk
	// straight through into the handler. Every real tool that takes a list —
	// a batch of files, a list of IDs — relies on this being enforced.
	if arr, isArr := value.([]any); isArr && len(node.Items) > 0 {
		var items schemaNode
		if err := json.Unmarshal(node.Items, &items); err == nil {
			for i, el := range arr {
				if err := validateNode(items, el, itemPath(path, i)); err != nil {
					return err
				}
			}
		}
	}

	obj, isObj := value.(map[string]any)
	if !isObj {
		return nil
	}

	for _, req := range node.Required {
		if _, ok := obj[req]; !ok {
			return &SchemaError{Path: join(path, req), Want: "present (required)", Got: "missing"}
		}
	}

	if len(node.Properties) == 0 {
		return nil
	}
	var props map[string]schemaNode
	if err := json.Unmarshal(node.Properties, &props); err != nil {
		return nil // malformed property map: skip, handler will see it
	}
	for name, prop := range props {
		v, ok := obj[name]
		if !ok {
			continue // absent optional property
		}
		if err := validateNode(prop, v, join(path, name)); err != nil {
			return err
		}
	}

	return nil
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// itemPath names one element of an array. The path stays empty for a top-level
// list, so the offending value reads "[3]" rather than ".[3]".
func itemPath(path string, i int) string {
	return fmt.Sprintf("%s[%d]", path, i)
}

func enumContains(enum []json.RawMessage, value any) bool {
	vb, err := json.Marshal(value)
	if err != nil {
		return false
	}
	for _, e := range enum {
		if jsonEqual(e, vb) {
			return true
		}
	}
	return false
}

// jsonEqual compares two JSON fragments semantically, so 1 and 1.0 in an enum
// both match the number 1.
func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}

func enumList(enum []json.RawMessage) string {
	parts := make([]string, 0, len(enum))
	for _, e := range enum {
		parts = append(parts, string(e))
	}
	return strings.Join(parts, ", ")
}
