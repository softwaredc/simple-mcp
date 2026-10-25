# Protocol notes

What this implementation covers, what it leaves out, and where the spec has
detail worth knowing before you build on it.

Chinese version: [PROTOCOL_CN.md](PROTOCOL_CN.md).

## The message envelope

MCP runs on JSON-RPC 2.0. Three message shapes:

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{...}}   // request
{"jsonrpc":"2.0","id":1,"result":{...}}                          // response
{"jsonrpc":"2.0","method":"notifications/initialized"}           // notification
```

A request without `id` is a notification and gets no response. A response
carries exactly one of `result` and `error`. `id` may be a string or a number,
and must be echoed unchanged — see
[pitfall 4](PITFALLS.md#4-the-id-must-round-trip-byte-for-byte).

## Methods implemented

| Method | Direction | Notes |
|---|---|---|
| `initialize` | client → server | capability negotiation |
| `notifications/initialized` | client → server | no response |
| `ping` | both | liveness, empty result |
| `tools/list` | client → server | all tools, no pagination |
| `tools/call` | client → server | schema-validated |
| `resources/list` | client → server | all resources |
| `resources/templates/list` | client → server | always empty here |
| `resources/read` | client → server | `-32002` when unknown |
| `prompts/list` | client → server | all prompts |
| `prompts/get` | client → server | required-argument check |

## Methods deliberately not implemented

- **`tools/list` pagination.** `nextCursor` is returned in the type but never
  set. A server with enough tools to need it has outgrown a teaching build.
- **Notifications from server to client.** No `notifications/progress`, no
  `notifications/cancelled`, no `notifications/message` (logging), no
  `.../list_changed`. The reader loop *routes* notifications correctly, so
  adding them is a matter of registering handlers, not restructuring.
- **`logging/setLevel`.** Needs the logging capability and a log sink.
- **`completion/complete`.** Argument autocompletion; a nicety, not a core path.
- **Sampling.** The server asking the client to run a model completion. This is
  the one genuinely interesting piece missing, and it is missing because it
  needs a model to demonstrate.
- **`resources/subscribe` / `unsubscribe`.** Subscription updates.

## The initialize handshake

```
client                                server
  |  initialize                        |
  |   protocolVersion: "2025-06-18"    |
  |   capabilities: {...}              |
  |   clientInfo: {name, version}      |
  | ---------------------------------> |
  |                                    |  result:
  |                                    |   protocolVersion (server's choice)
  |                                    |   capabilities
  |                                    |   serverInfo
  |                                    |   instructions?
  | <--------------------------------- |
  |  notifications/initialized         |
  | ---------------------------------> |   (no response)
```

Three details that matter:

1. **The server answers with a version it supports**, which may differ from the
   one the client asked for. This implementation prefers the client's version
   when it knows it, and falls back to its newest otherwise.
2. **Capabilities are optional pointers.** An absent capability means "not
   supported"; an empty object means "supported, no list-change notifications".
   `{"tools":{}}` and no `tools` key are different statements.
3. **`instructions` is free text the server returns to be surfaced to the
   model.** It is the cheapest quality lever in the whole protocol: a server
   that explains how to use its tools gets called correctly more often.

## Tool arguments and JSON Schema

`tools/list` returns an `inputSchema` per tool, which must be a JSON Schema
object. `tools/call` sends `arguments` as an arbitrary JSON object. The spec
does not say who validates, so the server does, and this implementation
understands the subset real tools use:

```json
{
  "type": "object",
  "properties": {
    "city":  {"type": "string"},
    "days":  {"type": "integer"},
    "unit":  {"type": "string", "enum": ["celsius", "fahrenheit"]},
    "tags":  {"type": "array", "items": {"type": "string"}}
  },
  "required": ["city"]
}
```

`type`, `properties`, `required`, `items`, `enum`. Anything beyond that can be
passed through as a raw schema and validated by the handler.

Note that `"integer"` accepts a whole-valued number: JSON has one number type,
and "integer" is a constraint on the value, not a distinct type.

## Content blocks

A tool result is an array of content blocks discriminated by `type`:

| type | fields | used for |
|---|---|---|
| `text` | `text` | almost everything |
| `image` | `data` (base64), `mimeType` | screenshots, diagrams |
| `audio` | `data` (base64), `mimeType` | voice |
| `resource` | `resource: {uri, mimeType?, text? \| blob?}` | embedded file reads |

Prompt messages carry the same blocks. Getting the tag wrong produces
`{"type":"text","data":"..."}`, which clients reject.

## The error taxonomy

| code | meaning | when to use |
|---|---|---|
| `-32700` | parse error | the bytes were not JSON |
| `-32600` | invalid request | not a valid Request object |
| `-32601` | method not found | unknown method |
| `-32602` | invalid params | malformed params, unknown tool, schema violation |
| `-32603` | internal error | the server broke |
| `-32002` | resource not found | MCP-specific: the URI does not exist |

Everything else that goes wrong inside a tool is **not** an error code. It is a
result with `isError: true`. See
[pitfall 7](PITFALLS.md#7-tool-failure-and-protocol-failure-are-different-things).

## Transports

### stdio

One JSON object per line on the server's stdin and stdout. This is what Claude
Desktop and most CLI agents use, because launching a subprocess is the simplest
thing an agent host can do.

Two rules: every write ends with exactly one `\n` and is flushed, and stdout
carries nothing but protocol.

### Streamable HTTP

The client POSTs a message to an endpoint. The server answers with either
`application/json` (one response) or `text/event-stream` (an SSE stream). The
client may also GET an SSE stream to receive server-to-client notifications.

**This implementation covers the single-response form and one-shot streams.**
It does not implement a server holding a POST open to push an unbounded sequence
of messages, nor a long-lived GET stream that carries real events — the GET
handler opens the stream and closes it, because the demo server never pushes
notifications unprompted.

That is a real simplification and it is stated here rather than buried, because
a reader deciding whether to build on this needs to know where the edge is. The
Go build has both a client and a server for HTTP; the C++ build has a client
only.

## Protocol version

Default `2025-06-18`. `2025-03-26` and `2024-11-05` are accepted in the
handshake. Fields removed in later revisions are not implemented, so a
2024-11-05 client gets the newest behaviour rather than the behaviour it asked
for — the handshake says so explicitly, and a stricter server would refuse.
