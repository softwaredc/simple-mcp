# Architecture

Six layers, each in its own package or file pair, each with one job.

Chinese version: [ARCHITECTURE_CN.md](ARCHITECTURE_CN.md).

```
                    ┌─────────────────────────────┐
   your code  ───►  │  6. Client                  │  typed methods,
                    │     reader goroutine        │  request matching
                    └──────────────┬──────────────┘
                                   │ Transport
                    ┌──────────────▼──────────────┐
                    │  2. Transport               │  stdio / HTTP
                    └──────────────┬──────────────┘
                                   │ []byte
                    ┌──────────────▼──────────────┐
                    │  1. JSON-RPC 2.0            │  framing, ids, codes
                    └──────────────┬──────────────┘
                                   │
                    ┌──────────────▼──────────────┐
                    │  3. Handshake               │  initialize, capabilities
                    │  4. Tools                   │  list, call, schema
                    │  5. Resources & prompts     │  list, read, get
                    └─────────────────────────────┘
                                   Server
```

The dependency direction is one-way: layers above use layers below, never the
reverse. The `Transport` interface is the only thing the client and server share,
which is why the integration test can run both over an in-memory pipe.

## Layer 1: JSON-RPC 2.0

`go/protocol/`, `cpp/src/jsonrpc.cpp`.

Owns framing, ids, and error codes. The one non-obvious decision: `id` is
`json.RawMessage` in Go and literal text in C++, so it round-trips byte for
byte. Everything else in this layer is mechanical.

## Layer 2: Transport

`go/transport/`, `cpp/src/transport_stdio.cpp`, `cpp/src/transport_http.cpp`.

Two methods: read a message, write a message. stdio is newline-delimited JSON;
HTTP is POST-and-read. The interesting part is what is *not* here: no buffering
decisions, no retry, no connection management beyond what the transport needs
to function.

`bufio.Reader.ReadBytes` rather than `bufio.Scanner` because of the 64 KiB cap
(see [pitfall 1](PITFALLS.md#1-bufioscanner-silently-caps-your-messages-at-64-kib)).

## Layer 3: Handshake

`go/server/server.go`, `handleInitialize`.

`initialize` sets a flag; every other method checks it. One check at the top of
`dispatch` rather than one per handler, so a new method cannot forget it.

## Layer 4: Tools

`go/server/handlers.go`, `handleToolCall`.

The registry maps name to a handler. The interesting code is the error fork:
protocol error for a malformed call, `isError` result for a tool that ran and
failed. Schema validation lives here too.

## Layer 5: Resources and prompts

`go/server/handlers.go`, `handleResourceRead`, `handlePromptGet`.

Straightforward lookups. The only subtlety is the error code: `-32002` for an
unknown URI, not `-32602`.

## Layer 6: Client

`go/client/`, `cpp/src/client.cpp`.

A table of pending requests keyed by id, and a reader goroutine that fills it.
Every method is a thin wrapper over `call()`. The client does not retry, does
not pool, and does not reconnect: those are production concerns and this is a
teaching implementation.

## Why two implementations

Not for coverage. The point is that the same protocol, implemented twice with
different constraints, shows which parts are inherent and which are incidental:

- Go gets HTTP for free from `net/http`; C++ has to open a socket and parse a
  response by hand, which makes the protocol's HTTP half visible.
- Go gets goroutines; C++ has to manage a `std::thread` and a condition
  variable, which makes the reader-loop design visible.
- Go gets `encoding/json`; C++ has to write a parser, which makes the
  number-fidelity problem visible.

The C++ build is not a port with comments. It is a second explanation.

## Test strategy

| level | where | what it proves |
|---|---|---|
| unit | `go/protocol`, `go/server/schema_test.go`, `cpp/tests` | one layer in isolation |
| integration | `go/server/integration_test.go`, `cpp/tests` | the layers compose |
| cross-language | `cpp/examples/http_client_main.cpp` | the C++ client talks to the Go server |
| benchmark | `go/cmd/mcpbench` | the cost is real and measured |

Two of those rows carry a caveat. TSan does not run on every machine: on WSL2
it aborts at startup with `FATAL: ThreadSanitizer: unexpected memory mapping`
before reaching main, so the sanitizer row is a CI claim rather than something
you can reproduce locally there. ASan and UBSan do run locally and are clean.
The cross-language row was added after the C++ client deadlocked against the Go
server once — see PITFALLS — which is the kind of bug no in-process test finds.

The integration test is the one that earns its keep. It runs a real client and a
real server over a real transport, with no mocks, which is the only way the
error taxonomy can be asserted from the client's side rather than assumed.
