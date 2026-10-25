// HTTP transport over POSIX sockets. No libcurl, no Boost.Asio.
//
// This is the client half only: connect, send a POST, read the response. The
// server half of Streamable HTTP is left to the Go implementation, which can
// use net/http. Writing an HTTP server by hand in C++ would triple the size of
// this file to teach a lesson that does not need repeating twice.
//
// What this does cover, and why it is worth the 200 lines: it shows the same
// protocol running over a completely different byte pipe, which is the whole
// point of the Transport abstraction.
#include "mcp/transport.hpp"

#include <arpa/inet.h>
#include <netdb.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>
#include <unistd.h>

#include <condition_variable>
#include <mutex>

#include <cctype>
#include <cstdlib>
#include <cstring>
#include <string>

namespace mcp {
namespace {

// content_length returns the declared body size, or -1 when the response does
// not declare one (chunked, or a body terminated by connection close).
long content_length(const std::string& headers) {
    std::string lower;
    lower.reserve(headers.size());
    for (char c : headers) lower.push_back(static_cast<char>(std::tolower(c)));
    size_t pos = lower.find("content-length:");
    if (pos == std::string::npos) return -1;
    pos += std::strlen("content-length:");
    while (pos < headers.size() && (headers[pos] == ' ' || headers[pos] == '\t')) ++pos;
    size_t end = headers.find("\r\n", pos);
    if (end == std::string::npos) return -1;
    long len = std::strtol(headers.substr(pos, end - pos).c_str(), nullptr, 10);
    return len < 0 ? -1 : len;
}

// read_response pulls exactly one complete HTTP response into out, consuming
// it from inbox and leaving any bytes that follow for the next call.
//
// The distinction that matters here is between "nothing more is coming" and
// "not all of it has arrived yet". A previous version conflated them: it read
// headers, took whatever body bytes were in the buffer, and treated a short
// read as end of stream. Every response bigger than one recv -- which is to
// say every response bigger than 4 KiB -- came back truncated, and the JSON
// parser saw half a document. False returns mean EOF or a socket error, and
// nothing else.
bool read_response(int fd, std::string& inbox, std::string& out) {
    for (;;) {
        size_t head_end = inbox.find("\r\n\r\n");
        if (head_end != std::string::npos) {
            size_t body_start = head_end + 4;
            long len = content_length(inbox.substr(0, head_end));
            if (len < 0) {
                // No declared length: nothing tells us where the body ends, so
                // take everything that arrived and let the parser reject it if
                // that was wrong.
                out = inbox;
                inbox.clear();
                return true;
            }
            size_t have = inbox.size() - body_start;
            if (have >= static_cast<size_t>(len)) {
                out = inbox.substr(0, body_start + len);
                inbox.erase(0, body_start + len);
                return true;
            }
        }
        char tmp[4096];
        ssize_t n = ::recv(fd, tmp, sizeof tmp, 0);
        if (n <= 0) return false;
        inbox.append(tmp, static_cast<size_t>(n));
    }
}

} // namespace

struct HttpTransport::Impl {
    // The reader thread and the caller can touch this object at the same time,
    // and the reader necessarily sees it first: Client::connect starts the
    // reader before the first write, so the socket does not exist yet when
    // read_message is called. The state therefore needs three values, not two:
    // not-yet-connected, connected, gone.
    std::mutex mu;
    std::condition_variable cv;

    int fd = -1;
    std::string host, port, path;
    std::string inbox;
    bool closed = false;

    ~Impl() {
        if (fd >= 0) ::close(fd);
    }

