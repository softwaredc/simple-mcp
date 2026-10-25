// The MCP client, C++ side.
//
// Same architecture as the Go client: a reader thread that demultiplexes
// responses from notifications, plus a table of pending requests keyed by id.
// The reason is identical — a client that writes a request and then reads
// blocks forever the first time a notification arrives ahead of the response.
#ifndef MCP_CLIENT_HPP
#define MCP_CLIENT_HPP

#include <condition_variable>
#include <map>
#include <mutex>
#include <optional>
#include <string>
#include <thread>

#include "mcp/jsonrpc.hpp"
#include "mcp/transport.hpp"
#include "mcp/types.hpp"

namespace mcp {

class Client {
public:
    Client(Transport& t, std::string name, std::string version);
    ~Client();

    // No copying: the reader thread holds a pointer back to this object.
    Client(const Client&) = delete;
    Client& operator=(const Client&) = delete;

    // connect performs the initialize handshake and starts the reader thread.
    void connect();

    const Implementation& server_info() const { return server_info_; }
    const std::string& protocol_version() const { return protocol_version_; }
    const std::string& instructions() const { return instructions_; }

    std::vector<Tool> list_tools();
    CallToolResult call_tool(const std::string& name, const json::Value& args);
    std::vector<Resource> list_resources();
    std::vector<ResourceContents> read_resource(const std::string& uri);
    std::vector<Prompt> list_prompts();
    std::vector<PromptMessage> get_prompt(const std::string& name,
                                          const std::map<std::string, std::string>& args);
    void ping();

private:
    // call performs one exchange and returns the raw result JSON. Throws
    // RpcError when the server answers with an error.
    std::string call(const std::string& method, const std::string& params_json);
    void read_loop();

    Transport& t_;
    Implementation src_;

    std::mutex mu_;
    std::condition_variable cv_;
    std::map<std::string, std::string> results_; // id -> raw result JSON
    std::map<std::string, RpcError> errors_;     // id -> error
    long long next_id_ = 0;
    bool stopped_ = false;

    Implementation server_info_;
    std::string protocol_version_;
    std::string instructions_;

    std::thread reader_;
};

} // namespace mcp

#endif // MCP_CLIENT_HPP
