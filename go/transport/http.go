package transport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// HTTPTransport is a client-side Transport over MCP's Streamable HTTP.
//
// How it works: every WriteMessage performs one POST and the response body is
// queued for the next ReadMessage. If the server answers with
// text/event-stream, each SSE `data:` payload becomes a separate queued
// message, so server-to-client notifications arrive through the same
// ReadMessage as responses.
//
// This is the simplified half of Streamable HTTP. The full protocol lets the
// server hold a POST open and push an unbounded stream of messages, and lets
// the client GET a standalone SSE stream for notifications. We implement
// request/response and one-shot streams, which is what a minimal server does.
// docs/PROTOCOL.md records exactly what is missing and why.
// The inbox and wake channels are deliberately unsymmetrised: wake is never
// closed, and shutdown travels on done instead. Closing wake would be the
// obvious move, and it is wrong — Close cannot know whether a goroutine is
// between "unlocked the mutex" and "sent the wake-up", which would panic with
// a send on a closed channel. A channel that is only ever sent to can never
// panic; a separate done channel carries the shutdown signal.
type HTTPTransport struct {
	endpoint string
	client   *http.Client

	mu     sync.Mutex
	inbox  [][]byte
	closed bool
	wake   chan struct{}
	done   chan struct{}
}

// NewHTTP builds a client transport talking to endpoint.
func NewHTTP(endpoint string, client *http.Client) *HTTPTransport {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPTransport{
		endpoint: endpoint,
		client:   client,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

// WriteMessage POSTs one message and queues whatever comes back.
func (t *HTTPTransport) WriteMessage(msg []byte) error {
	resp, err := t.client.Post(t.endpoint, "application/json", bytes.NewReader(msg))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("transport: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		return t.queueSSE(resp.Body)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxLineBytes))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		// 202 Accepted for a notification: nothing to read, by design.
		return nil
	}
	t.push(body)
	return nil
}

// queueSSE parses an SSE body and queues every data payload.
func (t *HTTPTransport) queueSSE(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxLineBytes)
	var data []string
	flush := func() {
		if len(data) == 0 {
			return
		}
		joined := strings.Join(data, "\n")
		data = data[:0]
		if strings.TrimSpace(joined) != "" {
			t.push([]byte(joined))
		}
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	return sc.Err()
}

func (t *HTTPTransport) push(msg []byte) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.inbox = append(t.inbox, msg)
	t.mu.Unlock()
	select {
	case t.wake <- struct{}{}:
	case <-t.done:
	default: // already woken and not yet drained: idempotent signal
	}
}

// ReadMessage returns the next queued response, blocking until one arrives or
// Close is called.
func (t *HTTPTransport) ReadMessage() ([]byte, error) {
	for {
		t.mu.Lock()
		if len(t.inbox) > 0 {
			msg := t.inbox[0]
			t.inbox = t.inbox[1:]
			t.mu.Unlock()
			return msg, nil
		}
		closed := t.closed
		t.mu.Unlock()

		if closed {
			return nil, io.EOF
		}
		// Wait on both: wake for new mail, done for shutdown. Shutdown cannot
		// be signalled by closing wake, for the reason given on the struct.
		select {
		case <-t.wake:
		case <-t.done:
		}
	}
}

// Close marks the transport done and unblocks any ReadMessage.
func (t *HTTPTransport) Close() error {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.done)
	}
	t.mu.Unlock()
	return nil
}

// HandlerFunc adapts a message handler into an http.Handler implementing the
// server half of Streamable HTTP.
type HandlerFunc func(msg []byte) ([]byte, error)

// HTTPHandler serves MCP over HTTP.
//
// POST /   one JSON-RPC message in the body, one in the response.
// GET  /   an SSE stream for server-to-client notifications. This minimal
//          implementation opens the stream and immediately closes it, because
//          our demo server never pushes notifications unprompted. A production
//          server would keep it open and write events as they happen.
func HTTPHandler(h HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			body, err := io.ReadAll(io.LimitReader(r.Body, MaxLineBytes))
			if err != nil {
				http.Error(w, "cannot read body", http.StatusBadRequest)
				return
			}
			out, herr := h(body)
			if herr != nil {
				http.Error(w, herr.Error(), http.StatusInternalServerError)
				return
			}
			if len(out) == 0 {
				// Notification: acknowledged, no body.
				w.WriteHeader(http.StatusAccepted)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(out)
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// SSEWriter writes server-sent events in the format the client above parses.
type SSEWriter struct {
	w http.ResponseWriter
}

// NewSSEWriter wraps a ResponseWriter that already carries the
// text/event-stream content type.
func NewSSEWriter(w http.ResponseWriter) *SSEWriter {
	return &SSEWriter{w: w}
}

// Send emits one event carrying payload.
func (s *SSEWriter) Send(payload []byte) error {
	if _, err := fmt.Fprintf(s.w, "event: message\ndata: %s\n\n", payload); err != nil {
		return err
	}
	// http.Flusher.Flush returns nothing; a flush failure surfaces on the next
	// write, so ignoring it here is correct rather than lazy.
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// MustJSON marshals v or panics. Used only in tests and demos.
func MustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
