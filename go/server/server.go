// Package server implements the server half of MCP.
//
// The server owns three registries (tools, resources, prompts) and one state
// machine (initialized or not). Everything else in this file is dispatch:
// turn a JSON-RPC request into a JSON-RPC response.
//
// The dispatch table is deliberately explicit — a switch on method name —
// rather than reflection or a handler map keyed by string. For a protocol with
// eleven methods, a switch is easier to read and impossible to misconfigure.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/softwaredc/simple-mcp/go/mcptypes"
	"github.com/softwaredc/simple-mcp/go/protocol"
	"github.com/softwaredc/simple-mcp/go/transport"
)

// SupportedVersions lists the MCP protocol revisions this server speaks, in
// preference order. The first entry is what we answer with when the client
// asks for something we do not know.
//
// Version negotiation is not a formality: the 2025-06-18 revision moved several
// fields and removed others. A client from 2024 and a server from 2025 can
// disagree about what a tool result looks like, so the handshake exists to
// make that disagreement explicit instead of silent.
var SupportedVersions = protocol.MCPVersions

// ToolHandler builds a tool result from decoded arguments.
//
// Returning an error produces a *tool-level* failure: the response is a normal
// result with isError set, not a JSON-RPC error. That distinction is the single
// most important thing for a client to get right, and it lives here.
type ToolHandler func(args json.RawMessage) ([]mcptypes.Content, error)

// Server is an MCP server. It is safe for concurrent use: registries are
// guarded by a RWMutex and the dispatch loop runs one message at a time.
type Server struct {
	info         mcptypes.Implementation
	instructions string

	mu          sync.RWMutex
	tools       map[string]*toolEntry
	resources   map[string]*resourceEntry
	prompts     map[string]*promptEntry
	initialized bool

	requests  atomic.Int64
	responses atomic.Int64
}

type toolEntry struct {
	def     mcptypes.Tool
	handler ToolHandler
}

type resourceEntry struct {
	def    mcptypes.Resource
	reader func() (string, error)
}

type promptEntry struct {
	def    mcptypes.Prompt
	render func(args map[string]string) ([]mcptypes.PromptMessage, error)
}

// NewServer builds a server with the given name and version. These are
// reported in initialize as serverInfo, and clients log them, so make them
// meaningful.
func NewServer(name, version string) *Server {
	return &Server{
		info:      mcptypes.Implementation{Name: name, Version: version},
		tools:     make(map[string]*toolEntry),
		resources: make(map[string]*resourceEntry),
		prompts:   make(map[string]*promptEntry),
	}
}

// SetInstructions sets the free-text guidance returned in initialize. This is
// where a server tells the model how to use its tools well; Claude surfaces it
// in the system prompt.
func (s *Server) SetInstructions(text string) { s.instructions = text }

// ------------------------------------------------------------------ registry

// AddTool registers a tool.
//
// The description and schema are what the model sees. A tool with a vague
// description gets called with wrong arguments, and no amount of handler code
// fixes that — see docs/PITFALLS.md, "the description is the interface".
func (s *Server) AddTool(name, description string, schema json.RawMessage, handler ToolHandler) {
	if name == "" || handler == nil {
		return
	}
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[name] = &toolEntry{
		def:     mcptypes.Tool{Name: name, Description: description, InputSchema: schema},
		handler: handler,
	}
}

// AddResource registers a readable resource at a fixed URI.
func (s *Server) AddResource(uri, name, description, mimeType string, reader func() (string, error)) {
	if uri == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[uri] = &resourceEntry{
		def:    mcptypes.Resource{URI: uri, Name: name, Description: description, MimeType: mimeType},
		reader: reader,
	}
}

// AddPrompt registers a prompt template.
func (s *Server) AddPrompt(name, description string, args []mcptypes.PromptArgument,
	render func(map[string]string) ([]mcptypes.PromptMessage, error)) {
	if name == "" || render == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts[name] = &promptEntry{
		def:    mcptypes.Prompt{Name: name, Description: description, Arguments: args},
		render: render,
	}
}

