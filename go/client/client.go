// Package client implements the client half of MCP.
//
// The interesting part is not the methods, it is the plumbing underneath them:
// a reader goroutine that demultiplexes responses and notifications, and a
// table of pending requests keyed by id.
//
// Why not just write a request and read the answer? Because the server is
// allowed to send things you did not ask for — notifications/initialized
// confirmations, progress updates, log messages, list-changed events. A
// client that does write-then-read works until the day a notification arrives
// first, at which point it reads a notification while waiting for a response
// and hangs. The reader goroutine makes that impossible by construction:
// every incoming message is routed by whether it carries an id.
package client

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/softwaredc/simple-mcp/go/mcptypes"
	"github.com/softwaredc/simple-mcp/go/protocol"
	"github.com/softwaredc/simple-mcp/go/transport"
)

// Client is an MCP client. One client per connection.
//
// Concurrent calls are safe, and it is worth being precise about what that
// means. Responses are demultiplexed by id in the reader goroutine, so two
// requests can be in flight at once and each one lands in the right channel.
// The write half is serialised: writeMu guarantees that one message is fully
// written before the next begins, because two goroutines writing into the
// same stream produce interleaved bytes that no amount of careful parsing can
// recover. Requests overlap; their bytes do not.
type Client struct {
	t   transport.Transport
	src mcptypes.Implementation

	mu      sync.Mutex
	writeMu sync.Mutex // serialises writes to t; see the note above
	nextID  int64
	pending     map[string]chan protocol.Response
	initialized bool

	serverInfo      mcptypes.Implementation
	serverCaps      mcptypes.ServerCapabilities
	instructions    string
	protocolVersion string

	done      chan struct{}
	closeOnce sync.Once
	readErr   error
}

// NewClient builds a client that will identify itself as name/version.
func NewClient(t transport.Transport, name, version string) *Client {
	return &Client{
		t:       t,
		src:     mcptypes.Implementation{Name: name, Version: version},
		pending: make(map[string]chan protocol.Response),
		done:    make(chan struct{}),
	}
}

// Connect performs the initialize handshake.
//
// The order is not negotiable: send initialize, wait for its response, then
// send notifications/initialized.
//
// The reader goroutine starts *before* the first request goes out. This is not
// a micro-optimisation: with a synchronous pipe, the server's response blocks
// until somebody reads it, so a client that starts reading only after sending
// deadlocks against its own server. Every real transport can hit this.
func (c *Client) Connect() error {
	go c.readLoop()

	var res mcptypes.InitializeResult
	if err := c.call("initialize", mcptypes.InitializeParams{
		ProtocolVersion: protocol.NewestMCPVersion(),
		Capabilities:    mcptypes.ClientCapabilities{},
		ClientInfo:      c.src,
	}, &res); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	c.mu.Lock()
	c.initialized = true
	c.serverInfo = res.ServerInfo
	c.serverCaps = res.Capabilities
	c.instructions = res.Instructions
	c.protocolVersion = res.ProtocolVersion
	c.mu.Unlock()

	// Confirm the handshake. No response is expected, which is exactly why
	// this goes through the notification path rather than call().
	n, err := protocol.NewNotification("notifications/initialized", nil)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if err := c.writeRaw(raw); err != nil {
		return fmt.Errorf("notifications/initialized: %w", err)
	}
	return nil
}

// writeRaw is the only place this client writes to the transport. Holding one
// mutex across the write (and never across the wait for the response) is what
// keeps concurrent callers from interleaving bytes while still letting their
// requests be in flight simultaneously.
func (c *Client) writeRaw(raw []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.t.WriteMessage(raw)
}

// ServerInfo returns what the server reported about itself.
func (c *Client) ServerInfo() mcptypes.Implementation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverInfo
}

// ProtocolVersion returns the negotiated protocol version.
func (c *Client) ProtocolVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.protocolVersion
}

// Instructions returns the server's usage guidance, if any.
func (c *Client) Instructions() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instructions
}

// --------------------------------------------------------------------- calls

