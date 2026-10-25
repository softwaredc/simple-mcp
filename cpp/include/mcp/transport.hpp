// MCP transports, C++ side.
//
// Two implementations, matching the Go side:
//
//   StdioTransport  reads lines from std::cin, writes to std::cout. This is
//                   what Claude Desktop and most CLI agents speak.
//   HttpTransport   POSTs one message and reads the response. Streamable HTTP
//                   in its single-response form.
//
// std::getline has no length cap, which is the same property the Go side gets
// from bufio.Reader.ReadBytes: a 300 KiB tool result crosses intact. A
// bufio.Scanner-based implementation would refuse it with "token too long".
#ifndef MCP_TRANSPORT_HPP
#define MCP_TRANSPORT_HPP

#include <optional>
#include <string>

namespace mcp {

class Transport {
public:
    virtual ~Transport() = default;

    // read_message returns the next complete message, or std::nullopt when the
    // peer closed the stream.
    virtual std::optional<std::string> read_message() = 0;

    // write_message sends one message. Callers serialise their own writes.
    virtual void write_message(const std::string& msg) = 0;

    virtual void close() {}
};

// StdioTransport speaks MCP over the process's standard streams.
class StdioTransport : public Transport {
public:
    StdioTransport();
    std::optional<std::string> read_message() override;
    void write_message(const std::string& msg) override;

    // The protocol owns stdout. log_to_stderr exists so a server can report
    // diagnostics without corrupting the stream; see server.cpp, which routes
    // every diagnostic through it.
    static void log_to_stderr(const std::string& line);
};

// HttpTransport is a client-side transport over Streamable HTTP.
class HttpTransport : public Transport {
public:
    // host and port identify the server; path is the MCP endpoint, usually
    // "/mcp".
    HttpTransport(std::string host, std::string port, std::string path);
    ~HttpTransport() override;

    std::optional<std::string> read_message() override;
    void write_message(const std::string& msg) override;
    void close() override;

private:
    struct Impl;
    Impl* impl_;
};

} // namespace mcp

#endif // MCP_TRANSPORT_HPP
