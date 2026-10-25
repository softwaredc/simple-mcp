// Command mcpserver is a working MCP server you can point any client at.
//
// It speaks both transports:
//
//	mcpserver                      # stdio (default): what Claude Desktop wants
//	mcpserver -http :8080          # Streamable HTTP on :8080
//
// Over stdio it reads JSON-RPC from stdin and writes to stdout. Anything you
// print yourself goes to stderr, because stdout belongs to the protocol.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/softwaredc/simple-mcp/go/mcptypes"
	"github.com/softwaredc/simple-mcp/go/server"
	"github.com/softwaredc/simple-mcp/go/transport"
)

func main() {
	httpAddr := flag.String("http", "", "serve Streamable HTTP on this address instead of stdio")
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("mcpserver: ")

	s := server.NewServer("simple-mcp-demo", "1.0.0")
	s.SetInstructions("Demo server. Use echo to repeat text, add to sum two integers, " +
		"and get_weather for a fixed mock forecast. Data is synthetic; see README.")

	registerTools(s)

	if *httpAddr != "" {
		serveHTTP(s, *httpAddr)
		return
	}
	serveStdio(s)
}

// registerTools adds the demo tools.
//
// Note what the descriptions do and do not say. "Repeat the given text back
// verbatim" tells the model everything it needs. A description like "echo
// utility" would get called with the wrong argument name half the time.
func registerTools(s *server.Server) {
	s.AddTool("echo", "Repeat the given text back verbatim. Use to verify connectivity.",
		mustSchema(`{"type":"object","properties":{"text":{"type":"string","description":"the text to repeat"}},"required":["text"]}`),
		func(args json.RawMessage) ([]mcptypes.Content, error) {
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			return []mcptypes.Content{mcptypes.Text(p.Text)}, nil
		})

	s.AddTool("add", "Add two integers and return the sum. Use for arithmetic, not for string concatenation.",
		mustSchema(`{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"]}`),
		func(args json.RawMessage) ([]mcptypes.Content, error) {
			var p struct {
				A int64 `json:"a"`
				B int64 `json:"b"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			return []mcptypes.Content{mcptypes.Text(strconv.FormatInt(p.A+p.B, 10))}, nil
		})

	// get_weather is deliberately fake. A demo that calls a real API needs a
	// key, breaks offline, and makes rate limits into a teaching problem.
	// Mock data keeps the protocol the thing being demonstrated.
	s.AddTool("get_weather", "Return a mock forecast for a city. The data is fixed and synthetic; it exists to show tool shape, not meteorology.",
		mustSchema(`{"type":"object","properties":{"city":{"type":"string","description":"city name, e.g. Beijing"},"unit":{"type":"string","enum":["celsius","fahrenheit"],"description":"temperature unit"}},"required":["city"]}`),
		func(args json.RawMessage) ([]mcptypes.Content, error) {
			var p struct {
				City string `json:"city"`
				Unit string `json:"unit"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			if p.City == "" {
				return nil, fmt.Errorf("city is required")
			}
			if p.Unit == "" {
				p.Unit = "celsius"
			}
			return []mcptypes.Content{mcptypes.Text(mockWeather(p.City, p.Unit))}, nil
		})

	s.AddResource("file:///demo/readme",
		"Demo readme", "A short description of this demo server", "text/plain",
		func() (string, error) {
			return "simple-mcp demo server.\nTools: echo, add, get_weather.\n", nil
		})

	s.AddPrompt("code_review", "Review a snippet for correctness and clarity",
		[]mcptypes.PromptArgument{
			{Name: "language", Description: "programming language", Required: true},
			{Name: "code", Description: "the snippet to review", Required: true},
		},
		func(args map[string]string) ([]mcptypes.PromptMessage, error) {
			return []mcptypes.PromptMessage{{
				Role: "user",
				Content: mcptypes.Text(fmt.Sprintf(
					"Review this %s snippet for correctness and clarity. "+
						"Point out bugs first, then style.\n\n```%s\n%s\n```",
					args["language"], args["language"], args["code"])),
			}}, nil
		})
}

func mockWeather(city, unit string) string {
	// Fixed table so the output is deterministic and testable.
	base := map[string]int{"beijing": 22, "shanghai": 25, "shenzhen": 29, "hangzhou": 24}
	c, ok := base[strings.ToLower(city)]
	if !ok {
		c = 20
	}
	if unit == "fahrenheit" {
		return fmt.Sprintf("%s: %d F (mock, clear)", city, c*9/5+32)
	}
	return fmt.Sprintf("%s: %d C (mock, clear)", city, c)
}

func serveStdio(s *server.Server) {
	// stdout is the protocol channel. Everything diagnostic goes to stderr;
	// one stray log line on stdout breaks every client that parses it.
	t := transport.NewStdio(os.Stdin, os.Stdout, nil)
	if err := s.Serve(t); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func serveHTTP(s *server.Server, addr string) {
	mux := http.NewServeMux()
	mux.Handle("/mcp", s.HTTPHandler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	log.Printf("listening on %s (Streamable HTTP at /mcp)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("http: %v", err)
	}
}

func mustSchema(raw string) json.RawMessage {
	var probe any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		panic("demo schema is not valid JSON: " + err.Error())
	}
	return json.RawMessage(raw)
}