// call performs one request/response exchange and decodes the result into out.
func (c *Client) call(method string, params any, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan protocol.Response, 1)
	key := fmt.Sprintf("%d", id)
	c.pending[key] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
	}()

	req, err := protocol.NewRequest(json.RawMessage(key), method, params)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err := c.writeRaw(raw); err != nil {
		return fmt.Errorf("write %s: %w", method, err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("connection closed while waiting for %s", method)
		}
		if resp.Error != nil {
			return resp.Error
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("decode %s result: %w", method, err)
			}
		}
		return nil
	case <-c.done:
		return fmt.Errorf("connection closed while waiting for %s", method)
	}
}

// Ping checks that the server is alive. Useful as a readiness probe.
func (c *Client) Ping() error {
	return c.call("ping", nil, nil)
}

// ListTools returns every tool the server exposes.
func (c *Client) ListTools() ([]mcptypes.Tool, error) {
	var res mcptypes.ListToolsResult
	if err := c.call("tools/list", nil, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

// CallTool invokes a tool. args is marshalled as the arguments object.
//
// A tool-level failure comes back as a result with IsError set, not as an
// error from this method. Check the flag: swallowing it turns "the tool could
// not find the city" into a silent empty answer.
func (c *Client) CallTool(name string, args any) (*mcptypes.CallToolResult, error) {
	var res mcptypes.CallToolResult
	if err := c.call("tools/call", mcptypes.CallToolParams{
		Name:      name,
		Arguments: mustRaw(args),
	}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ListResources returns every resource the server exposes.
func (c *Client) ListResources() ([]mcptypes.Resource, error) {
	var res mcptypes.ListResourcesResult
	if err := c.call("resources/list", nil, &res); err != nil {
		return nil, err
	}
	return res.Resources, nil
}

// ReadResource fetches one resource's contents.
func (c *Client) ReadResource(uri string) ([]mcptypes.ResourceContents, error) {
	var res mcptypes.ReadResourceResult
	if err := c.call("resources/read", mcptypes.ReadResourceParams{URI: uri}, &res); err != nil {
		return nil, err
	}
	return res.Contents, nil
}

// ListPrompts returns every prompt template the server exposes.
func (c *Client) ListPrompts() ([]mcptypes.Prompt, error) {
	var res mcptypes.ListPromptsResult
	if err := c.call("prompts/list", nil, &res); err != nil {
		return nil, err
	}
	return res.Prompts, nil
}

// GetPrompt renders a prompt template with the given arguments.
func (c *Client) GetPrompt(name string, args map[string]string) ([]mcptypes.PromptMessage, error) {
	var res mcptypes.GetPromptResult
	if err := c.call("prompts/get", mcptypes.GetPromptParams{Name: name, Arguments: args}, &res); err != nil {
		return nil, err
	}
	return res.Messages, nil
}

func mustRaw(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage(`{}`)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// ------------------------------------------------------------------ readLoop

// readLoop is the reason this client does not deadlock.
//
// Every message the peer sends arrives here. A message with an id is a
// response: hand it to whoever is waiting. A message without an id is a
// notification: nobody asked for it, so it is dropped (a real client would
// dispatch progress and log events somewhere).
func (c *Client) readLoop() {
	for {
		raw, err := c.t.ReadMessage()
		if err != nil {
			c.finish(err)
			return
		}
		var resp protocol.Response
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue // unparseable: no id to match, nothing to do
		}
		if len(resp.ID) == 0 {
			continue // notification
		}

		c.mu.Lock()
		ch, ok := c.pending[string(resp.ID)]
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
		// No waiter: a late or duplicated response. Dropping is correct;
		// queueing it would leak the channel.
	}
}

func (c *Client) finish(err error) {
	c.mu.Lock()
	c.readErr = err
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = make(map[string]chan protocol.Response)
	c.mu.Unlock()
	c.closeOnce.Do(func() { close(c.done) })
}

// Close shuts the transport down and unblocks every waiting caller.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return c.t.Close()
}

// Err reports why the read loop stopped, if it did.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readErr
}
