package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// echoTool is a tool with any name that returns its raw arguments.
type echoTool struct{ name string }

func (e echoTool) Name() string       { return e.name }
func (e echoTool) Toolset() string    { return SetWeb }
func (e echoTool) ParallelSafe() bool { return true }
func (e echoTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: e.name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (e echoTool) Run(_ context.Context, args json.RawMessage, _ *Env) (string, error) {
	if strings.Contains(string(args), "fail") {
		return "", toolErr("echo failed")
	}
	return e.name + ":" + string(args), nil
}

// echoAs returns an echo tool with the given name.
func echoAs(name string) Tool { return echoTool{name: name} }

// regWith returns a registry with the tools.
func regWith(ts ...Tool) *Registry {
	r := NewRegistry()
	for _, t := range ts {
		r.Register(t)
	}
	return r
}

// vis returns a visible map with the names.
func vis(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// rpcResp is one response line of the RPC server.
type rpcResp struct {
	ID     int64  `json:"id"`
	OK     bool   `json:"ok"`
	Result string `json:"result"`
	Error  string `json:"error"`
}

// rpcConn opens a connection to sock.
func rpcConn(t *testing.T, sock string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	return c, bufio.NewReader(c)
}

// rpcSend sends one request line on c and reads one response line.
func rpcSend(t *testing.T, c net.Conn, r *bufio.Reader, req string) rpcResp {
	t.Helper()
	if _, err := c.Write([]byte(req + "\n")); err != nil {
		t.Fatal(err)
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var resp rpcResp
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	return resp
}

// rpc sends one request on a new connection and returns the response.
func rpc(t *testing.T, sock, req string) rpcResp {
	t.Helper()
	c, r := rpcConn(t, sock)
	return rpcSend(t, c, r, req)
}

// listenRPC starts s in a new folder and stops it at the end of the test.
func listenRPC(t *testing.T, s *RPCServer) string {
	t.Helper()
	sock, stop, err := s.Listen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return sock
}

func TestRPCDispatchesAllowedTool(t *testing.T) {
	s := &RPCServer{Reg: regWith(echoAs("web_fetch")), Env: &Env{}, Visible: vis("web_fetch"), MaxCalls: 50}
	sock := listenRPC(t, s)
	resp := rpc(t, sock, `{"id":1,"tool":"web_fetch","args":{"urls":["x"]}}`)
	if !resp.OK || resp.ID != 1 || resp.Result != `web_fetch:{"urls":["x"]}` {
		t.Fatal(resp)
	}
}

func TestRPCToolErrorIsNotOK(t *testing.T) {
	s := &RPCServer{Reg: regWith(echoAs("web_fetch")), Env: &Env{}, Visible: vis("web_fetch")}
	sock := listenRPC(t, s)
	resp := rpc(t, sock, `{"id":7,"tool":"web_fetch","args":{"x":"fail"}}`)
	if resp.OK || resp.ID != 7 || resp.Error != "echo failed" {
		t.Fatal(resp)
	}
}

func TestRPCRefusesNonCallable(t *testing.T) {
	s := &RPCServer{Reg: regWith(echoAs("memory")), Env: &Env{}, Visible: vis("memory"), MaxCalls: 50}
	sock := listenRPC(t, s)
	resp := rpc(t, sock, `{"id":2,"tool":"memory","args":{}}`)
	if resp.OK || !strings.Contains(resp.Error, "not callable from execute_code") {
		t.Fatal(resp)
	}
}

func TestRPCRefusesHidden(t *testing.T) {
	s := &RPCServer{Reg: regWith(echoAs("terminal")), Env: &Env{}, Visible: vis(), MaxCalls: 50}
	sock := listenRPC(t, s)
	resp := rpc(t, sock, `{"id":3,"tool":"terminal","args":{"command":"ls"}}`)
	if resp.OK || resp.Result != "" {
		t.Fatal(resp)
	}
}

func TestRPCBadRequest(t *testing.T) {
	s := &RPCServer{Reg: regWith(echoAs("web_fetch")), Env: &Env{}, Visible: vis("web_fetch")}
	sock := listenRPC(t, s)
	c, r := rpcConn(t, sock)
	if resp := rpcSend(t, c, r, `not json`); resp.OK || !strings.Contains(resp.Error, "invalid request") {
		t.Fatal(resp)
	}
	// The connection stays usable after a bad line.
	if resp := rpcSend(t, c, r, `{"id":4,"tool":"web_fetch"}`); !resp.OK || resp.Result != "web_fetch:{}" {
		t.Fatal(resp)
	}
}

func TestRPCCallLimit(t *testing.T) {
	s := &RPCServer{Reg: regWith(echoAs("web_fetch")), Env: &Env{}, Visible: vis("web_fetch"), MaxCalls: 2}
	sock := listenRPC(t, s)
	c, r := rpcConn(t, sock)
	for i := range 2 {
		if resp := rpcSend(t, c, r, `{"id":1,"tool":"web_fetch","args":{}}`); !resp.OK {
			t.Fatal(i, resp)
		}
	}
	// The limit holds across connections.
	if resp := rpc(t, sock, `{"id":3,"tool":"web_fetch","args":{}}`); resp.OK || resp.Error != "tool call limit (2) reached" {
		t.Fatal(resp)
	}
}

func TestRPCConcurrentConnections(t *testing.T) {
	s := &RPCServer{Reg: regWith(echoAs("web_fetch")), Env: &Env{}, Visible: vis("web_fetch"), MaxCalls: 50}
	sock := listenRPC(t, s)
	held, _ := rpcConn(t, sock) // an idle connection must not block the others
	_ = held
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("unix", sock)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(5 * time.Second))
			c.Write([]byte(`{"id":1,"tool":"web_fetch","args":{}}` + "\n"))
			if _, err := bufio.NewReader(c).ReadBytes('\n'); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestSocketPermissions0600(t *testing.T) {
	s := &RPCServer{Reg: regWith(echoAs("web_fetch")), Env: &Env{}, Visible: vis("web_fetch")}
	sock, stop, err := s.Listen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatal(fi.Mode(), err)
	}
	stop()
	stop() // a second stop is safe
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("stop must remove the socket file")
	}
	if _, err := net.Dial("unix", sock); err == nil {
		t.Fatal("the server must not accept after stop")
	}
}

func TestStopClosesOpenConnections(t *testing.T) {
	s := &RPCServer{Reg: regWith(echoAs("web_fetch")), Env: &Env{}, Visible: vis("web_fetch")}
	sock, stop, err := s.Listen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, r := rpcConn(t, sock)
	rpcSend(t, c, r, `{"id":1,"tool":"web_fetch"}`)
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stop must not wait for an idle client")
	}
	if _, err := r.ReadBytes('\n'); err == nil {
		t.Fatal("the connection must be closed")
	}
}