// ------------------------------------------------------------------ dispatch

// HandleMessage processes one raw JSON-RPC message and returns the raw response
// bytes, or nil when the message is a notification and no reply is due.
//
// Every path that produces an error response funnels through here, so the
// jsonrpc version field and the id echo are set in exactly one place.
func (s *Server) HandleMessage(raw []byte) ([]byte, error) {
	var req protocol.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		// A parse error cannot carry an id: we never got far enough to read
		// one, so JSON-RPC says the id is null.
		return json.Marshal(protocol.NewErrorResponse(json.RawMessage("null"),
			protocol.NewError(protocol.ErrParse, "parse error: "+err.Error(), nil)))
	}

	if rerr := req.Validate(); rerr != nil {
		return json.Marshal(protocol.NewErrorResponse(req.ID, rerr))
	}

	s.requests.Add(1)

	out, err := s.dispatch(&req)
	if err != nil {
		// Errors from a notification are dropped like every other reply would
		// be. A notification has no id, so any response we sent would carry
		// "id":null and land in the client's reader as a reply nobody asked
		// for. An unknown notification method is not something the peer can
		// act on, so staying silent is both correct and harmless.
		// Errors from a notification are dropped like every other reply would
		// be. A notification has no id, so anything we sent back would either
		// carry "id":null or omit the field entirely, and the client's reader
		// would see a response nobody asked for. There is nothing the peer can
		// do about a notification we failed to act on, so the correct reply is
		// no reply.
		if req.IsNotification() {
			return nil, nil
		}
		rpc, ok := err.(*protocol.RPCError)
		if !ok {
			// A handler returned a plain error where a protocol error was
			// expected. Reporting it as -32603 keeps the client's error
			// handling simple instead of leaking Go internals.
			rpc = protocol.NewError(protocol.ErrInternal, err.Error(), nil)
		}
		return json.Marshal(protocol.NewErrorResponse(req.ID, rpc))
	}

	// A notification never gets a response. A request whose method legitimately
	// has nothing to say (only notifications/initialized) also returns nil,
	// and answering it would desynchronise the client.
	if req.IsNotification() || out == nil {
		return nil, nil
	}
	return json.Marshal(out)
}

// dispatch routes one request to its handler. Notifications and requests share
// this table; only the reply differs.
func (s *Server) dispatch(req *protocol.Request) (*protocol.Response, error) {
	// The handshake is the only thing allowed before it completes. Checking
	// here rather than in each handler means a new method cannot accidentally
	// forget the guard.
	if req.Method != "initialize" {
		if rerr := s.requireInit(); rerr != nil {
			return nil, rerr
		}
	}

	switch req.Method {

	case "initialize":
		return s.handleInitialize(req)

	case "notifications/initialized":
		// Client confirms the handshake. Nothing to say back.
		return nil, nil

	case "ping":
		// Exists so a client can check liveness without side effects.
		return result(req.ID, map[string]any{})

	case "tools/list":
		return result(req.ID, s.listTools())

	case "tools/call":
		return s.handleToolCall(req)

	case "resources/list":
		return result(req.ID, s.listResources())

	case "resources/templates/list":
		return result(req.ID, mcptypes.ListResourceTemplatesResult{ResourceTemplates: []mcptypes.ResourceTemplate{}})

	case "resources/read":
		return s.handleResourceRead(req)

	case "prompts/list":
		return result(req.ID, s.listPrompts())

	case "prompts/get":
		return s.handlePromptGet(req)

	default:
		return nil, protocol.NewError(protocol.ErrMethodNotFound,
			"unknown method: "+req.Method, nil)
	}
}

func result(id json.RawMessage, v any) (*protocol.Response, error) {
	return protocol.NewResponse(id, v)
}

