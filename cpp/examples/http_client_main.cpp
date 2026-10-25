// Demo: the C++ client talking to the Go server over Streamable HTTP.
//
// Why this exists, beyond being another demo. stdio proves the messages are
// well-formed; this proves the two implementations are the same protocol. A
// Go server and a C++ client share nothing but the bytes on the wire, so if
// either side has a private assumption -- about framing, about id handling,
// about what a notification is worth -- this is where it shows up.
//
// It also exercises the one piece of the C++ tree nothing else touches: there
// is no HTTP server here, so the client transport has no in-tree partner and
// unit tests cannot reach it. Point it at `mcpserver -http`.
//
// Usage: mcp_http_client [host] [port]
#include "mcp/client.hpp"
#include "mcp/transport.hpp"

#include <iostream>
#include <string>

namespace {

std::string describe(const mcp::RpcError& e) {
    return "rpc error " + std::to_string(e.code) + ": " + e.message;
}

} // namespace

int main(int argc, char** argv) {
    std::string host = argc > 1 ? argv[1] : "127.0.0.1";
    std::string port = argc > 2 ? argv[2] : "8080";

    mcp::HttpTransport transport(host, port, "/mcp");
    mcp::Client client(transport, "cpp-http-client", "1.0.0");

    try {
        client.connect();
    } catch (const mcp::RpcError& e) {
        std::cerr << "connect failed: " << describe(e) << "\n";
        return 1;
    }

    std::cout << "connected to " << client.server_info().name << " "
              << client.server_info().version << " (protocol "
              << client.protocol_version() << ")\n";

    for (const auto& tool : client.list_tools()) {
        std::cout << "  tool: " << tool.name << " — " << tool.description << "\n";
    }

    for (const auto& res : client.list_resources()) {
        std::cout << "  resource: " << res.uri << "\n";
    }

    // add exists in both demo servers, so this checks argument encoding in
    // both directions rather than echoing a fixed string back.
    try {
        mcp::json::Value args = mcp::json::Value::object();
        args.set("a", mcp::json::Value::number("17"));
        args.set("b", mcp::json::Value::number("25"));
        mcp::CallToolResult r = client.call_tool("add", args);
        for (const auto& c : r.content) {
            std::cout << "add(17, 25) -> " << c.text << (r.is_error ? " [isError]" : "") << "\n";
        }
    } catch (const mcp::RpcError& e) {
        std::cout << "add failed: " << describe(e) << "\n";
    }

    // The interesting failure: a protocol error rather than a tool error.
    // Getting this wrong in either direction is invisible until a real agent
    // retries the call three times.
    try {
        mcp::json::Value bad = mcp::json::Value::object();
        bad.set("city", mcp::json::Value::number("42"));
        mcp::CallToolResult r = client.call_tool("get_weather", bad);
        std::cout << "get_weather(42) unexpectedly succeeded: " << r.is_error << "\n";
    } catch (const mcp::RpcError& e) {
        std::cout << "get_weather with a number -> protocol error: " << describe(e) << "\n";
    }

    if (!client.list_resources().empty()) {
        const std::string uri = client.list_resources().front().uri;
        try {
            for (const auto& c : client.read_resource(uri)) {
                std::cout << "read " << uri << " -> " << c.text << "\n";
            }
        } catch (const mcp::RpcError& e) {
            std::cout << "read_resource failed: " << describe(e) << "\n";
        }
    }

    transport.close();
    std::cout << "done\n";
    return 0;
}
