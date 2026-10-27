package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/dnwandana/tether-agent/internal/app"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// toolsDocConfig turns on every built-in tool: a vision provider for view_image and a
// backend for each media tool. The provider gets no call.
const toolsDocConfig = `providers:
  primary:
    format: openai
    base_url: http://127.0.0.1:9/v1
    api_key: docs
    model: docs
    vision: true
media:
  image:
    backend: endpoint
    provider: primary
pricing:
  sources: []
`

// ToolsDocRegistry returns the registry of docs/TOOLS.md. It builds the app of the
// gateway role in a temporary data folder with a config that turns on every built-in
// tool. It also adds run_script when python3 is not on PATH, so the doc does not depend
// on the host. It starts no MCP server. Call done to close the app and to remove the
// folder.
func ToolsDocRegistry(ctx context.Context) (r *tools.Registry, done func(), err error) {
	dir, err := os.MkdirTemp("", "tether-tools-doc")
	if err != nil {
		return nil, nil, fmt.Errorf("gen-tools-doc: cannot make a temporary folder: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(toolsDocConfig), 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("gen-tools-doc: cannot write the config: %w", err)
	}
	a, err := app.New(ctx, app.Options{DataDir: dir, LogStderr: true, LogWriter: io.Discard, Gateway: true})
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("gen-tools-doc: %w", err)
	}
	if _, ok := a.Registry.Get("run_script"); !ok {
		c := a.Cfg.CodeExecution
		a.RegisterTool(tools.RunScriptTool{
			Reg:         a.Registry,
			ScratchRoot: filepath.Join(dir, "cache"),
			TimeoutS:    c.TimeoutS,
			MaxCalls:    c.MaxToolCalls,
			MaxStdout:   c.MaxStdoutBytes,
			Python:      "python3",
			Passthrough: func(*tools.Env) []string { return slices.Clone(a.Cfg.Terminal.EnvPassthrough) },
		})
	}
	return a.Registry, func() {
		a.Close()
		os.RemoveAll(dir)
	}, nil
}
