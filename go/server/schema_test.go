package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func schema(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	return json.RawMessage(raw)
}

func TestSchemaValid(t *testing.T) {
	s := schema(t, `{"type":"object","properties":{
		"city":{"type":"string"},
		"days":{"type":"integer"},
		"unit":{"type":"string","enum":["c","f"]}
	},"required":["city"]}`)

	ok := []string{
		`{"city":"Beijing"}`,
		`{"city":"Beijing","days":3}`,
		`{"city":"Beijing","unit":"c"}`,
		`{"city":"Beijing","days":3.0}`, // whole-valued float is an integer
	}
	for _, in := range ok {
		if err := ValidateSchema(s, json.RawMessage(in)); err != nil {
			t.Errorf("expected %s to validate, got %v", in, err)
		}
	}
}

func TestSchemaRejections(t *testing.T) {
	s := schema(t, `{"type":"object","properties":{
		"city":{"type":"string"},
		"days":{"type":"integer"},
		"unit":{"type":"string","enum":["c","f"]}
	},"required":["city"]}`)

	cases := map[string]string{
		"missing required": `{}`,
		"wrong type":       `{"city":42}`,
		"bad enum":         `{"city":"X","unit":"kelvin"}`,
		"fractional int":   `{"city":"X","days":1.5}`,
	}
	for name, in := range cases {
		err := ValidateSchema(s, json.RawMessage(in))
		if err == nil {
			t.Errorf("%s: expected rejection for %s", name, in)
			continue
		}
		se, ok := err.(*SchemaError)
		if !ok {
			t.Errorf("%s: expected *SchemaError, got %T", name, err)
			continue
		}
		if se.Path == "" && name != "missing required" {
			t.Errorf("%s: error should name the offending field: %v", name, se)
		}
	}
}

func TestSchemaEmptyMeansAnything(t *testing.T) {
	// A tool that declares no schema imposes no constraints. Refusing to call
	// it would be worse than calling it with whatever arrived.
	if err := ValidateSchema(nil, json.RawMessage(`{"anything":true}`)); err != nil {
		t.Errorf("empty schema must accept anything, got %v", err)
	}
}

func TestNestedObjects(t *testing.T) {
	s := schema(t, `{"type":"object","properties":{
		"filter":{"type":"object","properties":{"tag":{"type":"string"}},"required":["tag"]}
	}}`)
	if err := ValidateSchema(s, json.RawMessage(`{"filter":{"tag":"x"}}`)); err != nil {
		t.Errorf("valid nested object rejected: %v", err)
	}
	err := ValidateSchema(s, json.RawMessage(`{"filter":{}}`))
	if err == nil {
		t.Error("nested missing required must be caught")
	}
}

// TestSchemaItems covers the branch that used to be declared but not
// implemented: the struct had an Items field and the header comment claimed
// support, and nothing in the validator ever read it. A tool that declares
// {"type":"array","items":{"type":"string"}} accepted [1,2,3] — the outer
// "array" check passes and nothing looked inside — and then reported a type
// error from wherever in the handler the bad element surfaced.
func TestSchemaItems(t *testing.T) {
	s := schema(t, `{"type":"object","properties":{
		"tags":{"type":"array","items":{"type":"string"}}
	}}`)

	valid := []string{
		`{"tags":["a","b"]}`,
		`{"tags":[]}`, // empty list satisfies items vacuously
	}
	for _, in := range valid {
		if err := ValidateSchema(s, json.RawMessage(in)); err != nil {
			t.Errorf("expected %s to validate, got %v", in, err)
		}
	}

	err := ValidateSchema(s, json.RawMessage(`{"tags":["a",2]}`))
	if err == nil {
		t.Fatal("an array element of the wrong type must be rejected")
	}
	se, ok := err.(*SchemaError)
	if !ok {
		t.Fatalf("expected *SchemaError, got %T", err)
	}
	// The point of the path is that the caller can find the bad element
	// without guessing which index it was.
	if !strings.Contains(se.Path, "[1]") {
		t.Errorf("error must name the offending index, got path %q", se.Path)
	}
}

// TestSchemaTypeUnion covers the other way a real-world schema trips a naive
// validator: "type" given as a union rather than a single string.
//
// ["string","null"] is how modern schema says "optional, and null is fine",
// which is what nullable JSON columns and Python Optional[T] both emit.
// Decoding type into a string field makes unmarshalling the schema fail, and
// that failure was reported as the caller having sent bad arguments — a bug in
// the tool definition surfacing as an error about the tool's input.
func TestSchemaTypeUnion(t *testing.T) {
	s := schema(t, `{"type":"object","properties":{
		"nickname":{"type":["string","null"]}
	},"required":["nickname"]}`)

	for _, in := range []string{`{"nickname":"x"}`, `{"nickname":null}`} {
		if err := ValidateSchema(s, json.RawMessage(in)); err != nil {
			t.Errorf("expected %s to validate, got %v", in, err)
		}
	}
	if err := ValidateSchema(s, json.RawMessage(`{"nickname":7}`)); err == nil {
		t.Error("a value outside the union must be rejected")
	}
}
