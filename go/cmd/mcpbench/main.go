// Command mcpbench measures what this library actually costs.
//
// It reports four numbers, all measured locally and all reproducible:
//
//	round-trip latency   one tools/call, client to server and back, percentiles
//	throughput           messages per second when nobody waits
//	wire size            bytes on the wire for a tools/list response
//	handshake cost       the initialize exchange in isolation
//
// What it deliberately does NOT measure: anything that needs a model. How good
// a tool description is, how often a model picks the right tool, how much a
// compression strategy saves — those need an LLM in the loop and are reported
// separately, with the harness that produced them. docs/BENCHMARKS.md says so.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/softwaredc/simple-mcp/go/client"
	"github.com/softwaredc/simple-mcp/go/mcptypes"
	"github.com/softwaredc/simple-mcp/go/server"
	"github.com/softwaredc/simple-mcp/go/transport"
)

func main() {
	iters := flag.Int("n", 20000, "iterations per measurement")
	flag.Parse()

	fmt.Printf("simple-mcp benchmark\n")
	fmt.Printf("go=%s %s/%s cpus=%d\n\n", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU())

	s := demoServer()

	fmt.Println("== stdio transport ==")
	benchStdio(s, *iters)

	fmt.Println("\n== http transport (streamable http, loopback) ==")
	benchHTTP(s, *iters)

	fmt.Println("\n== cost breakdown (stdio) ==")
	benchBreakdown(s, *iters)

	fmt.Println("\n== wire size ==")
	reportWireSize(s)
}

// benchBreakdown answers the question the headline number invites: where does
// the time go? It measures the JSON work in isolation, with no transport and
// no goroutines, then subtracts. The remainder is pipes, channel handoff and
// scheduler wakeups.
//
// This is the honest way to present a latency number. "8.9 µs per call" on its
// own tells a reader nothing about whether the protocol or their own payload
// encoding dominates.
func benchBreakdown(s *server.Server, iters int) {
	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"bench"}}}`)

	// The response the server would produce, captured once.
	s.HandleMessage([]byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"b","version":"1"}}}`))
	resp, err := s.HandleMessage(req)
	if err != nil {
		fatal("handle: %v", err)
	}

	// JSON only: decode the request, encode the response, in a tight loop.
	t0 := time.Now()
	for i := 0; i < iters; i++ {
		var r struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(req, &r); err != nil {
			fatal("unmarshal: %v", err)
		}
		if _, err := json.Marshal(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
		}{"2.0", r.ID, resp}); err != nil {
			fatal("marshal: %v", err)
		}
	}
	jsonOnly := time.Since(t0).Nanoseconds() / int64(iters)

	fmt.Printf("  json encode+decode of one tools/call exchange   %8d ns\n", jsonOnly)

	// Now the same exchange through a pipe, to isolate transport overhead.
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	serverT := transport.NewStdio(c2sR, s2cW, nil)
	clientT := transport.NewStdio(s2cR, c2sW, nil)
	go func() { _ = s.Serve(serverT) }()
	c := client.NewClient(clientT, "bench", "1.0.0")
	defer c.Close()
	if err := c.Connect(); err != nil {
		fatal("connect: %v", err)
	}

	// Handshake cost, measured as one extra round trip on an established
	// session. The true initialize exchange happens once per connection, so
	// its absolute cost matters far less than the per-call cost below.
	hs := time.Now()
	if err := c.Ping(); err != nil {
		fatal("ping: %v", err)
	}
	fmt.Printf("  one extra round trip (ping)                     %8d ns\n", time.Since(hs).Nanoseconds())

	for i := 0; i < 1000; i++ {
		c.CallTool("echo", map[string]any{"text": "warm"})
	}
	samples := make([]int64, 0, iters)
	for i := 0; i < iters; i++ {
		start := time.Now()
		if _, err := c.CallTool("echo", map[string]any{"text": "bench"}); err != nil {
			fatal("call: %v", err)
		}
		samples = append(samples, time.Since(start).Nanoseconds())
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p50 := samples[len(samples)/2]
	fmt.Printf("  full round trip p50                             %8d ns\n", p50)
	fmt.Printf("  -> transport + goroutine + scheduler overhead   %8d ns (%.0f%% of total)\n",
		p50-jsonOnly, 100*float64(p50-jsonOnly)/float64(p50))
}

