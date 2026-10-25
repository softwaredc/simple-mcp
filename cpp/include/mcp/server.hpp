// The MCP server, C++ side. Same shape as the Go server: three registries,
// one handshake state machine, one explicit dispatch switch.
#ifndef MCP_SERVER_HPP
#define MCP_SERVER_HPP

#include <functional>
#include <map>
#include <mutex>
#include <stdexcept>
#include <string>
#include <vector>

#include "mcp/jsonrpc.hpp"
#include "mcp/transport.hpp"
#include "mcp/types.hpp"

namespace mcp {

// ToolError is what a tool throws to report a *tool-level* failure. The server
// turns it into a result with isError set, never into a JSON-RPC error.
struct ToolError : std::runtime_error {
    explicit ToolError(const std::string& msg) : std::runtime_error(msg) {}
};

// ToolHandler builds a result from decoded arguments.
using ToolHandler = std::function<std::vector<Content>(const json::Value& args)>;

// Schema validation errors are reported through this, so the message can name
// the offending field instead of saying "invalid params".
struct SchemaError : std::runtime_error {
    explicit SchemaError(const std::string& msg) : std::runtime_error(msg) {}
};

// validate_schema checks args against a JSON Schema document. Only the subset
// MCP actually uses is understood: type, properties, required, items, enum.
// Returns true when valid; on false, err holds a description with the path.
bool validate_schema(const json::Value& schema, const json::Value& args, std::string& err);

class Server {
public:
    Server(std::string name, std::string version);
    ~Server();

    void set_instructions(std::string text);

    // Registration. A handler that is null is ignored, which keeps a
    // misconfigured registration from crashing the dispatch loop later.
    void add_tool(const std::string& name, const std::string& description,
                  const std::string& schema_json, ToolHandler handler);
    void add_resource(const std::string& uri, const std::string& name,
                      const std::string& description, const std::string& mime_type,
                      const std::function<std::string()>& reader);
    void add_prompt(const std::string& name, const std::string& description,
                    const std::vector<PromptArgument>& arguments,
                    const std::function<std::vector<PromptMessage>(
                        const std::map<std::string, std::string>&)>& render);

    // handle_message processes one raw message and returns the raw response,
    // or std::nullopt when the message was a notification.
    std::optional<std::string> handle_message(const std::string& raw);

    // serve runs the request loop until the transport ends.
    void serve(Transport& t);

    const Implementation& info() const { return info_; }

private:
    struct ToolEntry {
        Tool def;
        ToolHandler handler;
    };
    struct ResourceEntry {
        Resource def;
        std::function<std::string()> reader;
    };
    struct PromptEntry {
        Prompt def;
        std::function<std::vector<PromptMessage>(const std::map<std::string, std::string>&)> render;
    };

    std::optional<std::string> dispatch(const Request& req);
    std::optional<std::string> handle_initialize(const Request& req);
    std::optional<std::string> handle_tools_call(const Request& req);
    std::optional<std::string> handle_resources_read(const Request& req);
    std::optional<std::string> handle_prompts_get(const Request& req);

    static std::string negotiate(const std::string& client_wants);
    static bool require_init(bool initialized);

    Implementation info_;
    std::string instructions_;

    std::mutex mu_;
    std::map<std::string, ToolEntry> tools_;
    std::map<std::string, ResourceEntry> resources_;
    std::map<std::string, PromptEntry> prompts_;
    bool initialized_ = false;
};

} // namespace mcp

#endif // MCP_SERVER_HPP
