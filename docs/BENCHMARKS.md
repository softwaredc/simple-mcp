# Benchmarks

Everything here was measured on the machine described below, with the harness in
`go/cmd/mcpbench`. Nothing is extrapolated, and nothing is compared against a
number I did not produce myself.

Chinese version: [BENCHMARKS_CN.md](BENCHMARKS_CN.md).

## Reference environment

```
go1.22.2 linux/amd64, 16 CPUs
Intel Core Ultra 7 356H (mobile, 6P+8E+2LPE)
WSL2, kernel 6.18.33, Ubuntu 24.04
gcc 13.3.0, g++ 13.3.0
```

WSL2 is not bare metal. Its futex and syscall costs are higher than a native
kernel's, so absolute numbers here are worse than the same code would produce on
real hardware. The *ratios* between transports still hold, because all of them
pay the same WSL2 tax.

Reproduce with:

```bash
make bench          # or: cd go && go run ./cmd/mcpbench -n 20000
```

## Run-to-run variance, stated up front

Five consecutive runs of the same binary, stdio p50:

```
10,742   10,874   11,049   11,111   11,356 ns
```

That is a 6% spread within one sitting. Across sittings, hours apart, the same
binary has produced 8.9 µs and 11.5 µs — a 25% spread. The cause is CPU frequency
scaling and whatever else the machine was doing.

So: the tables below are one representative run. The median is stable enough to
quote, and the tail moves by a factor of two between runs. Report the median,
distrust the tail, and re-run before you compare against anything.

## Round-trip latency, one `tools/call`

20,000 iterations after 1,000 warm-up rounds, percentiles computed from the full
sample set (not from bucketed counts).

| transport | p50 | p90 | p99 | p99.9 |
|---|---:|---:|---:|---:|
| stdio | **10,742 ns** | 20,991 ns | 76,266 ns | 276,777 ns |
| HTTP (Streamable, loopback) | 59,610 ns | 113,603 ns | 295,533 ns | 616,020 ns |

HTTP costs about 5.5x stdio. That is not JSON — it is the syscalls, the
connection handling, and the fact that each round trip is a fresh POST.

## Where the 10.7 µs goes

This is the part that makes the headline number mean something.

| component | cost | share |
|---|---:|---:|
| JSON encode + decode of one request/response pair | 1,508 ns | 14% |
| transport, goroutine handoff, scheduler wakeups | 9,234 ns | 86% |

The JSON half is measured in isolation: the same request decoded and the same
response encoded in a tight loop, with no pipe and no goroutines. The remainder
is the pipe write, the channel send, and waking a thread that then has to be
scheduled.

Two things follow. First, a faster JSON library would move this number by at
most 14%, so anyone optimising an MCP implementation should look at the wakeup
path first. Second, the 5.5x HTTP penalty is the same story at larger scale: the
protocol is cheap, the syscalls are not.

## Throughput

| transport | messages/second |
|---|---:|
| stdio | 66,865 |
| HTTP | 14,750 |

Throughput here means "how fast can one client and one server exchange
`tools/call` round trips", not "how many messages can the ring absorb". A
pipeline that batches would show a different and much larger number; that is
not what an agent does.

## Wire size

| message | bytes |
|---|---:|
| `initialize` response | 205 |
| `tools/list` response, 2 tools | 352 |
| `tools/call` request + response | 170 |

`tools/list` is the message every agent sends first, so its size is what you
feel when you read a trace. A real server with fifteen tools lands in the low
kilobytes, which is why `nextCursor` pagination exists.

## What is not measured here

Anything that needs a model in the loop:

- how often a model picks the right tool
- whether a tool description rewrite improves call accuracy
- how much a context-compression strategy saves

Those need an LLM, an evaluation set, and a budget. They will be reported
separately, with the harness that produced them, or not at all. A benchmark that
quietly mixes protocol cost with model behaviour is not a benchmark.

## Test counts

| | |
|---|---:|
| Go tests | 32 passing, including a client/server integration test, `-race` clean |
| Go assertion sites | 88 across `protocol` (12), `transport` (21), `server` (55) |
| C++ checks | 70 passing |
| C++ ASan + UBSan | clean, with leak detection on |
| C++ ThreadSanitizer | not run locally: it aborts on WSL2 before main. CI-only claim. |
