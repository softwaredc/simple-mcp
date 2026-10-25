package transport

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Helpers for the transport tests. Kept in a separate file so the test file
// itself stays about behaviour rather than plumbing.

func newTestScanner(r *strings.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	return sc // deliberately default-sized, to demonstrate the limit
}

type testServer struct {
	url string
	srv *httptest.Server
}

func (t *testServer) close() { t.srv.Close() }

func (t *testServer) client() *http.Client { return t.srv.Client() }

// newTestHTTPServer answers POST with a JSON-RPC response, and notifications
// with 202 and no body.
func newTestHTTPServer(t *testing.T) *testServer {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		// Not r.ContentLength: it is -1 for a chunked body, and make() with a
		// negative length panics. It is also attacker-controlled metadata, so
		// reading what is actually there is both safer and simpler.
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if strings.Contains(string(body), "notifications/") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	})
	srv := httptest.NewServer(mux)
	return &testServer{url: srv.URL + "/mcp", srv: srv}
}

// newTestSSEServer answers POST with an SSE stream instead of a plain body.
func newTestSSEServer(t *testing.T) *testServer {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{}}\n\n"))
	})
	srv := httptest.NewServer(mux)
	return &testServer{url: srv.URL + "/mcp", srv: srv}
}
