// Package mcptypes holds the wire types MCP defines.
//
// These structs are the contract between client and server, so their JSON tags
// are not cosmetic: a field named `inputSchema` in the spec must be tagged
// exactly that, or a model reading tools/list will not see the schema and will
// guess at the arguments.
//
// Content is the one type that needs custom marshalling. It is a tagged union
// discriminated by `type`, and Go has no union type, so we keep the tag
// alongside the payload and let MarshalJSON emit whichever fields belong to
// that tag. Getting this wrong produces `{"type":"text","data":"..."}`, which
// clients reject.
package mcptypes

import "encoding/json"

// Implementation names a client or server.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ------------------------------------------------------------------ schemas

// InputSchema is the subset of JSON Schema MCP requires for tool arguments.
//
// MCP says inputSchema must be a JSON Schema object. In practice a tool only
// ever needs type, properties, required, and occasionally enum or items, so
// this struct covers those and the validator in server/schema.go understands
// exactly these fields. Anything fancier can be passed through as a raw
// object; see NewRawSchema.
type InputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

// Property describes one argument.
type Property struct {
	Type        string    `json:"type"`
	Description string    `json:"description,omitempty"`
	Enum        []any     `json:"enum,omitempty"`
	Items       *Property `json:"items,omitempty"`
}

// NewRawSchema wraps an arbitrary JSON Schema object for pass-through when a
// tool needs keywords this struct does not model.
func NewRawSchema(raw json.RawMessage) json.RawMessage { return raw }

// ------------------------------------------------------------------ content

// ContentKind enumerates the content block types MCP defines.
type ContentKind string

const (
	KindText      ContentKind = "text"
	KindImage     ContentKind = "image"
	KindAudio     ContentKind = "audio"
	KindResource  ContentKind = "resource"
)

// Content is one block in a tool result or a prompt message.
//
// The zero value is not useful; construct with Text, Image, or ResourceContent.
type Content struct {
	Kind ContentKind `json:"type"`

	// Text is set when Kind == KindText.
	Text string `json:"text,omitempty"`

	// Data and MimeType are set when Kind == KindImage or KindAudio.
	// Data is base64, which is what the wire format requires.
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`

	// Resource is set when Kind == KindResource.
	Resource *ResourceContents `json:"resource,omitempty"`
}

// Text builds a text content block.
func Text(s string) Content { return Content{Kind: KindText, Text: s} }

// Image builds a base64 image content block.
func Image(data, mimeType string) Content {
	return Content{Kind: KindImage, Data: data, MimeType: mimeType}
}

// ResourceContent embeds a resource read result in a tool result.
func ResourceContent(rc ResourceContents) Content {
	return Content{Kind: KindResource, Resource: &rc}
}

// UnmarshalJSON reads a content block, choosing the shape by its `type` tag.
func (c *Content) UnmarshalJSON(b []byte) error {
	var probe struct {
		Type ContentKind `json:"type"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	c.Kind = probe.Type
	switch probe.Type {
	case KindText:
		var v struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		c.Text = v.Text
	case KindImage, KindAudio:
		var v struct {
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		c.Data, c.MimeType = v.Data, v.MimeType
	case KindResource:
		var v struct {
			Resource ResourceContents `json:"resource"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		c.Resource = &v.Resource
	default:
		// Unknown kind: keep the tag so a caller can decide what to do rather
		// than silently dropping content.
		return nil
	}
	return nil
}

// ------------------------------------------------------------------- tools

// Tool is one entry in tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ListToolsResult is the result of tools/list.
type ListToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// CallToolParams are the params of tools/call.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// CallToolResult is the result of tools/call.
//
// IsError is the important field: it reports a *tool-level* failure, which is
// not the same as a protocol error. A tool that cannot do its job returns
// IsError with an explanatory text block; a malformed call returns a JSON-RPC
// error. Conflating the two makes clients retry forever on permanent failures.
type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// --------------------------------------------------------------- resources

// Resource is one entry in resources/list.
type Resource struct {
	URI        string `json:"uri"`
	Name       string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

// ResourceContents is one entry in the result of resources/read.
//
// Exactly one of Text and Blob is set. Text is UTF-8; Blob is base64.
type ResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// ListResourcesResult is the result of resources/list.
type ListResourcesResult struct {
	Resources  []Resource `json:"resources"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// ResourceTemplate is one entry in resources/templates/list: a URI pattern the
// client can fill in, like file:///logs/{date}.log.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ListResourceTemplatesResult is the result of resources/templates/list.
type ListResourceTemplatesResult struct {
	ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
	NextCursor        string             `json:"nextCursor,omitempty"`
}

// ReadResourceParams are the params of resources/read.
type ReadResourceParams struct {
	URI string `json:"uri"`
}

// ReadResourceResult is the result of resources/read.
type ReadResourceResult struct {
	Contents []ResourceContents `json:"contents"`
}

// ----------------------------------------------------------------- prompts

// PromptArgument describes one argument of a prompt template.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Prompt is one entry in prompts/list.
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// ListPromptsResult is the result of prompts/list.
type ListPromptsResult struct {
	Prompts    []Prompt `json:"prompts"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

// GetPromptParams are the params of prompts/get.
type GetPromptParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// PromptMessage is one message in a rendered prompt.
type PromptMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// GetPromptResult is the result of prompts/get.
type GetPromptResult struct {
	Description string         `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// ------------------------------------------------------------- handshake

// ClientCapabilities declares what the client can do.
type ClientCapabilities struct {
	Roots    *RootsCapability    `json:"roots,omitempty"`
	Sampling *SamplingCapability `json:"sampling,omitempty"`
}

// RootsCapability: the client can tell the server about filesystem roots.
type RootsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// SamplingCapability: the client can run a model completion for the server.
type SamplingCapability struct{}

// ServerCapabilities declares what the server can do. The three optional
// pointers matter: an absent capability means "not supported", while an
// empty object means "supported, no list-change notifications".
type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
	Logging   *LoggingCapability   `json:"logging,omitempty"`
}

// ToolsCapability: this server exposes tools.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCapability: this server exposes resources.
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability: this server exposes prompt templates.
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// LoggingCapability: this server can emit log messages to the client.
type LoggingCapability struct{}

// InitializeParams are the params of the initialize request.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      Implementation     `json:"clientInfo"`
}

// InitializeResult is the result of initialize.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}
