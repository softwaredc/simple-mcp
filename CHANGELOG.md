# Changelog

## [1.0.0] - 2026-10-24

First public release.

### Added

- **Go implementation.** Six layers: JSON-RPC 2.0 framing, stdio and Streamable
  HTTP transports, the initialize handshake, tools, resources, and prompts,
  plus a client with a demultiplexing reader goroutine.
- **C++ implementation.** The same six layers, with a hand-written JSON parser
  and serializer, POSIX-socket HTTP client, and a reader thread.
- **JSON Schema subset validation** on tool arguments, in both languages.
- **Demo server and client** in both languages. The client spawns the server as
  a child process and speaks over its stdin/stdout, the way Claude Desktop and
  most CLI agents do.
- **Test suites.** Go: 32 tests including a client/server integration test over
  an in-memory pipe, run under `-race`. C++: 70 assertions including a
  client/server loopback test.
- **Benchmark harness** reporting round-trip percentiles, throughput, wire
  sizes, and a cost breakdown separating JSON encoding from transport overhead.
- **Documentation.** Architecture, protocol notes, pitfalls with reproductions,
  and benchmark methodology, in English and Chinese.

### The error taxonomy this implementation is built around

A malformed call returns a JSON-RPC error. A tool that ran and failed returns a
result with `isError` set. Both implementations, and the tests in both, treat
this as the distinction that decides whether a client retries.

### Known limitations

- Streamable HTTP is implemented in its single-response form. A server holding
  a POST open to stream an unbounded sequence of messages is not implemented;
  `docs/PROTOCOL.md` records exactly what is missing.
- The C++ build has no HTTP *server*. The Go build has one.
- No pagination (`nextCursor`). `tools/list` returns everything.
- No notifications beyond `notifications/initialized`: no progress, no
  cancellation, no list-changed events.
- No `logging/setLevel`, no `completion/complete`, no sampling.
- Protocol version 2025-06-18 is the default; 2025-03-26 and 2024-11-05 are
  accepted in the handshake.

### Measured

Round-trip latency for one `tools/call` over stdio: p50 10.7 µs, of which 1.5 µs
is JSON encoding and decoding. Five consecutive runs gave p50 between 10.7 and
11.5 µs. Full table, machine details and reproduction instructions in
[docs/BENCHMARKS.md](docs/BENCHMARKS.md).
