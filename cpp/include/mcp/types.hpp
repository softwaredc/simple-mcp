// MCP wire types, C++ side. Mirrors go/mcptypes.
//
// Everything here is a plain struct with a to_json/from_json pair, because the
// wire format is the contract and there is no point hiding it behind
// accessors. Where the Go version uses json.RawMessage to preserve bytes
// verbatim, this version keeps std::string of raw JSON for the same reason.
#ifndef MCP_TYPES_HPP
#define MCP_TYPES_HPP

#include <string>
#include <vector>

#include "mcp/json.hpp"

namespace mcp {

// Content is one block in a tool result. Discriminated by kind.
struct Content {
    enum class Kind { Text, Image, Audio, Resource } kind = Kind::Text;
    std::string text;                                // Kind::Text
    std::string data;                                // Kind::Image / Audio, base64
    std::string mime_type;                           // Kind::Image / Audio
    std::string resource_uri;                        // Kind::Resource
    std::string resource_text;                       // Kind::Resource

    static Content make_text(std::string t) {
        Content c;
        c.kind = Kind::Text;
        c.text = std::move(t);
        return c;
    }
    static Content make_image(std::string b64, std::string mime) {
        Content c;
        c.kind = Kind::Image;
        c.data = std::move(b64);
        c.mime_type = std::move(mime);
        return c;
    }

    json::Value to_json() const;
    static Content from_json(const json::Value& v);
};

// Tool is one entry in tools/list.
struct Tool {
    std::string name;
    std::string description;
    std::string input_schema_json; // raw JSON Schema

    json::Value to_json() const;
};

// CallToolResult is the result of tools/call. is_error reports a tool-level
// failure, which is not the same as a protocol error.
struct CallToolResult {
    std::vector<Content> content;
    bool is_error = false;

    json::Value to_json() const;
};

// Resource is one entry in resources/list.
struct Resource {
    std::string uri;
    std::string name;
    std::string description;
    std::string mime_type;
    long long size = 0;

    json::Value to_json() const;
};

// ResourceContents is one entry in the result of resources/read.
struct ResourceContents {
    std::string uri;
    std::string mime_type;
    std::string text;

    json::Value to_json() const;
};

// PromptArgument describes one argument of a prompt template.
struct PromptArgument {
    std::string name;
    std::string description;
    bool required = false;

    json::Value to_json() const;
};

// Prompt is one entry in prompts/list.
struct Prompt {
    std::string name;
    std::string description;
    std::vector<PromptArgument> arguments;

    json::Value to_json() const;
};

// PromptMessage is one message in a rendered prompt.
struct PromptMessage {
    std::string role;
    Content content;

    json::Value to_json() const;
};

// Implementation names a client or server.
struct Implementation {
    std::string name;
    std::string version;
};

} // namespace mcp

#endif // MCP_TYPES_HPP
