package server_test

// Integration tests: a real client and a real server talking over a real
// transport. Nothing here is mocked.
//
// The transport is an in-memory pipe rather than a subprocess because the
// point is to exercise the protocol layers, not the OS. What makes it a real
// test anyway: every byte that crosses is a JSON-RPC message, the handshake
// runs, and the error taxonomy is checked from the client's side.

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/softwaredc/simple-mcp/go/client"
	"github.com/softwaredc/simple-mcp/go/mcptypes"
	"github.com/softwaredc/simple-mcp/go/protocol"
	"github.com/softwaredc/simple-mcp/go/server"
	"github.com/softwaredc/simple-mcp/go/transport"
)

// newDemoServer builds a server with enough surface to exercise every method.
func newDemoServer() *server.Server {
	s := server.NewServer("integration-demo", "1.0.0")
	s.SetInstructions("Test server.")

	s.AddTool("echo", "Repeat the text back",
		json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		func(args json.RawMessage) ([]mcptypes.Content, error) {
			var p struct{ Text string }
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			return []mcptypes.Content{mcptypes.Text(p.Text)}, nil
		})

	s.AddTool("boom", "Always fails, to exercise the isError path",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(json.RawMessage) ([]mcptypes.Content, error) {
			return nil, io.ErrUnexpectedEOF // any error becomes isError
		})

	s.AddResource("file:///demo/note", "Note", "a note", "text/plain",
		func() (string, error) { return "integration note body", nil })

	s.AddPrompt("greet", "Greet someone",
		[]mcptypes.PromptArgument{{Name: "who", Required: true}},
		func(args map[string]string) ([]mcptypes.PromptMessage, error) {
			return []mcptypes.PromptMessage{{
				Role:    "user",
				Content: mcptypes.Text("hello " + args["who"]),
			}}, nil
		})
	return s
}

// pipePair wires a server transport and a client transport to each other.
func pipePair(s *server.Server) (*client.Client, func()) {
	// client -> server
	c2sR, c2sW := io.Pipe()
	// server -> client
	s2cR, s2cW := io.Pipe()

	serverT := transport.NewStdio(c2sR, s2cW, nil)
	clientT := transport.NewStdio(s2cR, c2sW, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		serverT.Close()
	}()

	go func() { _ = s.Serve(serverT) }()

	c := client.NewClient(clientT, "integration-client", "1.0.0")
	return c, func() {
		c.Close()
		c2sW.Close()
		s2cW.Close()
		<-done
	}
}

func TestHandshake(t *testing.T) {
	s := newDemoServer()
	c, cleanup := pipePair(s)
	defer cleanup()

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := c.ServerInfo().Name; got != "integration-demo" {
		t.Errorf("serverInfo.name = %q, want integration-demo", got)
	}
	if v := c.ProtocolVersion(); v == "" {
		t.Error("negotiated protocol version is empty")
	}
	if got := c.Instructions(); got != "Test server." {
		t.Errorf("instructions = %q", got)
	}
}

func TestToolCallSuccess(t *testing.T) {
	s := newDemoServer()
	c, cleanup := pipePair(s)
	defer cleanup()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}

	res, err := c.CallTool("echo", map[string]any{"text": "round trip"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected isError: %v", res.Content)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "round trip" {
		t.Fatalf("unexpected content: %+v", res.Content)
	}
}

func TestToolLevelFailureIsNotProtocolError(t *testing.T) {
	// The distinction this whole project is built around: a tool that fails
	// returns a result with isError, not a JSON-RPC error.
	s := newDemoServer()
	c, cleanup := pipePair(s)
	defer cleanup()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}

	res, err := c.CallTool("boom", nil)
	if err != nil {
		t.Fatalf("a tool failure must not surface as a protocol error, got %v", err)
	}
	if !res.IsError {
		t.Fatal("expected isError to be set")
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "boom") {
		t.Fatalf("error content should name the tool: %+v", res.Content)
	}
}

func TestSchemaViolationIsProtocolError(t *testing.T) {
	// The mirror image: a malformed call IS a protocol error, and must not be
	// reported as isError. A client retrying on isError would retry forever.
	s := newDemoServer()
	c, cleanup := pipePair(s)
	defer cleanup()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}

	_, err := c.CallTool("echo", map[string]any{"text": 42})
	if err == nil {
		t.Fatal("wrong argument type must be a protocol error")
	}
	if !strings.Contains(err.Error(), "invalid arguments") {
		t.Errorf("error should mention validation, got %v", err)
	}
}

func TestUnknownTool(t *testing.T) {
	s := newDemoServer()
	c, cleanup := pipePair(s)
	defer cleanup()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CallTool("nope", nil); err == nil {
		t.Fatal("unknown tool must error")
	}
}

