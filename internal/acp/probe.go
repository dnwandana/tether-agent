package acp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dnwandana/tether-agent/internal/config"
)

// ProbeResult is the result of one Probe. Probe fills the field of each step that passed.
type ProbeResult struct {
	Program         string
	NodeVersion     string
	ProtocolVersion int
	AgentVersion    string
	SessionOK       bool
	NotLoggedIn     bool
}

// probeClientVersion is the clientInfo version that Probe sends.
const probeClientVersion = "doctor"

// shebangMax is the number of bytes that isNodeScript reads.
const shebangMax = 256

// Probe starts the adapter of one agent, does the handshake, and starts and closes one
// session in a new temp folder. tether doctor uses it. It returns the result of the steps
// that passed and the error of the first step that failed.
func Probe(ctx context.Context, agentID string, a config.ACPAgent, dotenv map[string]string) (ProbeResult, error) {
	var r ProbeResult
	if len(a.Command) == 0 {
		return r, fmt.Errorf("acp: agent %s has no command", agentID)
	}
	env := BuildEnv(os.Environ(), dotenv, a)
	prog, err := FindProgram(env, a.Command[0])
	if err != nil {
		return r, err
	}
	r.Program = prog
	if isNodeScript(prog) {
		r.NodeVersion = nodeVersion(ctx, env)
	}

	dir, err := os.MkdirTemp("", "tether-acp-doctor")
	if err != nil {
		return r, err
	}
	defer os.RemoveAll(dir)
	c, err := Start(ctx, StartOptions{
		AgentID:       agentID,
		Agent:         a,
		DotEnv:        dotenv,
		Dir:           dir,
		ClientVersion: probeClientVersion,
		Log:           slog.New(slog.DiscardHandler),
	})
	if err != nil {
		return r, err
	}
	// This defer runs before the RemoveAll defer, so the process stops before the folder goes.
	defer func() { _ = c.Stop() }()
	caps := c.Capabilities()
	r.ProtocolVersion = caps.ProtocolVersion
	r.AgentVersion = caps.AgentVersion

	info, err := c.NewSession(ctx, dir)
	if err != nil {
		var nl *NotLoggedInError
		r.NotLoggedIn = errors.As(err, &nl)
		return r, err
	}
	r.SessionOK = true
	if caps.Close {
		// A failed close does not matter: the stop ends the session.
		_ = c.CloseSession(ctx, info.ID)
	}
	return r, nil
}

// nodeVersion returns the output of "node --version" with env, or "" when it fails. The
// handshake then shows the cause of the failure.
func nodeVersion(ctx context.Context, env []string) string {
	node, err := FindProgram(env, "node")
	if err != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, node, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isNodeScript reports whether the first line of the file at path is a "#!" line with a
// word whose base name is node, for example "#!/usr/bin/env node".
func isNodeScript(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, shebangMax)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	line, _, _ := bytes.Cut(buf[:n], []byte("\n"))
	rest, ok := bytes.CutPrefix(line, []byte("#!"))
	if !ok {
		return false
	}
	for w := range strings.FieldsSeq(string(rest)) {
		if filepath.Base(w) == "node" {
			return true
		}
	}
	return false
}
