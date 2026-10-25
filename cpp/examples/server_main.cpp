// Demo MCP server, C++ side.
//
//	mcp_server            # stdio: reads JSON-RPC from stdin, writes to stdout
//	mcp_server -http :8080
//
// Over stdio, stdout belongs to the protocol. Every diagnostic goes to stderr;
// one stray line on stdout breaks any client that parses the stream.
#include <cstdio>
#include <cstring>
#include <iostream>
#include <string>

#include "mcp/server.hpp"
#include "mcp/transport.hpp"

using namespace mcp;

static Server& build_server() {
    static Server s("simple-mcp-cpp-demo", "1.0.0");
    static bool built = false;
    if (built) return s;
    built = true;

    s.set_instructions("Demo server, C++ side. echo, add and get_weather.");

    s.add_tool("echo", "Repeat the given text back verbatim.",
               R"({"type":"object","properties":{"text":{"type":"string","description":"the text to repeat"}},"required":["text"]})",
               [](const json::Value& args) -> std::vector<Content> {
                   const json::Value* t = args.find("text");
                   if (t == nullptr || !t->is_string()) throw ToolError("text must be a string");
                   return {Content::make_text(t->as_string())};
               });

    s.add_tool("add", "Add two integers and return the sum.",
               R"({"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"]})",
               [](const json::Value& args) -> std::vector<Content> {
                   long long a = static_cast<long long>(args.find("a")->as_number());
                   long long b = static_cast<long long>(args.find("b")->as_number());
                   return {Content::make_text(std::to_string(a + b))};
               });

    // Mock data on purpose: a demo that calls a real API needs a key, breaks
    // offline, and turns rate limits into a teaching problem.
    s.add_tool("get_weather", "Return a mock forecast for a city. The data is fixed and synthetic.",
               R"({"type":"object","properties":{"city":{"type":"string"},"unit":{"type":"string","enum":["celsius","fahrenheit"]}},"required":["city"]})",
               [](const json::Value& args) -> std::vector<Content> {
                   std::string city = args.find("city")->as_string();
                   std::string unit = "celsius";
                   if (const json::Value* u = args.find("unit")) unit = u->as_string();
                   int c = 20;
                   if (city == "Beijing") c = 22;
                   else if (city == "Shanghai") c = 25;
                   if (unit == "fahrenheit") {
                       return {Content::make_text(city + ": " + std::to_string(c * 9 / 5 + 32) + " F (mock)")};
                   }
                   return {Content::make_text(city + ": " + std::to_string(c) + " C (mock)")};
               });

    s.add_resource("file:///demo/readme", "Demo readme",
                   "A short description of this demo server", "text/plain",
                   [] {
                       return std::string("simple-mcp C++ demo server.\nTools: echo, add, get_weather.\n");
                   });

    s.add_prompt("code_review", "Review a snippet for correctness and clarity",
                 {{"language", "programming language", true}, {"code", "the snippet", true}},
                 [](const std::map<std::string, std::string>& a) {
                     return std::vector<PromptMessage>{PromptMessage{
                         "user",
                         Content::make_text("Review this " + a.at("language") +
                                            " snippet for correctness and clarity:\n\n```" +
                                            a.at("language") + "\n" + a.at("code") + "\n```")}};
                 });
    return s;
}

int main(int argc, char** argv) {
    if (argc >= 3 && std::strcmp(argv[1], "-http") == 0) {
        // The C++ side intentionally has no HTTP server. Writing one by hand
        // would add a few hundred lines to teach a lesson the Go version
        // already teaches; see docs/PROTOCOL.md.
        std::fprintf(stderr,
                     "simple-mcp-cpp: -http is not implemented in the C++ build.\n"
                     "Use the Go server for Streamable HTTP: go run ./cmd/mcpserver -http :8080\n");
        return 2;
    }

    Server& s = build_server();
    StdioTransport t;
    StdioTransport::log_to_stderr("simple-mcp-cpp: serving on stdio");
    s.serve(t);
    return 0;
}