func TestScriptCallableToolsList(t *testing.T) {
	want := []string{"web_search", "web_fetch", "read_file", "write_file", "patch", "terminal"}
	if strings.Join(ScriptCallableTools, ",") != strings.Join(want, ",") {
		t.Fatal(ScriptCallableTools)
	}
}

func TestRPCUnknownArgsNoNoteOnSuccess(t *testing.T) {
	s := &RPCServer{Reg: regWith(fakeTool{name: "web_fetch", out: "ok", params: abSchema}), Env: &Env{}, Visible: vis("web_fetch"), MaxCalls: 50}
	resp := rpc(t, listenRPC(t, s), `{"id":1,"tool":"web_fetch","args":{"x":1}}`)
	if !resp.OK || resp.Result != "ok" {
		t.Fatalf("response = %+v", resp)
	}
	if s.Env.ScriptCall {
		t.Fatal("the RPC server changed the shared Env")
	}
}

func TestRPCUnknownArgsNoteOnError(t *testing.T) {
	s := &RPCServer{Reg: regWith(fakeTool{name: "web_fetch", err: toolErr("boom"), params: abSchema}), Env: &Env{}, Visible: vis("web_fetch"), MaxCalls: 50}
	resp := rpc(t, listenRPC(t, s), `{"id":1,"tool":"web_fetch","args":{"x":1}}`)
	if resp.OK || !strings.Contains(resp.Error, "Tether ignored these unknown arguments: x.") {
		t.Fatalf("response = %+v", resp)
	}
}
