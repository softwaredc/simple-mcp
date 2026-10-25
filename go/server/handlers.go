package server

import (
	"encoding/json"
	"fmt"

	"github.com/softwaredc/simple-mcp/go/mcptypes"
	"github.com/softwaredc/simple-mcp/go/protocol"
)

// This file holds the three method handlers that carry MCP's actual payload:
// tools/call, resources/read and prompts/get. Each one has a failure mode that
// is specific to it, and each is called out in the comments.

// handleToolCall runs a registered tool.
//
// The error taxonomy is the whole point of this function:
//
//	bad params (no name, unknown tool, schema violation)  -> JSON-RPC error
//	handler returned an error                             -> isError result
//
// Why it matters. A client that receives a JSON-RPC error treats the call as
// not having happened and may retry. A client that receives isError knows the
// tool ran and failed, and will not retry. If a tool that says "city not
// found" reports a protocol error, a well-behaved agent retries it three times,
// gives up, and tells the user the tool is broken — when in fact the tool
// worked exactly as intended.
func (s *Server) handleToolCall(req *protocol.Request) (*protocol.Response, error) {
	var params mcptypes.CallToolParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.ErrInvalidParams, "bad tools/call params: "+err.Error(), nil)
		}
	}
	if params.Name == "" {
		return nil, protocol.NewError(protocol.ErrInvalidParams, "tools/call requires a name", nil)
	}

	s.mu.RLock()
	entry, ok := s.tools[params.Name]
	s.mu.RUnlock()
	if !ok {
		return nil, protocol.NewError(protocol.ErrInvalidParams, "unknown tool: "+params.Name, nil)
	}

	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if err := ValidateSchema(entry.def.InputSchema, args); err != nil {
		return nil, protocol.NewError(protocol.ErrInvalidParams, "invalid arguments: "+err.Error(), nil)
	}

	content, herr := entry.handler(args)
	if herr != nil {
		// A handler may opt out of the isError path by returning an RPCError
		// explicitly, which is how a tool reports a protocol-level problem.
		var rpc *protocol.RPCError
		if e, ok := herr.(*protocol.RPCError); ok {
			rpc = e
		}
		if rpc != nil {
			return nil, rpc
		}
		return result(req.ID, mcptypes.CallToolResult{
			Content: []mcptypes.Content{mcptypes.Text(fmt.Sprintf("tool %q failed: %v", params.Name, herr))},
			IsError: true,
		})
	}

	return result(req.ID, mcptypes.CallToolResult{Content: content})
}

// handleResourceRead returns the contents of one resource.
//
// resources/read is the one method with an MCP-specific error code: an unknown
// URI is -32002, not -32602. Getting that wrong means a client cannot tell
// "you asked for something that does not exist" from "your request was
// malformed", and the two need different recovery.
func (s *Server) handleResourceRead(req *protocol.Request) (*protocol.Response, error) {
	var params mcptypes.ReadResourceParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.ErrInvalidParams, "bad resources/read params: "+err.Error(), nil)
		}
	}
	if params.URI == "" {
		return nil, protocol.NewError(protocol.ErrInvalidParams, "resources/read requires a uri", nil)
	}

	s.mu.RLock()
	entry, ok := s.resources[params.URI]
	s.mu.RUnlock()
	if !ok {
		return nil, protocol.NewError(protocol.ErrResourceNotFound, "no resource at "+params.URI, nil)
	}

	text, err := entry.reader()
	if err != nil {
		return nil, protocol.NewError(protocol.ErrInternal, "cannot read resource: "+err.Error(), nil)
	}

	return result(req.ID, mcptypes.ReadResourceResult{
		Contents: []mcptypes.ResourceContents{{
			URI:      params.URI,
			MimeType: entry.def.MimeType,
			Text:     text,
		}},
	})
}

// handlePromptGet renders a prompt template.
//
// Required arguments are checked here rather than by the render function, so
// every prompt reports missing arguments the same way. The rendered messages
// carry a role of "user" or "assistant"; a role the client does not recognise
// makes the whole prompt unusable.
func (s *Server) handlePromptGet(req *protocol.Request) (*protocol.Response, error) {
	var params mcptypes.GetPromptParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.ErrInvalidParams, "bad prompts/get params: "+err.Error(), nil)
		}
	}
	if params.Name == "" {
		return nil, protocol.NewError(protocol.ErrInvalidParams, "prompts/get requires a name", nil)
	}

	s.mu.RLock()
	entry, ok := s.prompts[params.Name]
	s.mu.RUnlock()
	if !ok {
		return nil, protocol.NewError(protocol.ErrInvalidParams, "unknown prompt: "+params.Name, nil)
	}

	for _, a := range entry.def.Arguments {
		if a.Required {
			if v, present := params.Arguments[a.Name]; !present || v == "" {
				return nil, protocol.NewError(protocol.ErrInvalidParams,
					fmt.Sprintf("prompt %q requires argument %q", params.Name, a.Name), nil)
			}
		}
	}

	msgs, err := entry.render(params.Arguments)
	if err != nil {
		return nil, protocol.NewError(protocol.ErrInternal, "cannot render prompt: "+err.Error(), nil)
	}

	return result(req.ID, mcptypes.GetPromptResult{
		Description: entry.def.Description,
		Messages:    msgs,
	})
}