func TestResourcesAndPrompts(t *testing.T) {
	s := newDemoServer()
	c, cleanup := pipePair(s)
	defer cleanup()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}

	res, err := c.ListResources()
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].URI != "file:///demo/note" {
		t.Fatalf("unexpected resources: %+v", res)
	}
	contents, err := c.ReadResource("file:///demo/note")
	if err != nil {
		t.Fatal(err)
	}
	if contents[0].Text != "integration note body" {
		t.Errorf("resource text = %q", contents[0].Text)
	}

	// An unknown URI is -32002, not -32602. Asserting on the code rather than
	// the message keeps this test honest when the wording changes.
	_, err = c.ReadResource("file:///nope")
	if err == nil {
		t.Fatal("unknown resource must error")
	}
	var rpc *protocol.RPCError
	if !errors.As(err, &rpc) {
		t.Fatalf("expected an *RPCError, got %T: %v", err, err)
	}
	if rpc.Code != protocol.ErrResourceNotFound {
		t.Errorf("unknown URI should be -32002, got %d (%s)", rpc.Code, rpc.Message)
	}

	msgs, err := c.GetPrompt("greet", map[string]string{"who": "world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content.Text != "hello world" {
		t.Fatalf("unexpected prompt: %+v", msgs)
	}

	// A required argument that is missing is a params error.
	if _, err := c.GetPrompt("greet", nil); err == nil {
		t.Fatal("missing required prompt argument must error")
	}
}

func TestServerRejectsPreHandshakeCalls(t *testing.T) {
	// A client that skips initialize gets a clear refusal rather than
	// confusing behaviour later.
	s := server.NewServer("strict", "1.0.0")
	s.AddTool("x", "x", json.RawMessage(`{"type":"object","properties":{}}`),
		func(json.RawMessage) ([]mcptypes.Content, error) { return nil, nil })

	out, err := s.HandleMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "not initialized") {
		t.Errorf("pre-handshake call must be refused, got %s", out)
	}
}

func TestParseErrorCarriesNullID(t *testing.T) {
	s := server.NewServer("strict", "1.0.0")
	out, err := s.HandleMessage([]byte(`{not json`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"id":null`) {
		t.Errorf("parse error must carry id null, got %s", out)
	}
}

func TestNotificationGetsNoResponse(t *testing.T) {
	// notifications/initialized is a notification. Answering it would make the
	// client read a response while waiting for the next real one.
	s := server.NewServer("strict", "1.0.0")
	s.HandleMessage([]byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"c","version":"1"}}}`))
	out, err := s.HandleMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("notification must not produce a response, got %s", out)
	}
}

// TestUnknownNotificationGetsNoResponse covers the case the test above does
// not, and covers it for a reason: the existing test only exercises the branch
// where nothing fails.
//
// notifications/initialized is handled by its own case in the dispatch table,
// which returns a nil result on purpose. An unrecognised notification falls
// through to default and returns -32601 instead, and the error path in
// HandleMessage used to marshal that error without ever asking whether the
// message was a notification. The reply carried "id":null, a client matching
// responses by id would hand it to whoever was waiting next, and the whole
// session desynchronised. Silence is the only correct answer to any
// notification, including one we do not understand.
func TestUnknownNotificationGetsNoResponse(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"unknown notification method", `{"jsonrpc":"2.0","method":"notifications/cancelled"}`},
		{"notification we do know", `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		{"unknown request-like method", `{"jsonrpc":"2.0","method":"tools/teleport"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Deliberately never initialised: this is also the previous test's
			// blind spot, because requireInit fails first and its error used to
			// take the same unguarded path.
			s := server.NewServer("strict", "1.0.0")
			out, err := s.HandleMessage([]byte(tc.msg))
			if err != nil {
				t.Fatal(err)
			}
			if out != nil {
				t.Errorf("a notification must never be answered, got %s", out)
			}
		})
	}
}

// TestUnknownRequestStillAnswers checks we did not fix the above by refusing
// to answer everything. Requests keep their error responses.
func TestUnknownRequestStillAnswers(t *testing.T) {
	s := server.NewServer("strict", "1.0.0")
	s.HandleMessage([]byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"c","version":"1"}}}`))
	out, err := s.HandleMessage([]byte(`{"jsonrpc":"2.0","id":9,"method":"tools/nope"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Fatal("an unknown request must still get an error response")
	}
	if !strings.Contains(string(out), `-32601`) {
		t.Errorf("expected -32601, got %s", out)
	}
	if !strings.Contains(string(out), `"id":9`) {
		t.Errorf("the response must echo the request id, got %s", out)
	}
}

func TestPing(t *testing.T) {
	s := newDemoServer()
	c, cleanup := pipePair(s)
	defer cleanup()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(); err != nil {
		t.Errorf("Ping: %v", err)
	}
}
