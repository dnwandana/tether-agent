// Command echoserver is a small stdio MCP server for the tests of
// internal/mcp. It has the tools echo, env, sleep, fail, and spawn.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoIn struct {
	Text string `json:"text"`
}

type sleepIn struct {
	Seconds float64 `json:"seconds"`
}

type none struct{}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "echoserver", Version: "v0.0.1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Returns the text."},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			return text(in.Text), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "env", Description: "Returns the environment."},
		func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, any, error) {
			return text(strings.Join(os.Environ(), "\n")), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "sleep", Description: "Waits for some seconds."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sleepIn) (*mcp.CallToolResult, any, error) {
			select {
			case <-time.After(time.Duration(in.Seconds * float64(time.Second))):
			case <-ctx.Done():
			}
			return text("slept"), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "Fails with a credential in the text."},
		func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, any, error) {
			return nil, nil, errors.New("upstream refused Authorization: Bearer sk-abcdefghijklmnopqrstuv")
		})
	mcp.AddTool(s, &mcp.Tool{Name: "spawn", Description: "Starts a child process and returns its pid."},
		func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, any, error) {
			c := exec.Command("sleep", "60")
			if err := c.Start(); err != nil {
				return nil, nil, err
			}
			return text(strconv.Itoa(c.Process.Pid)), nil, nil
		})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Print(err)
	}
}
