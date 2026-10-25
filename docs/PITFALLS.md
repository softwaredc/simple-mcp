# Pitfalls

Seven things that break an MCP implementation, each with the failure mode, the
cause, and a test that catches it. Every test referenced here exists and fails
if the code regresses.

Chinese version: [PITFALLS_CN.md](PITFALLS_CN.md).

---

## 1. `bufio.Scanner` silently caps your messages at 64 KiB

**Symptom.** A tool that returns a source file, a JSON dump or a base64 image
works in testing and then fails in production with:

```
bufio.Scanner: token too long
```

Nothing in that message mentions MCP, so people go looking in the wrong place
for hours.

**Cause.** `bufio.Scanner`'s default maximum token size is 64 KiB. The stdio
transport is line-delimited, so a message larger than that is not a message as
far as the scanner is concerned.

**Fix.** Use `bufio.Reader.ReadBytes('\n')`, which grows to whatever the message
actually is, and impose your own cap if you want one. This implementation caps
at 16 MiB, which is far above any real MCP message and still bounds memory per
connection.

```go
// go/transport/stdio.go
line, err := t.r.ReadBytes('\n')
```

**Test.** `TestLargeMessageSurvivesStdio` pushes 300 KiB through the transport
and asserts it comes back byte-identical. `TestScannerWouldHaveFailedHere`
demonstrates that a default `Scanner` refuses the same input, so the trap stays
documented rather than becoming folklore.

The C++ side has the same property for free: `std::getline` has no length cap.
That is worth knowing, because it is the reason the C++ transport needs no
special handling.

---

## 2. stdout belongs to the protocol

**Symptom.** The server works when you run it by hand and fails the moment a
client launches it. The client reports a parse error on the first message.

**Cause.** Over stdio, the server's stdout *is* the protocol channel. One line
of `fmt.Println("listening")` on stdout, and the client's first read gets
`listening\n{"jsonrpc":"2.0",...}`, which is not JSON.

**Fix.** Diagnostics go to stderr. Always.

```go
log.SetPrefix("mcpserver: ")   // writes to stderr by default
```

**Test.** Not directly testable without a subprocess, which is why
`cmd/mcpserver` sets its logger's output explicitly and why the demo client
wires the child's stderr to its own. If you add logging to a server in this
repo, the review question is "which stream does this go to".

---

## 3. Notifications get no response

**Symptom.** A client hangs after the handshake, or reads a response while
waiting for a different one, and the two are off by one forever.

**Cause.** A JSON-RPC request with no `id` is a notification, and notifications
must not be answered. `notifications/initialized` is a notification. A server
that replies to it has just sent an unsolicited message into a stream where the
client is matching responses to requests by id.

**Fix.** Check `IsNotification()` before writing anything back, and return nil
from the dispatch path for methods that legitimately have nothing to say.

```go
if req.IsNotification() || out == nil {
    return nil, nil
}
```

**Test.** `TestNotificationGetsNoResponse` sends `notifications/initialized`
and asserts the server produces zero bytes.

---

## 4. The `id` must round-trip byte for byte

**Symptom.** A client hangs forever waiting for a response that already
arrived. Or it works with small ids and breaks with large ones.

**Cause.** Decoding `id` into `interface{}` gives you a `float64`. Re-encoding
that can produce `1` or `1e+00` depending on magnitude, and integers beyond 2^53
lose precision entirely. A client that stored `"9007199254740993"` and is now
looking for `"9007199254740992"` never finds its response.

**Fix.** Keep the id as raw bytes: `json.RawMessage` in Go, the literal text in
C++. Echo it back untouched.

```go
type Request struct {
    ID json.RawMessage `json:"id,omitempty"`
}
```

**Test.** `TestIDRoundTrip` in the Go protocol package feeds five id shapes,
including one beyond float64's exact range, and asserts each appears verbatim in
the response. The C++ equivalent is `test_number_literal_preserved`.

---

## 5. A client needs a reader goroutine

**Symptom.** The client works against your own server and hangs against a real
one. Or it works until the day the server sends something unprompted.

**Cause.** Write-a-request-then-read-a-response assumes the server never speaks
first. It can: progress notifications, log messages, list-changed events. The
first time one arrives ahead of a response, the client reads a notification
while waiting for a response and the conversation is off by one permanently.

