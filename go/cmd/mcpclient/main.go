// Command mcpclient talks to an MCP server and prints what it learned.
//
// Over stdio it spawns the server as a child process, exactly the way Claude
// Desktop and most CLI agents do it: the client owns the pipe, and the
// server's stdin and stdout are the protocol channel.
//
//	mcpclient                          # spawn ./mcpserver and drive it
//	mcpclient -server /path/to/bin     # spawn a different server binary
//	mcpclient -http http://host:8080/mcp   # talk HTTP instead
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/softwaredc/simple-mcp/go/client"
	"github.com/softwaredc/simple-mcp/go/mcptypes"
	"github.com/softwaredc/simple-mcp/go/transport"
)

func main() {
	serverBin := flag.String("server", "./mcpserver", "path to the server binary for stdio mode")
	httpURL := flag.String("http", "", "Streamable HTTP endpoint; empty means spawn a stdio server")
	flag.Parse()

	c, cleanup, err := connect(*serverBin, *httpURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer cleanup()

	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
}

// connect builds a client over whichever transport was asked for.
func connect(serverBin, httpURL string) (*client.Client, func(), error) {
	if httpURL != "" {
		t := transport.NewHTTP(httpURL, nil)
		c := client.NewClient(t, "simple-mcp-client", "1.0.0")
		if err := c.Connect(); err != nil {
			t.Close()
			return nil, nil, err
		}
		return c, func() { c.Close() }, nil
	}

	cmd := exec.Command(serverBin)
	cmd.Stderr = os.Stderr // server diagnostics flow to our stderr, not into the pipe
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("cannot start %s: %w (build it first: go build -o mcpserver ./cmd/mcpserver)", serverBin, err)
	}

	t := transport.NewStdio(stdout, stdin, nil)
	c := client.NewClient(t, "simple-mcp-client", "1.0.0")
	if err := c.Connect(); err != nil {
		cmd.Process.Kill()
		return nil, nil, err
	}
	return c, func() {
		c.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}, nil
}

func run(c *client.Client) error {
	fmt.Printf("connected to %s %s (protocol %s)\n",
		c.ServerInfo().Name, c.ServerInfo().Version, c.ProtocolVersion())
	if ins := c.Instructions(); ins != "" {
		fmt.Printf("instructions: %s\n", ins)
	}

	tools, err := c.ListTools()
	if err != nil {
		return err
	}
	fmt.Printf("\n%d tools:\n", len(tools))
	for _, t := range tools {
		fmt.Printf("  %-14s %s\n", t.Name, t.Description)
	}

	resources, err := c.ListResources()
	if err != nil {
		return err
	}
	fmt.Printf("\n%d resources:\n", len(resources))
	for _, r := range resources {
		fmt.Printf("  %-28s %s\n", r.URI, r.Name)
	}

	prompts, err := c.ListPrompts()
	if err != nil {
		return err
	}
	fmt.Printf("\n%d prompts:\n", len(prompts))
	for _, p := range prompts {
		fmt.Printf("  %-14s %s\n", p.Name, p.Description)
	}

	// Call every tool once, so the demo shows real results rather than names.
	fmt.Println("\ncalling tools:")
	for _, t := range tools {
		args := demoArgs(t.Name)
		res, err := c.CallTool(t.Name, args)
		if err != nil {
			fmt.Printf("  %-14s protocol error: %v\n", t.Name, err)
			continue
		}
		if res.IsError {
			fmt.Printf("  %-14s tool error: %s\n", t.Name, firstText(res))
			continue
		}
		fmt.Printf("  %-14s %s\n", t.Name, firstText(res))
	}

	// Show a tool-level failure and a protocol error side by side, because the
	// difference is the single most misunderstood part of MCP.
	fmt.Println("\nerror taxonomy:")
	res, err := c.CallTool("get_weather", map[string]any{"city": 12345})
	fmt.Printf("  wrong argument type -> protocol error: %v\n", err)
	if res != nil && res.IsError {
		fmt.Printf("  tool-level failure  -> isError result:  %s\n", firstText(res))
	}

	// Read a resource and render a prompt.
	if len(resources) > 0 {
		contents, err := c.ReadResource(resources[0].URI)
		if err != nil {
			return err
		}
		fmt.Printf("\nresource %s:\n", resources[0].URI)
		for _, ct := range contents {
			fmt.Printf("  %s\n", strings.ReplaceAll(ct.Text, "\n", "\n  "))
		}
	}
	if len(prompts) > 0 {
		msgs, err := c.GetPrompt(prompts[0].Name, map[string]string{
			"language": "go",
			"code":     "func f() int { return 1 }",
		})
		if err != nil {
			return err
		}
		fmt.Printf("\nprompt %s rendered %d message(s):\n", prompts[0].Name, len(msgs))
		for _, m := range msgs {
			fmt.Printf("  [%s] %s\n", m.Role, truncate(m.Content.Text, 100))
		}
	}

	return nil
}

func demoArgs(name string) any {
	switch name {
	case "echo":
		return map[string]any{"text": "hello from the client"}
	case "add":
		return map[string]any{"a": 40, "b": 2}
	case "get_weather":
		return map[string]any{"city": "Beijing", "unit": "celsius"}
	default:
		return map[string]any{}
	}
}

func firstText(res *mcptypes.CallToolResult) string {
	for _, c := range res.Content {
		if c.Kind == mcptypes.KindText {
			return truncate(c.Text, 120)
		}
	}
	return "(no text content)"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