// handleInitialize runs the handshake.
//
// Two rules worth noticing:
//
//  1. The server answers with a version *it* supports, which may differ from
//     the one the client asked for. We prefer the client's version when we
//     support it, and fall back to our newest otherwise.
//
//  2. After initialize the session is locked in: every other method is refused
//     with -32000-ish until notifications/initialized arrives. Without that
//     check, a client that skips the handshake gets confusing behaviour later,
//     when a tools/call fails for a reason unrelated to the call.
func (s *Server) handleInitialize(req *protocol.Request) (*protocol.Response, error) {
	var params mcptypes.InitializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.ErrInvalidParams, "bad initialize params: "+err.Error(), nil)
		}
	}
	if params.ClientInfo.Name == "" {
		return nil, protocol.NewError(protocol.ErrInvalidParams, "clientInfo.name is required", nil)
	}

	s.mu.Lock()
	s.initialized = true
	s.mu.Unlock()

	return result(req.ID, mcptypes.InitializeResult{
		ProtocolVersion: negotiate(params.ProtocolVersion),
		Capabilities: mcptypes.ServerCapabilities{
			Tools:     &mcptypes.ToolsCapability{},
			Resources: &mcptypes.ResourcesCapability{},
			Prompts:   &mcptypes.PromptsCapability{},
		},
		ServerInfo:   s.info,
		Instructions: s.instructions,
	})
}

// negotiate picks the protocol version to answer with.
func negotiate(clientWants string) string {
	if clientWants == "" {
		return SupportedVersions[0]
	}
	for _, v := range SupportedVersions {
		if v == clientWants {
			return v
		}
	}
	// Client asked for something we do not speak. Answer with our newest and
	// let the client decide whether to continue. A stricter server would
	// return an error here; being lenient is friendlier for a teaching build.
	return SupportedVersions[0]
}

// requireInit guards every method except the handshake itself.
func (s *Server) requireInit() *protocol.RPCError {
	s.mu.RLock()
	ok := s.initialized
	s.mu.RUnlock()
	if !ok {
		return protocol.NewError(protocol.ErrInvalidRequest,
			"session not initialized: call initialize first", nil)
	}
	return nil
}

func (s *Server) listTools() mcptypes.ListToolsResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := mcptypes.ListToolsResult{Tools: make([]mcptypes.Tool, 0, len(s.tools))}
	for _, t := range s.tools {
		out.Tools = append(out.Tools, t.def)
	}
	return out
}

func (s *Server) listResources() mcptypes.ListResourcesResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := mcptypes.ListResourcesResult{Resources: make([]mcptypes.Resource, 0, len(s.resources))}
	for _, r := range s.resources {
		out.Resources = append(out.Resources, r.def)
	}
	return out
}

func (s *Server) listPrompts() mcptypes.ListPromptsResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := mcptypes.ListPromptsResult{Prompts: make([]mcptypes.Prompt, 0, len(s.prompts))}
	for _, p := range s.prompts {
		out.Prompts = append(out.Prompts, p.def)
	}
	return out
}

// -------------------------------------------------------------------- serving

// Serve runs the request loop until the transport ends.
func (s *Server) Serve(t transport.Transport) error {
	for {
		raw, err := t.ReadMessage()
		if err != nil {
			// io.EOF is a clean shutdown: the peer closed its write half.
			// Anything else is a real failure worth reporting.
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		out, herr := s.HandleMessage(raw)
		if herr != nil {
			return fmt.Errorf("handle: %w", herr)
		}
		if len(out) > 0 {
			if err := t.WriteMessage(out); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			s.responses.Add(1)
		}
	}
}

// HTTPHandler exposes the server over Streamable HTTP.
func (s *Server) HTTPHandler() http.Handler {
	return transport.HTTPHandler(func(msg []byte) ([]byte, error) {
		return s.HandleMessage(msg)
	})
}

// Stats reports counters useful in tests and in the demo's /stats output.
type Stats struct {
	Requests  int64
	Responses int64
	Tools     int
	Resources int
	Prompts   int
}

// Stats returns a snapshot of the server counters.
func (s *Server) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Stats{
		Requests:  s.requests.Load(),
		Responses: s.responses.Load(),
		Tools:     len(s.tools),
		Resources: len(s.resources),
		Prompts:   len(s.prompts),
	}
}
