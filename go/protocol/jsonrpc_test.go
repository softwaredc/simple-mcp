package protocol

import (
	"encoding/json"
	"testing"
)

// The id round-trip is the reason this package exists. These tests pin the
// behaviour: whatever bytes arrive in `id` must come back unchanged.

func TestIDRoundTrip(t *testing.T) {
	cases := []string{
		`1`,            // small integer
		`9007199254740993`, // beyond float64's exact range
		`"abc-123"`,    // string id
		`null`,         // explicit null
		`1.0`,          // decimal that must not become "1"
	}
	for _, id := range cases {
		resp, err := NewResponse(json.RawMessage(id), map[string]any{"ok": true})
		if err != nil {
			t.Fatalf("NewResponse(%s): %v", id, err)
		}
		out, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		// The id must appear verbatim in the output.
		if !containsID(out, id) {
			t.Errorf("id %s not preserved in %s", id, out)
		}
	}
}

func containsID(msg []byte, id string) bool {
	var probe struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(msg, &probe); err != nil {
		return false
	}
	return string(probe.ID) == id
}

func TestNotificationHasNoID(t *testing.T) {
	n, err := NewNotification("notifications/initialized", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !n.IsNotification() {
		t.Error("a notification must report IsNotification() == true")
	}
	out, _ := json.Marshal(n)
	if contains(out, `"id"`) {
		t.Errorf("notification must not carry an id: %s", out)
	}
}

func TestValidate(t *testing.T) {
	good := &Request{JSONRPC: Version, Method: "ping"}
	if err := good.Validate(); err != nil {
		t.Errorf("valid request rejected: %v", err)
	}
	bad := &Request{JSONRPC: "1.0", Method: "ping"}
	if err := bad.Validate(); err == nil || err.Code != ErrInvalidRequest {
		t.Errorf("wrong jsonrpc version must be -32600, got %v", err)
	}
	noMethod := &Request{JSONRPC: Version}
	if err := noMethod.Validate(); err == nil || err.Code != ErrInvalidRequest {
		t.Errorf("missing method must be -32600, got %v", err)
	}
}

func TestRPCErrorIsError(t *testing.T) {
	e := NewError(ErrMethodNotFound, "no such method", nil)
	if e.Error() == "" {
		t.Error("RPCError must implement error")
	}
	if !IsProtocolError(e) {
		t.Error("IsProtocolError must accept *RPCError")
	}
	if IsProtocolError(errPlain) {
		t.Error("IsProtocolError must reject plain errors")
	}
}

var errPlain = &plainErr{}

type plainErr struct{}

func (plainErr) Error() string { return "plain" }

func contains(b []byte, s string) bool {
	return len(b) > 0 && len(s) > 0 && indexOf(string(b), s) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
