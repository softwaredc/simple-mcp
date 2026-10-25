# simple-mcp

Zero-dependency MCP (Model Context Protocol) implementation, written twice: Go
and C++. Every layer annotated.

[中文说明](README_CN.md) | English

[![CI](https://github.com/softwaredc/simple-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/softwaredc/simple-mcp/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.21+-00ADD8.svg)](go/go.mod)
[![C++](https://img.shields.io/badge/C++-17-00599C.svg)](cpp/CMakeLists.txt)
[![Dependencies](https://img.shields.io/badge/dependencies-none-brightgreen.svg)](go/go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

---

MCP is the protocol that lets a language model call tools, read resources and
use prompt templates. Most people use it through an SDK and never read the
wire format. This repository is the wire format, in full, in two languages,
small enough to read in an afternoon.

It is **not** a production SDK. It is the thing you read to understand the one
you use in production, and the thing you copy when you need MCP in a codebase
that cannot take a dependency.

## Numbers

Measured on the reference machine in [docs/BENCHMARKS.md](docs/BENCHMARKS.md).
Reproduce with `make bench`.

| | |
|---|---:|
| Go implementation | 2,692 lines |
| Go tests | 911 lines, 32 tests, `-race` clean |
| C++ implementation | 2,334 lines (1,721 source + 613 headers) |
| C++ tests | 70 assertions, ASan/UBSan clean |
| Round-trip latency, one `tools/call` over stdio | **p50 10.7 µs** |
| of which JSON encode + decode | 1.5 µs |
| Throughput, stdio | ~67,000 msg/s |
| `tools/list` response size | 352 bytes |

## Build

```bash
# Go
cd go && go build ./... && go test ./...

# C++
cmake -S cpp -B cpp/build -DCMAKE_BUILD_TYPE=Release
cmake --build cpp/build -j
cd cpp/build && ctest --output-on-failure
```

No dependencies. Go uses the standard library; C++ uses the standard library
and POSIX sockets.

## Run the demo

```bash
cd go
go build -o bin/mcpserver ./cmd/mcpserver
go build -o bin/mcpclient ./cmd/mcpclient
./bin/mcpclient -server ./bin/mcpserver
```

The client spawns the server as a child process and speaks JSON-RPC over its
stdin and stdout, which is how a real agent host does it. Expected output:

```
connected to simple-mcp-demo 1.0.0 (protocol 2025-06-18)
instructions: Demo server. Use echo to repeat text, add to sum two integers, and get_weather for a fixed mock forecast.

3 tools:
  echo           Repeat the given text back verbatim. Use to verify connectivity.
  add            Add two integers and return the sum. Use for arithmetic, not for string concatenation.
  get_weather    Return a mock forecast for a city. The data is fixed and synthetic.

calling tools:
  echo           hello from the client
  add            42
  get_weather    Beijing: 22 C (mock, clear)

error taxonomy:
  wrong argument type -> protocol error: rpc error -32602: invalid arguments: city: expected string, got number
```

The same demo exists in C++:

```bash
cmake --build cpp/build -j
./cpp/build/mcp_client ./cpp/build/mcp_server
```

## Point your own client at it

The server speaks stdio, so anything that launches a subprocess works:

```json
{
  "mcpServers": {
    "simple-mcp": {
      "command": "/path/to/bin/mcpserver"
    }
  }
}
```

Or run it over HTTP: `./bin/mcpserver -http :8080`, then connect to
`http://localhost:8080/mcp`.

## The six layers

| Layer | Responsibility | Go | C++ |
|---|---|---|---|
| 1. JSON-RPC 2.0 | request / response / notification framing, error codes | `go/protocol/` | `cpp/src/jsonrpc.cpp` |
| 2. Transport | newline-delimited JSON over stdio; POST + SSE over HTTP | `go/transport/` | `cpp/src/transport_stdio.cpp`, `transport_http.cpp` |
| 3. Handshake | `initialize`, capability negotiation, `notifications/initialized` | `go/server/server.go` | `cpp/src/server.cpp` |
| 4. Tools | `tools/list`, `tools/call`, JSON Schema validation | `go/server/handlers.go` | `cpp/src/server.cpp` |
| 5. Resources & prompts | `resources/list`, `resources/read`, `prompts/list`, `prompts/get` | `go/server/handlers.go` | `cpp/src/server.cpp` |
| 6. Client SDK | request/response matching, reader goroutine, typed methods | `go/client/` | `cpp/src/client.cpp` |

Reading order: start at `go/protocol/jsonrpc.go`, then `go/server/server.go`,
then `go/client/client.go`. The C++ versions mirror them file for file.

## The thing worth understanding

MCP has two kinds of failure, and they are not interchangeable.

```
malformed call        ->  JSON-RPC error       (-32602, -32601, -32002 ...)
tool ran and failed   ->  result with isError  (content explains what went wrong)
```

A client that receives a JSON-RPC error treats the call as not having happened.
A client that receives `isError` knows the tool executed. If a tool that cannot
find a city reports a protocol error, a well-behaved agent retries it three
times, gives up, and reports that the tool is broken, when in fact the tool
worked exactly as intended.

`go/server/handlers.go` is where this decision is made, and
`go/server/integration_test.go` asserts both halves of it from the client's
side. The C++ tests do the same.

## Pitfalls this codebase is built to avoid

Each of these has a test that fails if the code regresses.

1. **`bufio.Scanner` caps a message at 64 KiB.** A tool returning a source file
   or a base64 image exceeds that routinely, and the failure message mentions
   nothing about MCP. `go/transport/transport_test.go` proves a 300 KiB message
   crosses intact, and proves that a default `Scanner` refuses it.
2. **stdout belongs to the protocol.** One log line on stdout breaks every
   client that parses the stream. Diagnostics go to stderr.
3. **Notifications get no response.** Answering `notifications/initialized`
   makes a client read a response while waiting for the next real one.
4. **The id must round-trip byte for byte.** Decoding `id` into `interface{}`
   turns `1` into the float64 `1`, and a client matching responses by id hangs.
5. **A client needs a reader goroutine.** Write-then-read works until a
   notification arrives first.
6. **An unknown resource is `-32002`, not `-32602`.** One means "malformed",
   the other means "does not exist", and they need different recovery.
7. **JSON Schema validation is the server's job.** MCP does not specify who
   validates tool arguments, so if you do not, nobody does.

Full write-ups with reproductions in [docs/PITFALLS.md](docs/PITFALLS.md).

## Documentation

- [Architecture](docs/ARCHITECTURE.md) · [架构（中文）](docs/ARCHITECTURE_CN.md)
- [Protocol notes](docs/PROTOCOL.md) · [协议笔记（中文）](docs/PROTOCOL_CN.md)
- [Pitfalls](docs/PITFALLS.md) · [踩坑（中文）](docs/PITFALLS_CN.md)
- [Benchmarks](docs/BENCHMARKS.md) · [基准测试（中文）](docs/BENCHMARKS_CN.md)
- [Publishing to GitHub](docs/PUBLISHING.md) · [发布步骤（中文）](docs/PUBLISHING_CN.md)
- [Getting stars](docs/GETTING-STARS.md) · [涨星策略（中文）](docs/GETTING-STARS_CN.md)
- [Article, in Chinese](articles/01-mcp-from-scratch.md)

## Limitations

Stated up front, because a teaching implementation that hides its edges teaches
the wrong lesson.

- **Linux and macOS.** The C++ HTTP transport uses POSIX sockets.
- **Not a production SDK.** No retry, no pooling, no pagination, no
  cancellation, no progress notifications, no sampling.
- **Streamable HTTP is simplified.** Single-response form only; a server
  holding a POST open to stream is not implemented. See
  [docs/PROTOCOL.md](docs/PROTOCOL.md).
- **The C++ build has no HTTP server.** Writing one by hand would add a few
  hundred lines to teach a lesson the Go version already teaches.
- **`get_weather` returns fixed mock data.** A demo that calls a real API needs
  a key, breaks offline, and turns rate limits into a teaching problem.
- **Protocol version 2025-06-18** is the default; older revisions are accepted
  in the handshake but their removed fields are not implemented.

## Layout

```
go/
  protocol/    JSON-RPC 2.0: framing, ids, error codes
  transport/   stdio and Streamable HTTP
  mcptypes/    wire types, including the Content tagged union
  server/      registries, handshake, dispatch, schema validation
  client/      typed methods, reader goroutine, pending-request table
  cmd/         mcpserver, mcpclient, mcpbench
cpp/
  include/mcp/ headers, one per layer
  src/         implementations, one per layer
  tests/       70 assertions
  examples/    server_main, client_main, http_client_main
docs/          architecture, protocol, pitfalls, benchmarks
```

`http_client_main` exists to do one thing no unit test can: it points the C++
client at the running Go server and speaks to it over a real socket. That is
how the socket-close trap in [PITFALLS](docs/PITFALLS.md) was found — both
implementations were green while the client deadlocked on exit. To run it:

```bash
cd go && go build -o mcpserver ./cmd/mcpserver && ./mcpserver -http 127.0.0.1:8080 &
cmake -S cpp -B cpp/build -DCMAKE_BUILD_TYPE=Release && cmake --build cpp/build -j
./cpp/build/mcp_http_client 127.0.0.1 8080
```

## Contributing

Corrections are the most valuable contribution this repository can receive. If
a comment is wrong or a protocol detail does not match the spec, please file an
issue pointing at the spec section. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