    bool connect() {
        if (fd >= 0) return true;
        addrinfo hints{};
        hints.ai_family = AF_UNSPEC;
        hints.ai_socktype = SOCK_STREAM;
        addrinfo* res = nullptr;
        if (::getaddrinfo(host.c_str(), port.c_str(), &hints, &res) != 0) return false;

        for (addrinfo* ai = res; ai != nullptr; ai = ai->ai_next) {
            fd = ::socket(ai->ai_family, ai->ai_socktype, ai->ai_protocol);
            if (fd < 0) continue;
            if (::connect(fd, ai->ai_addr, ai->ai_addrlen) == 0) {
                int one = 1;
                ::setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
                break;
            }
            ::close(fd);
            fd = -1;
        }
        ::freeaddrinfo(res);
        return fd >= 0;
    }
};

HttpTransport::HttpTransport(std::string host, std::string port, std::string path)
    : impl_(new Impl{}) {
    impl_->host = std::move(host);
    impl_->port = std::move(port);
    impl_->path = std::move(path);
}

HttpTransport::~HttpTransport() { delete impl_; }

void HttpTransport::write_message(const std::string& msg) {
    {
        std::lock_guard<std::mutex> lock(impl_->mu);
        if (impl_->closed) return;
        if (!impl_->connect()) return;
    }

    std::string req;
    req += "POST " + impl_->path + " HTTP/1.1\r\n";
    req += "Host: " + impl_->host + ":" + impl_->port + "\r\n";
    req += "Content-Type: application/json\r\n";
    // MCP requires this pair: a server is allowed to answer either a plain
    // JSON body or an SSE stream, and one that checks will return 406 if we
    // do not say we can handle both.
    req += "Accept: application/json, text/event-stream\r\n";
    req += "Content-Length: " + std::to_string(msg.size()) + "\r\n";
    req += "Connection: keep-alive\r\n\r\n";
    req += msg;

    // send is allowed to write less than asked. For a request larger than the
    // socket buffer it will.
    size_t sent = 0;
    while (sent < req.size()) {
        ssize_t n = ::send(impl_->fd, req.data() + sent, req.size() - sent, MSG_NOSIGNAL);
        if (n <= 0) {
            std::lock_guard<std::mutex> lock(impl_->mu);
            impl_->closed = true;
            impl_->cv.notify_all();
            return;
        }
        sent += static_cast<size_t>(n);
    }

    // A reader that arrived before the socket existed is waiting on this.
    impl_->cv.notify_all();
}

std::optional<std::string> HttpTransport::read_message() {
    int fd;
    {
        std::unique_lock<std::mutex> lock(impl_->mu);
        // Wait for the first write to establish the connection. Returning
        // nullopt instead would be read as end-of-stream, and the reader
        // thread would retire before the conversation started -- which is not
        // a corner case, it is the ordinary startup order.
        impl_->cv.wait(lock, [&] { return impl_->closed || impl_->fd >= 0; });
        if (impl_->closed) return std::nullopt;
        fd = impl_->fd;
    }

    // recv runs outside the lock: it blocks, and holding the mutex across it
    // would stop the peer ever being able to write.
    std::string resp;
    if (!read_response(fd, impl_->inbox, resp)) {
        std::lock_guard<std::mutex> lock(impl_->mu);
        impl_->closed = true;
        impl_->cv.notify_all();
        return std::nullopt;
    }

    size_t head_end = resp.find("\r\n\r\n");
    if (head_end == std::string::npos) return std::string{};
    std::string body = resp.substr(head_end + 4);
    if (body.empty()) {
        // 202 Accepted for a notification. The stream is still perfectly
        // alive, so this is an empty message rather than no message: treating
        // it as EOF would retire the reader on the very first notification,
        // which every handshake sends.
        return std::string{};
    }
    // SSE responses arrive as "data: {...}\n\n". Unwrap to the payload the
    // JSON parser expects.
    if (body.compare(0, 5, "data:") == 0) {
        std::string payload;
        size_t pos = 0;
        while (pos < body.size()) {
            size_t eol = body.find('\n', pos);
            if (eol == std::string::npos) eol = body.size();
            std::string line = body.substr(pos, eol - pos);
            pos = eol + 1;
            if (line.compare(0, 5, "data:") == 0) {
                std::string v = line.substr(5);
                if (!v.empty() && v[0] == ' ') v.erase(0, 1);
                payload += v;
            }
        }
        return payload.empty() ? std::string{} : payload;
    }
    return body;
}

void HttpTransport::close() {
    {
        std::lock_guard<std::mutex> lock(impl_->mu);
        if (impl_->fd >= 0) {
            // shutdown before close, in that order, or close hangs.
            //
            // It hangs because the reader thread is blocked in recv on this
            // very descriptor. Closing it does not reliably wake that call --
            // on Linux the descriptor is released while a syscall still
            // references it, and the blocked read simply never returns. Since
            // the reader is joined in Client's destructor, "never returns"
            // means the program cannot exit. shutdown(SHUT_RDWR) is what makes
            // a blocked recv come back with EOF.
            ::shutdown(impl_->fd, SHUT_RDWR);
            ::close(impl_->fd);
            impl_->fd = -1;
        }
        impl_->closed = true;
    }
    impl_->cv.notify_all();
}

} // namespace mcp
