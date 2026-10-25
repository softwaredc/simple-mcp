#include "mcp/transport.hpp"

#include <iostream>
#include <string>

namespace mcp {

StdioTransport::StdioTransport() {
    // Tie nothing: we manage flushing explicitly, because an unflushed write
    // on a line-delimited protocol leaves the peer blocked forever.
    std::ios::sync_with_stdio(false);
}

std::optional<std::string> StdioTransport::read_message() {
    std::string line;
    // std::getline grows its buffer to fit, so a large tool result is not a
    // special case here. Blank lines are skipped rather than treated as parse
    // errors: the framing is a stream of lines.
    while (std::getline(std::cin, line)) {
        while (!line.empty() && (line.back() == '\r' || line.back() == '\n')) {
            line.pop_back();
        }
        size_t first = line.find_first_not_of(" \t");
        if (first == std::string::npos) continue;
        size_t last = line.find_last_not_of(" \t");
        return line.substr(first, last - first + 1);
    }
    return std::nullopt; // EOF or a read error; either way the session is over
}

void StdioTransport::write_message(const std::string& msg) {
    std::cout << msg << "\n";
    std::cout.flush(); // without this the peer never sees the message
}

void StdioTransport::log_to_stderr(const std::string& line) {
    std::cerr << line << "\n";
    std::cerr.flush();
}

} // namespace mcp
