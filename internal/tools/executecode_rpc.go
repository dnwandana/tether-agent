package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// ScriptCallableTools holds the tools that an execute_code script can call. It
// is the Hermes SANDBOX_ALLOWED_TOOLS list with the tether names.
var ScriptCallableTools = []string{"web_search", "web_fetch", "read_file", "write_file", "patch", "terminal"}

// The limits of the script RPC.
const (
	defaultRPCMaxCalls = 50
	rpcMaxLine         = 4 << 20
	rpcSockName        = "rpc.sock"
)

// RPCServer serves the tool calls of one execute_code script over a Unix
// socket. The protocol is one JSON object per line. A request is
// {"id":N,"tool":"web_fetch","args":{...}}. A response is
// {"id":N,"ok":true,"result":"..."} or {"id":N,"ok":false,"error":"..."}.
// Each call goes through Registry.Dispatch, so the approval gate and the
// protected paths apply the same as for a model call.
type RPCServer struct {
	Reg *Registry
	// Env is the Env of the execute_code call. The script calls use it.
	Env *Env
	// Visible holds the tools of the session. A script can call a tool only
	// when it is in ScriptCallableTools and in Visible.
	Visible map[string]bool
	// MaxCalls limits the calls of one script. Zero or less means 50.
	MaxCalls int
	// Ctx is the context of the execute_code call. Nil means
	// context.Background. Stop also cancels the calls.
	Ctx context.Context

	mu    sync.Mutex // serializes the calls, so one call runs at a time
	calls int
}

type rpcRequest struct {
	ID   int64           `json:"id"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type rpcResponse struct {
	ID     int64  `json:"id"`
	OK     bool   `json:"ok"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// Listen starts the server on dir/rpc.sock with mode 0600. dir must be a
// private folder. stop closes the listener and the open connections, waits
// for the calls in progress, and removes the socket file. stop is safe to
// call more than once.
func (s *RPCServer) Listen(dir string) (sockPath string, stop func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("execute_code: cannot create the socket folder %s: %w", dir, err)
	}
	sockPath = filepath.Join(dir, rpcSockName)
	if err := os.Remove(sockPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", nil, fmt.Errorf("execute_code: cannot remove the old socket %s: %w", sockPath, err)
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return "", nil, fmt.Errorf("execute_code: cannot listen on %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		os.Remove(sockPath)
		return "", nil, fmt.Errorf("execute_code: cannot set the mode of %s: %w", sockPath, err)
	}

	parent := s.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		conns = map[net.Conn]bool{}
		done  bool
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if done {
				mu.Unlock()
				c.Close()
				return
			}
			conns[c] = true
			wg.Add(1)
			mu.Unlock()
			go func() {
				defer wg.Done()
				s.serve(ctx, c)
				mu.Lock()
				delete(conns, c)
				mu.Unlock()
				c.Close()
			}()
		}
	}()

	stop = sync.OnceFunc(func() {
		mu.Lock()
		done = true
		for c := range conns {
			c.Close()
		}
		mu.Unlock()
		cancel()
		ln.Close()
		wg.Wait()
		os.Remove(sockPath)
	})
	return sockPath, stop, nil
}

// serve reads the requests of one connection until it closes. A bad line
// gets an error response, and the connection stays open. A line over 4 MB
// ends the connection.
func (s *RPCServer) serve(ctx context.Context, c net.Conn) {
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), rpcMaxLine)
	enc := json.NewEncoder(c)
	enc.SetEscapeHTML(false)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		var resp rpcResponse
		if err := json.Unmarshal(line, &req); err != nil {
			resp = rpcResponse{Error: "invalid request: " + err.Error()}
		} else {
			resp = s.call(ctx, req)
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
	if errors.Is(sc.Err(), bufio.ErrTooLong) {
		_ = enc.Encode(rpcResponse{Error: fmt.Sprintf("invalid request: the line is longer than %d bytes", rpcMaxLine)})
	}
}

// call checks and runs one request.
func (s *RPCServer) call(ctx context.Context, req rpcRequest) rpcResponse {
	resp := rpcResponse{ID: req.ID}
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := s.MaxCalls
	if limit <= 0 {
		limit = defaultRPCMaxCalls
	}
	if s.calls >= limit {
		resp.Error = fmt.Sprintf("tool call limit (%d) reached", limit)
		return resp
	}
	s.calls++
	if !slices.Contains(ScriptCallableTools, req.Tool) {
		resp.Error = fmt.Sprintf("tool %q is not callable from execute_code; the callable tools are %v", req.Tool, ScriptCallableTools)
		return resp
	}
	if s.Reg == nil {
		resp.Error = "no tools are available"
		return resp
	}
	if err := ctx.Err(); err != nil {
		resp.Error = "the execute_code call ended: " + err.Error()
		return resp
	}
	args := req.Args
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	// Mark the call on a copy, because other calls share s.Env. The copy keeps the
	// same Values map, so the read tracker still works.
	var env Env
	if s.Env != nil {
		env = *s.Env
	}
	env.ScriptCall = true
	res := s.Reg.Dispatch(ctx, req.Tool, args, &env, s.Visible)
	if res.IsError {
		resp.Error = res.Text
		return resp
	}
	resp.OK, resp.Result = true, res.Text
	return resp
}
