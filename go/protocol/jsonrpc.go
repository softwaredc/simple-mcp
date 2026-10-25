// Package protocol implements JSON-RPC 2.0, the message envelope MCP runs on.
//
// Why dedicate a package to something this small? Because three details of
// JSON-RPC are easy to get wrong, and each one fails in a different way:
//
//  1. The `id` must round-trip byte for byte. Decoding it into interface{}
//     turns the number 1 into the float64 1, and re-encoding can emit "1" or
//     "1e+00" depending on magnitude. A client that matches responses to
//     requests by id then stalls forever waiting for a reply that already
//     arrived. Keeping id as json.RawMessage sidesteps the whole problem: we
//     store the bytes the peer sent and echo them back untouched.
//
//  2. A response carries either result or error, never both and never neither.
//
//  3. A request with no id is a notification, and notifications get no
//     response. This is why MCP's notifications/initialized is silent, and why
//     a server that answers it makes a client hang waiting for the next real
//     reply to show up.
package protocol

import (
	"encoding/json"
	"fmt"
)

// Version is the only JSON-RPC version string in existence. MCP requires it.
const Version = "2.0"

// MCPVersions lists the MCP protocol revisions, newest first.
//
// MCP is versioned by date, not by semver, and the dates matter: a client and
// server negotiate one of these in the initialize handshake. This library
// implements the newest one it knows and tolerates older clients.
var MCPVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// NewestMCPVersion returns the most recent MCP revision this library knows.
func NewestMCPVersion() string { return MCPVersions[0] }

// Request is one JSON-RPC request. When ID is empty it is a notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether this request expects a response.
func (r *Request) IsNotification() bool { return len(r.ID) == 0 }

// IDKey returns a comparable form of the id, for matching responses to
// requests. Two requests share a key only if their ids are byte-identical,
// which is exactly the matching rule JSON-RPC implies.
func (r *Request) IDKey() string { return string(r.ID) }

// Response is one JSON-RPC response. Exactly one of Result and Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// NewResponse builds a successful response for id carrying result.
func NewResponse(id json.RawMessage, result any) (*Response, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{JSONRPC: Version, ID: id, Result: raw}, nil
}

// NewErrorResponse builds a failure response for id.
func NewErrorResponse(id json.RawMessage, e *RPCError) *Response {
	return &Response{JSONRPC: Version, ID: id, Error: e}
}

// NewNotification builds a notification: a request that must not be answered.
func NewNotification(method string, params any) (*Request, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	return &Request{JSONRPC: Version, Method: method, Params: raw}, nil
}

// NewRequest builds a request with an explicit id.
func NewRequest(id json.RawMessage, method string, params any) (*Request, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	return &Request{JSONRPC: Version, ID: id, Method: method, Params: raw}, nil
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// Validate checks the parts of the spec a receiver can check without knowing
// the method. A malformed request gets error -32600.
func (r *Request) Validate() *RPCError {
	if r.JSONRPC != Version {
		return NewError(ErrInvalidRequest, fmt.Sprintf("jsonrpc must be %q, got %q", Version, r.JSONRPC), nil)
	}
	if r.Method == "" {
		return NewError(ErrInvalidRequest, "method is required", nil)
	}
	return nil
}

// RPCError is the error object carried in a failed response.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// NewError builds an RPCError. data may be nil.
func NewError(code int, message string, data any) *RPCError {
	e := &RPCError{Code: code, Message: message}
	if data != nil {
		if raw, err := json.Marshal(data); err == nil {
			e.Data = raw
		}
	}
	return e
}

// Error makes RPCError satisfy the error interface, so a handler can return it
// directly and the server can tell "protocol-level failure" from "tool-level
// failure" by type.
func (e *RPCError) Error() string {
	if e == nil {
		return "<nil rpc error>"
	}
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// IsProtocolError reports whether the error came from the JSON-RPC/MCP layer
// rather than from application code inside a tool handler.
func IsProtocolError(err error) bool {
	_, ok := err.(*RPCError)
	return ok
}