**Fix.** A reader goroutine that reads everything and routes by whether the
message carries an id. Responses go to whoever is waiting; notifications are
dispatched or dropped.

```go
// go/client/client.go
func (c *Client) readLoop() {
    for {
        raw, err := c.t.ReadMessage()
        ...
        if len(resp.ID) == 0 {
            continue // notification
        }
        ch, ok := c.pending[string(resp.ID)]
        if ok { ch <- resp }
    }
}
```

**Test.** The integration tests exercise the real path: a client and a server
over a pipe, with the reader goroutine running from before the first request.
There is a comment in `Connect()` explaining why the goroutine starts first —
with a synchronous pipe, the server's response blocks until somebody reads it.

---

## 6. An unknown resource is `-32002`, not `-32602`

**Symptom.** A client retries a `resources/read` on a URI that does not exist,
three times, then gives up and reports the server as broken.

**Cause.** `-32602` means "your parameters are malformed". `-32002` means "the
resource you asked about does not exist". They are different situations with
different correct responses, and conflating them makes a client retry something
that can never succeed.

**Fix.** Use the MCP-specific code for the MCP-specific condition.

```go
return nil, protocol.NewError(protocol.ErrResourceNotFound,
    "no resource at "+params.URI, nil)
```

**Test.** `TestResourcesAndPrompts` asserts the code, not the message, because
the message can be reworded without the behaviour changing.

---

## 7. Tool failure and protocol failure are different things

**Symptom.** An agent retries a tool three times, gives up, and tells the user
the tool is broken. The tool was fine; it just could not find the city.

**Cause.** The handler returned an error, and the dispatch layer turned every
error into a JSON-RPC error. But a tool that ran and failed should return a
*result* with `isError` set, so the client knows the call executed and must not
retry.

**Fix.** Two paths, chosen deliberately:

```go
content, herr := entry.handler(args)
if herr != nil {
    // A handler may escalate deliberately by returning an *RPCError.
    var rpc *protocol.RPCError
    if errors.As(herr, &rpc) { return nil, rpc }

    // Everything else is a tool-level failure.
    return result(req.ID, mcptypes.CallToolResult{
        Content: []mcptypes.Content{
            mcptypes.Text(fmt.Sprintf("tool %q failed: %v", params.Name, herr)),
        },
        IsError: true,
    })
}
```

**Test.** `TestToolLevelFailureIsNotProtocolError` calls a tool that always
fails and asserts the client gets a result with `isError`, not an error.
`TestSchemaViolationIsProtocolError` does the mirror image: a wrong argument
type must be a protocol error.

---

## Bonus: three that are easy to miss

**Closing a socket from another thread does not wake the reader.** HTTP
keep-alive means one socket carries every request, and `Client` owns that socket
plus a reader thread parked in `recv`. When `close()` runs, shutting the fd down
does not unblock a `recv` that is already waiting on that same fd — the thread
keeps sleeping on a descriptor that no longer exists, and the destructor's
`join()` never returns. The process hangs at exit, after printing all the right
answers. Closing has to take the lock, flip a flag, wake the condition variable,
and let the reader thread see the flag and leave on its own.

What makes this one worth writing down is that **every unit test passed while
the bug was present**. Both implementations were green; nothing in-process could
reach it, because the failure needs two threads on two sides of a real socket.
It surfaced only when the C++ client was pointed at the running Go server —
`cpp/examples/http_client_main.cpp`, which stays in the repo for exactly this
reason. If your project has two implementations, one of the most valuable tests
you can have is one that never pretends they agree.

**A notification arriving first still has to be routed.** The reader goroutine
in pitfall 5 handles this, but the C++ version of the same bug is subtler:
calling `parse_request` on a *response* throws, because a response has no
`method` field, and the `continue` that catches the exception drops the
response on the floor. That looks exactly like a hung client. The C++ reader
loop therefore parses responses as generic objects and never runs them through
the request parser.

**An empty `arguments` object is not the same as a missing one.** `tools/call`
with no `arguments` field is legal; a handler that does `args["x"]` on a nil map
panics in Go and reads garbage in C++. Both implementations default to `{}`
before handing arguments to a handler.
