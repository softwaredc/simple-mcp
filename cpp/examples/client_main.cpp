// Demo MCP client, C++ side. Spawns the server as a child process and drives
// it over stdio, which is how a real agent host does it.
//
//	mcp_client ./mcp_server
#include <sys/wait.h>
#include <unistd.h>

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <iostream>
#include <string>
#include <vector>

#include "mcp/client.hpp"
#include "mcp/server.hpp"
#include "mcp/transport.hpp"

using namespace mcp;

// PipeTransport wraps a pair of file descriptors: the child's stdout becomes
// our read end, our write end becomes the child's stdin.
class PipeTransport : public Transport {
public:
    PipeTransport(int read_fd, int write_fd) : rfd_(read_fd), wfd_(write_fd) {}

    std::optional<std::string> read_message() override {
        std::string line;
        char c;
        while (::read(rfd_, &c, 1) == 1) {
            if (c == '\n') {
                while (!line.empty() && (line.back() == '\r')) line.pop_back();
                if (line.empty()) continue;
                return line;
            }
            line.push_back(c);
        }
        return std::nullopt;
    }

    void write_message(const std::string& msg) override {
        std::string out = msg + "\n";
        ssize_t written = ::write(wfd_, out.data(), out.size());
        (void)written;
    }

    void close() override {
        if (rfd_ >= 0) ::close(rfd_);
        if (wfd_ >= 0) ::close(wfd_);
    }

private:
    int rfd_, wfd_;
};

int main(int argc, char** argv) {
    std::string server_path = argc > 1 ? argv[1] : "./mcp_server";

    // Two pipes, one per direction, created before the fork so both ends exist
    // in both processes.
    int to_child[2], from_child[2];
    if (::pipe(to_child) != 0 || ::pipe(from_child) != 0) {
        std::perror("pipe");
        return 1;
    }

    pid_t pid = ::fork();
    if (pid < 0) {
        std::perror("fork");
        return 1;
    }
    if (pid == 0) {
        // Child: stdin is the pipe from the parent, stdout is the pipe to it.
        ::dup2(to_child[0], STDIN_FILENO);
        ::dup2(from_child[1], STDOUT_FILENO);
        ::close(to_child[0]);
        ::close(to_child[1]);
        ::close(from_child[0]);
        ::close(from_child[1]);
        ::execl(server_path.c_str(), server_path.c_str(), nullptr);
        std::perror("execl");
        _exit(127);
    }

    // Parent: close the ends the child owns.
    ::close(to_child[0]);
    ::close(from_child[1]);

    PipeTransport t(from_child[0], to_child[1]);
    Client c(t, "simple-mcp-cpp-client", "1.0.0");

    try {
        c.connect();
    } catch (const std::exception& e) {
        std::fprintf(stderr, "connect failed: %s\n", e.what());
        ::kill(pid, SIGKILL);
        return 1;
    }

    std::printf("connected to %s %s (protocol %s)\n", c.server_info().name.c_str(),
                c.server_info().version.c_str(), c.protocol_version().c_str());
    if (!c.instructions().empty()) {
        std::printf("instructions: %s\n", c.instructions().c_str());
    }

    auto tools = c.list_tools();
    std::printf("\n%zu tools:\n", tools.size());
    for (const auto& t2 : tools) {
        std::printf("  %-14s %s\n", t2.name.c_str(), t2.description.c_str());
    }

    std::printf("\ncalling tools:\n");
    struct Call { const char* name; json::Value args; };
    std::vector<Call> calls;
    {
        json::Value a = json::Value::object();
        a.set("text", json::Value::string("hello from C++"));
        calls.push_back({"echo", a});
    }
    {
        json::Value a = json::Value::object();
        a.set("a", json::Value::number("40"));
        a.set("b", json::Value::number("2"));
        calls.push_back({"add", a});
    }
    {
        json::Value a = json::Value::object();
        a.set("city", json::Value::string("Beijing"));
        calls.push_back({"get_weather", a});
    }
    for (const auto& call : calls) {
        try {
            auto res = c.call_tool(call.name, call.args);
            std::printf("  %-14s %s%s\n", call.name,
                        res.content.empty() ? "" : res.content[0].text.c_str(),
                        res.is_error ? " [tool error]" : "");
        } catch (const RpcError& e) {
            std::printf("  %-14s protocol error %d: %s\n", call.name, e.code, e.message.c_str());
        }
    }

    // The error taxonomy, side by side.
    std::printf("\nerror taxonomy:\n");
    try {
        json::Value a = json::Value::object();
        a.set("city", json::Value::number("12345"));
        auto res = c.call_tool("get_weather", a);
        std::printf("  wrong argument type -> isError result: %s\n",
                    res.content.empty() ? "" : res.content[0].text.c_str());
    } catch (const RpcError& e) {
        std::printf("  wrong argument type -> protocol error %d: %s\n", e.code, e.message.c_str());
    }

    auto resources = c.list_resources();
    if (!resources.empty()) {
        auto contents = c.read_resource(resources[0].uri);
        std::printf("\nresource %s:\n", resources[0].uri.c_str());
        for (const auto& rc : contents) std::printf("  %s\n", rc.text.c_str());
    }

    ::kill(pid, SIGTERM);
    int status = 0;
    ::waitpid(pid, &status, 0);
    t.close();
    return 0;
}
