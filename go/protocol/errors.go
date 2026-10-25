package protocol

// Error codes. The first five are defined by JSON-RPC 2.0 itself; the rest are
// MCP-specific and live in the range the spec reserves for application errors.
//
// The distinction matters in practice: a JSON-RPC error means "the call itself
// is malformed or unknown", while an MCP error means "the request was fine but
// the thing you asked about does not exist".
const (
	// JSON-RPC 2.0 standard codes.
	ErrParse          = -32700 // invalid JSON was received
	ErrInvalidRequest = -32600 // not a valid Request object
	ErrMethodNotFound = -32601 // method does not exist
	ErrInvalidParams  = -32602 // invalid method parameter(s)
	ErrInternal       = -32603 // internal error

	// MCP-defined codes.
	ErrResourceNotFound = -32002 // resources/read on an unknown URI
)

// Messages for the standard codes, so every layer reports the same text.
var stdErrorText = map[int]string{
	ErrParse:            "parse error",
	ErrInvalidRequest:   "invalid request",
	ErrMethodNotFound:   "method not found",
	ErrInvalidParams:    "invalid params",
	ErrInternal:         "internal error",
	ErrResourceNotFound: "resource not found",
}

// Text returns the canonical message for a standard code, or a generic string
// for anything else.
func Text(code int) string {
	if t, ok := stdErrorText[code]; ok {
		return t
	}
	return "unknown error"
}
