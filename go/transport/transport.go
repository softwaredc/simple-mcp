// Package transport carries MCP messages between a client and a server.
//
// The interface is two methods on purpose. Everything MCP does — requests,
// responses, notifications, streaming progress — reduces to reading and
// writing whole JSON-RPC messages. Which bytes those are, and how they are
// framed, is the only thing that differs between stdio and HTTP.
//
// MCP defines two transports:
//
//	stdio             the server's stdin and stdout, one JSON object per line.
//	                  This is what Claude Desktop and most CLI agents use.
//	Streamable HTTP   POST a message, get a response body back; the server may
//	                  also stream server-to-client notifications over SSE.
//
// Keeping the interface this small means the server and client layers above
// never learn which transport is underneath. The integration test exploits
// that: it runs a real client and server over an in-memory pipe.
package transport

// Transport is a bidirectional byte pipe carrying JSON-RPC messages.
type Transport interface {
	// ReadMessage blocks until one complete message arrives, returning the
	// raw JSON bytes without the trailing newline. io.EOF means the peer
	// closed the stream and the session is over.
	ReadMessage() ([]byte, error)

	// WriteMessage sends one message. Callers must serialise their own
	// writes; the client does so with a mutex.
	WriteMessage(msg []byte) error

	// Close releases the underlying reader/writer.
	Close() error
}