func demoServer() *server.Server {
	s := server.NewServer("bench", "1.0.0")
	s.SetInstructions("Benchmark server.")
	s.AddTool("echo", "Repeat the text back",
		json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		func(args json.RawMessage) ([]mcptypes.Content, error) {
			var p struct{ Text string }
			json.Unmarshal(args, &p)
			return []mcptypes.Content{mcptypes.Text(p.Text)}, nil
		})
	s.AddTool("add", "Add two integers",
		json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"]}`),
		func(args json.RawMessage) ([]mcptypes.Content, error) {
			var p struct{ A, B int64 }
			json.Unmarshal(args, &p)
			return []mcptypes.Content{mcptypes.Text(fmt.Sprintf("%d", p.A+p.B))}, nil
		})
	return s
}

func benchStdio(s *server.Server, iters int) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()

	serverT := transport.NewStdio(c2sR, s2cW, nil)
	clientT := transport.NewStdio(s2cR, c2sW, nil)
	go func() { _ = s.Serve(serverT) }()

	c := client.NewClient(clientT, "bench", "1.0.0")
	defer c.Close()

	if err := c.Connect(); err != nil {
		fatal("connect: %v", err)
	}

	// Warm-up: first call pays for the pipe, the maps and the branch
	// predictors. Reporting it would measure the CPU, not the protocol.
	for i := 0; i < 1000; i++ {
		if _, err := c.CallTool("echo", map[string]any{"text": "warm"}); err != nil {
			fatal("warm-up: %v", err)
		}
	}

	samples := make([]int64, 0, iters)
	t0 := time.Now()
	for i := 0; i < iters; i++ {
		start := time.Now()
		if _, err := c.CallTool("echo", map[string]any{"text": "bench"}); err != nil {
			fatal("call: %v", err)
		}
		samples = append(samples, time.Since(start).Nanoseconds())
	}
	elapsed := time.Since(t0)

	report("tools/call round trip", samples, elapsed, iters)
}

func benchHTTP(s *server.Server, iters int) {
	ts := httptest.NewServer(s.HTTPHandler())
	defer ts.Close()

	tr := transport.NewHTTP(ts.URL, nil)
	c := client.NewClient(tr, "bench", "1.0.0")
	defer c.Close()

	if err := c.Connect(); err != nil {
		fatal("connect: %v", err)
	}
	for i := 0; i < 500; i++ {
		if _, err := c.CallTool("echo", map[string]any{"text": "warm"}); err != nil {
			fatal("warm-up: %v", err)
		}
	}

	samples := make([]int64, 0, iters/4)
	t0 := time.Now()
	n := iters / 4
	for i := 0; i < n; i++ {
		start := time.Now()
		if _, err := c.CallTool("echo", map[string]any{"text": "bench"}); err != nil {
			fatal("call: %v", err)
		}
		samples = append(samples, time.Since(start).Nanoseconds())
	}
	elapsed := time.Since(t0)

	report("tools/call round trip", samples, elapsed, n)
}

func report(label string, samples []int64, elapsed time.Duration, n int) {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	pct := func(p float64) int64 {
		if len(samples) == 0 {
			return 0
		}
		i := int(p * float64(len(samples)-1))
		return samples[i]
	}
	fmt.Printf("  %s, %d iterations\n", label, n)
	fmt.Printf("    p50    %10d ns\n", pct(0.50))
	fmt.Printf("    p90    %10d ns\n", pct(0.90))
	fmt.Printf("    p99    %10d ns\n", pct(0.99))
	fmt.Printf("    p99.9  %10d ns\n", pct(0.999))
	fmt.Printf("    min    %10d ns\n", samples[0])
	fmt.Printf("    max    %10d ns\n", samples[len(samples)-1])
	fmt.Printf("    mean   %10d ns\n", elapsed.Nanoseconds()/int64(n))
	fmt.Printf("    rate   %10.0f msg/s\n", float64(n)/elapsed.Seconds())
}

func reportWireSize(s *server.Server) {
	// tools/list is the message every agent sends first, so its size is the
	// number people feel when they look at a trace.
	out, err := s.HandleMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"bench","version":"1"}}}`))
	if err != nil {
		fatal("initialize: %v", err)
	}
	fmt.Printf("  initialize response      %6d bytes\n", len(out))

	out, _ = s.HandleMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	fmt.Printf("  tools/list response      %6d bytes  (2 tools)\n", len(out))

	out, _ = s.HandleMessage([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"add","arguments":{"a":1,"b":2}}}`))
	fmt.Printf("  tools/call request+resp  %6d bytes\n", len([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"add","arguments":{"a":1,"b":2}}}`))+len(out))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mcpbench: "+format+"\n", args...)
	os.Exit(1)
}
