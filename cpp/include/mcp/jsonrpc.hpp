// JSON-RPC 2.0 for MCP, C++ side.
//
// Same design as the Go package, for the same reasons:
//
//   - the id is kept as raw text, so 1 does not become 1.0
//   - a request with no id is a notification and gets no response
//   - exactly one of result and error is set in a response
#ifndef MCP_JSONRPC_HPP
#define MCP_JSONRPC_HPP

#include <optional>
#include <stdexcept>
#include <string>

#include "mcp/json.hpp"

namespace mcp {

// Error codes. The first five come from JSON-RPC itself; the rest are MCP's.
namespace err {
constexpr int kParse          = -32700;
constexpr int kInvalidRequest = -32600;
constexpr int kMethodNotFound = -32601;
constexpr int kInvalidParams  = -32602;
constexpr int kInternal       = -32603;
constexpr int kResourceNotFound = -32002; // MCP-specific
} // namespace err

// RpcError is thrown by handlers that need to fail at the protocol level.
// Anything else a tool throws becomes an isError result instead.
// RpcError is thrown by the client when the server answers with an error.
//
// It derives from std::runtime_error rather than being a bare struct, because
// a bare one silently breaks every handler written the ordinary way: catch
// (const std::exception&) does not see it, so the exception escapes to
// terminate. Deriving costs nothing and means e.what() works too.
struct RpcError : std::runtime_error {
    // A default constructor exists so the type can live in a std::map (the
    // client's pending-error table). The zero code is never sent on the wire;
    // build_error always fills it in.
    RpcError() : std::runtime_error("rpc error") {}
    RpcError(int c, std::string msg, std::string d = "")
        : std::runtime_error("rpc error " + std::to_string(c) + ": " + msg),
          code(c), message(std::move(msg)), data(std::move(d)) {}

    int code = 0;
    std::string message;
    std::string data; // optional, raw JSON
};

// Request is one incoming JSON-RPC message.
struct Request {
    std::string jsonrpc = "2.0";
    bool has_id = false;
    std::string id_raw;   // the id exactly as it appeared on the wire
    std::string method;
    std::string params_raw; // raw JSON text of params, empty when absent

    bool is_notification() const { return !has_id; }
};

// Response is one outgoing JSON-RPC message.
struct Response {
    bool has_id = false;
    std::string id_raw;
    bool is_error = false;
    std::string result_raw; // raw JSON text, valid when !is_error
    int error_code = 0;
    std::string error_message;
    std::string error_data;

    // build_result and build_error are the only two ways to make a response,
    // which is what keeps "exactly one of result and error" true.
    static Response build_result(std::string id, std::string result_json) {
        Response r;
        r.has_id = true;
        r.id_raw = std::move(id);
        r.result_raw = std::move(result_json);
        return r;
    }
    static Response build_error(std::string id, const RpcError& e) {
        Response r;
        r.has_id = true;
        r.id_raw = std::move(id);
        r.is_error = true;
        r.error_code = e.code;
        r.error_message = e.message;
        r.error_data = e.data;
        return r;
    }
};

// parse_request decodes one message. Throws json::ParseError on malformed
// input; the caller turns that into a -32700 response with a null id.
Request parse_request(const std::string& text);

// dump serialises a response.
std::string dump_response(const Response& r);

// build_notification serialises a notification (no id, no response expected).
std::string build_notification(const std::string& method, const std::string& params_json);

// build_request serialises a request with the given raw id text.
std::string build_request(const std::string& id_raw, const std::string& method,
                          const std::string& params_json);

} // namespace mcp

#endif // MCP_JSONRPC_HPP
