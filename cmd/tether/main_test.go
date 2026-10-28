package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/telegram"
)

func TestVersionSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"version"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "tether dev") {
		t.Fatalf("got %q", out.String())
	}
}

func TestUnknownSubcommandFails(t *testing.T) {
	for in, want := range map[string]string{
		"doctr": "unknown command: doctr\nDid you mean \"doctor\"? Run \"tether help\" for the commands.\n",
		"zzz":   "unknown command: zzz\nRun \"tether help\" for the commands.\n",
	} {
		var out, errb bytes.Buffer
		if code := run([]string{in}, &out, &errb); code != 2 {
			t.Fatalf("%s: exit %d", in, code)
		}
		if errb.String() != want || out.Len() != 0 {
			t.Fatalf("%s: stderr %q, stdout %q", in, errb.String(), out.String())
		}
	}
}

func TestDataDirOrder(t *testing.T) {
	t.Setenv("TETHER_HOME", "/tmp/th")
	if dataDir("/x") != "/x" {
		t.Fatal("flag must win")
	}
	if dataDir("") != "/tmp/th" {
		t.Fatal("env second")
	}
	t.Setenv("TETHER_HOME", "")
	home, _ := os.UserHomeDir()
	if dataDir("") != filepath.Join(home, ".tether") {
		t.Fatal("default")
	}
}

func TestSkillsResetSubcommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TETHER_HOME", home)
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "reset"}, &out, &errb); code != 0 {
		t.Fatalf("reset all: exit %d: %s", code, errb.String())
	}
	for _, name := range []string{"tether-memory", "tether-self", "tether-skills"} {
		if !strings.Contains(out.String(), name) {
			t.Fatalf("reset all output %q lacks %s", out.String(), name)
		}
	}
	p := filepath.Join(home, "skills", "tether-self", "SKILL.md")
	orig, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"skills", "reset", "tether-self"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, orig) {
		t.Fatal("not restored")
	}
	errb.Reset()
	if code := run([]string{"skills", "reset", "nope"}, &out, &errb); code != 1 {
		t.Fatalf("unknown skill: exit %d", code)
	}
	if !strings.Contains(errb.String(), "nope") {
		t.Fatal(errb.String())
	}
}

func TestSkillsResetDataDirFlag(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "--data-dir", dir, "reset", "tether-self"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "tether-self", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsListSubcommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TETHER_HOME", home)
	ext := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ext, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "notes", "SKILL.md"), []byte("---\nname: notes\ndescription: Notes. Use when taking notes.\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "skills", "mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "skills", "mine", "SKILL.md"), []byte("---\nname: mine\ndescription: My skill. Use when testing.\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := "providers:\n  primary:\n    format: openai\n    base_url: http://127.0.0.1:1\n    api_key: x\n    model: m\nskills:\n  external_dirs: [" + ext + "]\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	run([]string{"skills", "reset", "tether-self"}, &out, &errb)
	out.Reset()
	if code := run([]string{"skills", "list"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	lines := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			lines[f[0]] = f[1]
		}
	}
	want := map[string]string{"mine": "local", "notes": "external", "tether-self": "builtin"}
	for name, src := range want {
		if lines[name] != src {
			t.Fatalf("%s: got %q, want %q in:\n%s", name, lines[name], src, out.String())
		}
	}
	if !strings.Contains(out.String(), "My skill. Use when testing.") {
		t.Fatal(out.String())
	}
}

func TestSkillsBadUsage(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if run([]string{"skills"}, &out, &errb) != 2 || run([]string{"skills", "frob"}, &out, &errb) != 2 {
		t.Fatal("want exit 2")
	}
	if run([]string{"skills", "reset", "a", "b"}, &out, &errb) != 2 {
		t.Fatal("want exit 2 for two names")
	}
}

// seedStateDB writes one session with two messages to <dir>/state.db and returns its id.
func seedStateDB(t *testing.T, dir string) string {
	t.Helper()
	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	st := sessions.New(d)
	sess, err := st.Create(ctx, sessions.Session{Key: "cli:user", Source: "cli", Title: "trip plan"})
	if err != nil {
		t.Fatal(err)
	}
	st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "my key is hunter2-super-secret-value"}, sessions.MessageMeta{})
	st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleAssistant, Content: "ok"}, sessions.MessageMeta{Model: "m"})
	return sess.ID
}

func TestSessionsSubcommand(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("MY_SECRET=hunter2-super-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := seedStateDB(t, dir)
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "export", id, "--data-dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	p := filepath.Join(dir, "exports", id+".jsonl")
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	b, _ := os.ReadFile(p)
	if strings.Count(string(b), "\n") != 2 || strings.Contains(string(b), "hunter2-super-secret-value") {
		t.Fatal(string(b))
	}
	if !strings.Contains(out.String(), p) {
		t.Fatal(out.String())
	}

	alt := filepath.Join(t.TempDir(), "x.jsonl")
	out.Reset()
	if code := run([]string{"sessions", "--data-dir", dir, "export", id, "--out", alt}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(alt); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := run([]string{"sessions", "list", "--data-dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), id) || !strings.Contains(out.String(), "trip plan") || !strings.Contains(out.String(), "\t2") {
		t.Fatalf("%q", out.String())
	}
}

func TestSessionsSubcommandErrors(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "list", "--data-dir", dir}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "state.db") {
		t.Fatalf("no db: exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err == nil {
		t.Fatal("list must not create state.db")
	}
	seedStateDB(t, dir)
	errb.Reset()
	if code := run([]string{"sessions", "export", "nope", "--data-dir", dir}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "no session nope") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "exports", "nope.jsonl")); err == nil {
		t.Fatal("a failed export must not leave a file")
	}
	if run([]string{"sessions"}, &out, &errb) != 2 || run([]string{"sessions", "export"}, &out, &errb) != 2 || run([]string{"sessions", "frob"}, &out, &errb) != 2 {
		t.Fatal("want exit 2")
	}
}

func TestChatSubcommand(t *testing.T) {
	sse, err := os.ReadFile("../../internal/provider/testdata/openai_text_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(sse)
	}))
	defer srv.Close()
	t.Setenv("TETHER_HOME", t.TempDir())
	dir := t.TempDir()
	cfg := "providers:\n  primary:\n    format: openai\n    base_url: " + srv.URL + "\n    api_key: ${MY_KEY}\n    model: m\npricing:\n  sources: []\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("MY_KEY=sk-test-key-123456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin = strings.NewReader("hello\n")
	defer func() { stdin = os.Stdin }()
	var out, errb bytes.Buffer
	if code := run([]string{"chat", "--data-dir", dir, "--log-level", "debug"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "Hello, world.") || errb.Len() != 0 {
		t.Fatalf("out %q, err %q", out.String(), errb.String())
	}
	logb, err := os.ReadFile(filepath.Join(dir, "logs", "tether.log"))
	if err != nil || !strings.Contains(string(logb), "turn end") {
		t.Fatal(string(logb), err)
	}

	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	key := "cli:" + osUserName()
	sess, err := sessions.New(d).Active(context.Background(), key)
	if err != nil || sess == nil {
		t.Fatal(key, sess, err)
	}

	// --log-stderr sends the logs to stderr.
	stdin = strings.NewReader("again\n")
	out.Reset()
	if code := run([]string{"chat", "--data-dir", dir, "--log-stderr"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "turn end") {
		t.Fatalf("err %q", errb.String())
	}
}

func TestChatNeedsProvider(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	stdin = strings.NewReader("")
	defer func() { stdin = os.Stdin }()
	var out, errb bytes.Buffer
	if code := run([]string{"chat", "--data-dir", t.TempDir()}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "providers.primary") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

// seedPairing writes a pending pairing code for user 6 to <dir>/state.db and returns the code.
func seedPairing(t *testing.T, dir string) string {
	t.Helper()
	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p := &telegram.Pairing{DB: d}
	code, _, err := p.Request(context.Background(), "telegram", "6", "ana")
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestPairingSubcommand(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	dir := t.TempDir()
	code := seedPairing(t, dir)
	var out, errb bytes.Buffer
	if c := run([]string{"pairing", "--data-dir", dir, "list"}, &out, &errb); c != 0 {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	if !strings.Contains(out.String(), "ana") || !strings.Contains(out.String(), "pending") || strings.Contains(out.String(), code) {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	if c := run([]string{"pairing", "approve", "telegram", strings.ToLower(code), "--data-dir", dir}, &out, &errb); c != 0 {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	if !strings.Contains(out.String(), "Approved! User 6 on telegram") {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	if c := run([]string{"pairing", "list", "telegram", "--data-dir", dir}, &out, &errb); c != 0 {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	if !strings.Contains(out.String(), "6") || !strings.Contains(out.String(), "approved") {
		t.Fatalf("%q", out.String())
	}
	// The short form uses the telegram platform.
	out.Reset()
	if c := run([]string{"pairing", "revoke", "6", "--data-dir", dir}, &out, &errb); c != 0 {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	if !strings.Contains(out.String(), "Revoked access for user 6 on telegram") {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	if c := run([]string{"pairing", "list", "--data-dir", dir}, &out, &errb); c != 0 || !strings.Contains(out.String(), "No pairing data") {
		t.Fatalf("exit %d: %q", c, out.String())
	}
}

func TestPairingSubcommandErrors(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if c := run([]string{"pairing", "list", "--data-dir", dir}, &out, &errb); c != 1 || !strings.Contains(errb.String(), "state.db") {
		t.Fatalf("no db: exit %d: %s", c, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err == nil {
		t.Fatal("list must not create state.db")
	}
	seedPairing(t, dir)
	errb.Reset()
	if c := run([]string{"pairing", "approve", "AAAAAAAA", "--data-dir", dir}, &out, &errb); c != 1 || !strings.Contains(errb.String(), "not found or expired") {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	errb.Reset()
	if c := run([]string{"pairing", "revoke", "telegram", "99", "--data-dir", dir}, &out, &errb); c != 1 || !strings.Contains(errb.String(), "User 99 not found") {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	for i := 0; i < 5; i++ {
		run([]string{"pairing", "approve", "AAAAAAAA", "--data-dir", dir}, &out, &errb)
	}
	errb.Reset()
	if c := run([]string{"pairing", "approve", "AAAAAAAA", "--data-dir", dir}, &out, &errb); c != 1 || !strings.Contains(errb.String(), "locked") {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	if run([]string{"pairing"}, &out, &errb) != 2 || run([]string{"pairing", "approve"}, &out, &errb) != 2 ||
		run([]string{"pairing", "revoke", "a", "b", "c"}, &out, &errb) != 2 || run([]string{"pairing", "frob"}, &out, &errb) != 2 {
		t.Fatal("want exit 2")
	}
}

// writeChatConfig writes a config.yaml for a fake provider at url and no price sources.
func writeChatConfig(t *testing.T, dir, url string) {
	t.Helper()
	cfg := "providers:\n  primary:\n    format: openai\n    base_url: " + url + "\n    api_key: k\n    model: m\npricing:\n  sources: []\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGatewaySubcommandLockAndChat(t *testing.T) {
	sse, err := os.ReadFile("../../internal/provider/testdata/openai_text_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(sse)
	}))
	defer srv.Close()
	t.Setenv("TETHER_HOME", t.TempDir())
	t.Setenv("TETHER_PPROF", "")
	dir := t.TempDir()
	writeChatConfig(t, dir, srv.URL)

	gctx, cancel := context.WithCancel(context.Background())
	gatewayContext = func() context.Context { return gctx }
	defer func() { gatewayContext = context.Background }()
	exit := make(chan int, 1)
	var gout, gerr bytes.Buffer
	go func() { exit <- run([]string{"gateway", "--data-dir", dir}, &gout, &gerr) }()
	lock := filepath.Join(dir, "gateway.lock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(lock)
		if strings.TrimSpace(string(b)) == strconv.Itoa(os.Getpid()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the gateway did not take the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A second gateway refuses to start and names the PID.
	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "--data-dir", dir}, &out, &errb); code != 1 ||
		!strings.Contains(errb.String(), fmt.Sprintf("another gateway is running (pid %d)", os.Getpid())) {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	// A chat starts next to the gateway.
	stdin = strings.NewReader("hello\n")
	defer func() { stdin = os.Stdin }()
	out.Reset()
	errb.Reset()
	if code := run([]string{"chat", "--data-dir", dir}, &out, &errb); code != 0 || !strings.Contains(out.String(), "Hello, world.") {
		t.Fatalf("exit %d: %q %q", code, out.String(), errb.String())
	}

	cancel()
	select {
	case code := <-exit:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, gerr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the gateway did not stop")
	}
	logb, _ := os.ReadFile(filepath.Join(dir, "logs", "tether.log"))
	if !strings.Contains(string(logb), "gateway started") || !strings.Contains(string(logb), "gateway stopped") {
		t.Fatal(string(logb))
	}
}

func TestGatewayPprofMustBeLoopback(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	dir := t.TempDir()
	writeChatConfig(t, dir, "http://127.0.0.1:1")
	t.Setenv("TETHER_PPROF", "0.0.0.0:0")
	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "--data-dir", dir}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "loopback") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	srv, err := startPprof("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
}

func TestUsageSubcommand(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"usage", "--data-dir", dir}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "state.db") {
		t.Fatalf("no db: exit %d: %s", code, errb.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("providers:\n  primary:\n    format: openai\n    base_url: http://127.0.0.1:1\n    model: glm-4.7\n    flat_rate: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	st := sessions.New(d)
	ctx := context.Background()
	sess, _ := st.Create(ctx, sessions.Session{Key: "cli:user"})
	st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleAssistant, Content: "a"},
		sessions.MessageMeta{Model: "glm-4.7", Provider: "primary", Usage: provider.Usage{InputTokens: 3410000}})
	st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleAssistant, Content: "b"},
		sessions.MessageMeta{Model: "z-ai/glm-4.7", Provider: "openrouter", CostUSD: 0.07})
	d.Close()
	for _, args := range [][]string{{"usage", "--data-dir", dir}, {"usage", "30d", "--data-dir", dir}} {
		out.Reset()
		errb.Reset()
		if code := run(args, &out, &errb); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errb.String())
		}
		for _, want := range []string{"3.41M", "flat plan", "$0.07", "tok/s"} {
			if !strings.Contains(out.String(), want) {
				t.Fatal(args, want, out.String())
			}
		}
	}
	if code := run([]string{"usage", "1y", "--data-dir", dir}, &out, &errb); code != 2 {
		t.Fatal("want exit 2 for a bad range")
	}
}

func TestGatewayTelegramNeedsToken(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	t.Setenv("TETHER_PPROF", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	dir := t.TempDir()
	writeChatConfig(t, dir, "http://127.0.0.1:1")
	f, err := os.OpenFile(filepath.Join(dir, "config.yaml"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("telegram:\n  enabled: true\n")
	f.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "--data-dir", dir}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "bot token") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestGatewayStartsTelegramAndCron(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []string
	)
	bot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		calls = append(calls, method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":99,"is_bot":true,"username":"tether_bot"}}`))
		case "getUpdates":
			// A short long poll, so the loop does not spin.
			select {
			case <-time.After(50 * time.Millisecond):
			case <-r.Context().Done():
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer bot.Close()
	telegramBaseURL = bot.URL
	defer func() { telegramBaseURL = "" }()
	t.Setenv("TETHER_HOME", t.TempDir())
	t.Setenv("TETHER_PPROF", "")
	dir := t.TempDir()
	writeChatConfig(t, dir, "http://127.0.0.1:1")
	f, err := os.OpenFile(filepath.Join(dir, "config.yaml"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("telegram:\n  enabled: true\n  bot_token: T\n")
	f.Close()

	gctx, cancel := context.WithCancel(context.Background())
	gatewayContext = func() context.Context { return gctx }
	defer func() { gatewayContext = context.Background }()
	exit := make(chan int, 1)
	var gout, gerr bytes.Buffer
	go func() { exit <- run([]string{"gateway", "--data-dir", dir}, &gout, &gerr) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := strings.Join(calls, " ")
		mu.Unlock()
		if strings.Contains(got, "getMe") && strings.Contains(got, "setMyCommands") && strings.Contains(got, "getUpdates") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the Telegram channel did not start: ", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-exit:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, gerr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the gateway did not stop")
	}
	logb, _ := os.ReadFile(filepath.Join(dir, "logs", "tether.log"))
	if !strings.Contains(string(logb), "telegram channel started") || !strings.Contains(string(logb), `"channels":1`) {
		t.Fatal(string(logb))
	}
}

func TestDoctorExitCode(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	// A fake GitHub API: the test binary is a dev build, so the release check sends no request.
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.9.0","published_at":"2026-10-20T10:00:00Z","assets":[]}`))
	}))
	defer gh.Close()
	oldAPI := doctorReleaseAPI
	doctorReleaseAPI = gh.URL
	t.Cleanup(func() { doctorReleaseAPI = oldAPI })
	// No config: providers.primary is missing, so the config check fails.
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"doctor", "--data-dir", dir}, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "✗ config") || !strings.Contains(out.String(), "→ run tether setup") {
		t.Fatal(out.String())
	}
	if !strings.Contains(out.String(), "✓ release: dev build; the release check is off") {
		t.Fatal(out.String())
	}

	sse, err := os.ReadFile("../../internal/provider/testdata/openai_text_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(sse)
	}))
	defer srv.Close()
	writeChatConfig(t, dir, srv.URL)
	out.Reset()
	if code := run([]string{"doctor", "--data-dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "✓ provider primary: model m answered") {
		t.Fatal(out.String())
	}
}

func TestSetupSubcommand(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	sse, err := os.ReadFile("../../internal/provider/testdata/openai_text_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(sse)
	}))
	defer srv.Close()
	dir := t.TempDir()
	stdin = strings.NewReader("openai\n" + srv.URL + "\nsk-setup-key-123\nm\nn\nn\nn\n")
	defer func() { stdin = os.Stdin }()
	var out, errb bytes.Buffer
	if code := run([]string{"setup", "--data-dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil || !strings.Contains(string(cfg), "${PRIMARY_API_KEY}") {
		t.Fatal(string(cfg), err)
	}
	if !strings.Contains(out.String(), "The setup is complete") {
		t.Fatal(out.String())
	}
	// The input ends before the model: the setup fails.
	stdin = strings.NewReader("")
	if code := run([]string{"setup", "--data-dir", t.TempDir()}, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
}

func TestGenToolsDocSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"gen-tools-doc"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"# Tools", "## terminal", "### terminal", "### execute_code", "```json"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("no %q in the doc", want)
		}
	}
	if code := run([]string{"gen-tools-doc", "x"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

// stopEvents records the steps of a gateway stop in order.
type stopEvents struct {
	mu  sync.Mutex
	got []string
}

func (e *stopEvents) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, s)
}

func (e *stopEvents) list() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.got, " ")
}

func TestStopGatewayOrder(t *testing.T) {
	ev := &stopEvents{}
	never := make(chan struct{})
	done := make(chan struct{})
	close(done)
	var logb bytes.Buffer
	s := gatewayStop{
		drain:       time.Second,
		grace:       100 * time.Millisecond,
		stopReceive: func() { ev.add("receive") },
		waitTurns: func(d time.Duration) bool {
			ev.add("drain:" + d.String())
			return true
		},
		stopWork: func() { ev.add("work") },
		waits: []stopWait{
			{name: "channels", wait: waitChan(done)},
			{name: "cron", wait: waitChan(never)},
			{name: "batches", wait: func(time.Duration) bool { ev.add("batches"); return true }},
		},
		log: slog.New(slog.NewTextHandler(&logb, nil)),
	}
	start := time.Now()
	s.run()
	if got := ev.list(); got != "receive drain:1s work batches" {
		t.Fatal("the order must be: stop the receive loops, drain the turns, cancel the work, wait:", got)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatal("the wait for the cancelled work must keep its bound:", el)
	}
	if !strings.Contains(logb.String(), "part=cron") || strings.Contains(logb.String(), "part=channels") {
		t.Fatal(logb.String())
	}
}

func TestStopGatewayCancelsTurnsAfterTheDrain(t *testing.T) {
	ev := &stopEvents{}
	var logb bytes.Buffer
	s := gatewayStop{
		drain:       10 * time.Millisecond,
		grace:       10 * time.Millisecond,
		stopReceive: func() { ev.add("receive") },
		waitTurns:   func(time.Duration) bool { ev.add("drain"); return false },
		stopWork:    func() { ev.add("work") },
		log:         slog.New(slog.NewTextHandler(&logb, nil)),
	}
	s.run()
	if got := ev.list(); got != "receive drain work" || !strings.Contains(logb.String(), "did not end in time") {
		t.Fatal(got, logb.String())
	}
}

func TestGatewayFirstSignalRestoresTheDefault(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	t.Setenv("TETHER_PPROF", "")
	dir := t.TempDir()
	writeChatConfig(t, dir, "http://127.0.0.1:1")
	gctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	stoppedAtEnd := make(chan bool, 1)
	notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return gctx, func() {
			once.Do(func() {
				b, _ := os.ReadFile(filepath.Join(dir, "logs", "tether.log"))
				stoppedAtEnd <- strings.Contains(string(b), "gateway stopped")
			})
		}
	}
	defer func() { notifyContext = signal.NotifyContext }()
	exit := make(chan int, 1)
	var gout, gerr bytes.Buffer
	go func() { exit <- run([]string{"gateway", "--data-dir", dir}, &gout, &gerr) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(dir, "logs", "tether.log"))
		if strings.Contains(string(b), "gateway started") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the gateway did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-exit:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, gerr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the gateway did not stop")
	}
	if <-stoppedAtEnd {
		t.Fatal("the gateway must restore the default signal action at the first signal, so that a second signal ends the process")
	}
}

func TestCommandBadUsage(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	for _, name := range []string{"chat", "gateway", "doctor", "setup", "version"} {
		h, _ := findHelp(name)
		for _, c := range []struct{ arg, reason string }{
			{"--verbose", "unknown flag --verbose"},
			{"extra", "unexpected argument: extra"},
		} {
			var out, errb bytes.Buffer
			want := name + ": " + c.reason + "\nusage: " + h.Usage + "\nRun \"tether " + name + " help\" for the details.\n"
			if code := run([]string{name, c.arg}, &out, &errb); code != 2 || errb.String() != want {
				t.Errorf("%s %s: exit %d %q", name, c.arg, code, errb.String())
			}
		}
	}
}

func TestHelpFlagAfterOtherFlags(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"chat", "--data-dir", dir, "--help"}, &out, &errb); code != 0 || out.String() != commandHelp("chat") {
		t.Fatalf("exit %d %q", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err == nil {
		t.Fatal("help must not start the chat")
	}
}

func TestVersionTakesNoDataDir(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"version", "--data-dir", "d"}, &out, &errb)
	first, _, _ := strings.Cut(errb.String(), "\n")
	if code != 2 || first != "version: unknown flag --data-dir" {
		t.Fatalf("exit %d %q", code, errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout %q", out.String())
	}
}

func TestSubcommandBadUsage(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	for _, c := range []struct {
		args   []string
		reason string
	}{
		{[]string{"skills"}, "a command is needed: list, install, check, update, uninstall, or reset"},
		{[]string{"skills", "frob"}, "unknown command: frob"},
		{[]string{"skills", "list", "x"}, "unexpected argument: x"},
		{[]string{"skills", "reset", "a", "b"}, "unexpected argument: b"},
		{[]string{"sessions"}, "a command is needed: list or export"},
		{[]string{"sessions", "export"}, "export needs a session id"},
		{[]string{"sessions", "export", "a", "b"}, "unexpected argument: b"},
		{[]string{"sessions", "frob"}, "unknown command: frob"},
		{[]string{"pairing"}, "a command is needed: list, approve, or revoke"},
		{[]string{"pairing", "approve"}, "approve needs a code"},
		{[]string{"pairing", "revoke"}, "revoke needs a user id"},
		{[]string{"pairing", "revoke", "a", "b", "c"}, "unexpected argument: c"},
		{[]string{"pairing", "frob"}, "unknown command: frob"},
		{[]string{"usage", "90d"}, "unknown range 90d; use today, 7d, or 30d"},
		{[]string{"usage", "7d", "x"}, "unexpected argument: x"},
		{[]string{"sessions", "--verbose"}, "unknown flag --verbose"},
	} {
		name := c.args[0]
		h, _ := findHelp(name)
		want := name + ": " + c.reason + "\nusage: " + h.Usage + "\nRun \"tether " + name + " help\" for the details.\n"
		var out, errb bytes.Buffer
		if code := run(c.args, &out, &errb); code != 2 || errb.String() != want {
			t.Errorf("%v: exit %d %q", c.args, code, errb.String())
		}
	}
}

// withTerminal sets stdioIsTerminal and stdin for one test and restores them.
func withTerminal(t *testing.T, term bool, input string) {
	t.Helper()
	oldTerm, oldIn := stdioIsTerminal, stdin
	stdioIsTerminal = func() bool { return term }
	stdin = strings.NewReader(input)
	t.Cleanup(func() { stdioIsTerminal, stdin = oldTerm, oldIn })
}

// skillsHome makes a data folder with a minimal config, sets TETHER_HOME to
// it, and returns it. The config stops the config warning on stderr.
func skillsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("TETHER_HOME", home)
	cfg := "providers:\n  primary:\n    format: openai\n    base_url: http://127.0.0.1:1\n    api_key: x\n    model: m\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// writeTestSkill writes dir/SKILL.md for a skill with the given name.
func writeTestSkill(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: A demo skill. Use when testing.\n---\nSay hello.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsInstallLocal(t *testing.T) {
	home := skillsHome(t)
	src := t.TempDir()
	writeTestSkill(t, src, "demo")
	withTerminal(t, true, "y\n")
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "install", src}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "demo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Install "demo"? [y/N]`, "Installed demo.", "You are about to install a third-party skill."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestSkillsInstallNoTerminalNeedsYes(t *testing.T) {
	home := skillsHome(t)
	src := t.TempDir()
	writeTestSkill(t, src, "demo")
	withTerminal(t, false, "")
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "install", src}, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
	want := "skills install: no terminal for the confirm prompt. Run the command again with --yes to confirm.\n"
	if errb.String() != want {
		t.Fatalf("stderr %q", errb.String())
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "demo")); !os.IsNotExist(err) {
		t.Fatalf("the skill folder must not exist: %v", err)
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"skills", "install", src, "--yes"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "demo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsInstallCancelled(t *testing.T) {
	home := skillsHome(t)
	src := t.TempDir()
	writeTestSkill(t, src, "demo")
	withTerminal(t, true, "n\n")
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "install", src}, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "Cancelled.") || errb.Len() != 0 {
		t.Fatalf("stdout %q, stderr %q", out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "demo")); !os.IsNotExist(err) {
		t.Fatalf("the skill folder must not exist: %v", err)
	}
}

func TestSkillsInstallManySkills(t *testing.T) {
	skillsHome(t)
	// The space in the folder name needs quotes in the example line.
	src := filepath.Join(t.TempDir(), "my skills")
	writeTestSkill(t, filepath.Join(src, "a"), "a")
	writeTestSkill(t, filepath.Join(src, "b"), "b")
	withTerminal(t, true, "y\n")
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "install", src}, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{
		"skills install: the repository has 2 skills. Select one with --skill\n",
		"\n  a  a\n",
		"\n  b  b\n",
		"Example: tether skills install '" + src + "' --skill a\n",
	} {
		if !strings.Contains(errb.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, errb.String())
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"openai/skills":          "openai/skills",
		"https://github.com/o/r": "https://github.com/o/r",
		"~/my skills":            "'~/my skills'",
		"it's":                   `'it'\''s'`,
		"a;rm -rf x":             "'a;rm -rf x'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkillsInstallUsageErrors(t *testing.T) {
	skillsHome(t)
	withTerminal(t, true, "y\n")
	for _, c := range []struct {
		args   []string
		reason string
	}{
		{[]string{"skills", "install"}, "install needs a source"},
		{[]string{"skills", "install", "x", "y"}, "unexpected argument: y"},
		{[]string{"skills", "install", "https://github.com/o/r/tree/main/x", "--ref", "dev"}, ""},
		{[]string{"skills", "list", "--yes"}, "flag --yes is not used by skills list"},
		{[]string{"skills", "reset", "--force"}, "flag --force is not used by skills reset"},
		{[]string{"skills", "install", "x", "--available"}, "flag --available is not used by skills install"},
	} {
		var out, errb bytes.Buffer
		if code := run(c.args, &out, &errb); code != 2 {
			t.Fatalf("%v: exit %d: %s", c.args, code, errb.String())
		}
		if !strings.HasPrefix(errb.String(), "skills: "+c.reason) || !strings.Contains(errb.String(), "usage: tether skills") {
			t.Fatalf("%v: stderr %q", c.args, errb.String())
		}
	}
}

func TestSkillsListAvailable(t *testing.T) {
	skillsHome(t)
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "list", "--available"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if f := strings.Fields(lines[0]); len(f) != 4 || f[0] != "NAME" || f[1] != "INSTALLED" || f[2] != "REPO" || f[3] != "DESCRIPTION" {
		t.Fatalf("header %q", lines[0])
	}
	found := false
	for _, l := range lines[1:] {
		if f := strings.Fields(l); len(f) >= 3 && f[0] == "pdf" {
			found = f[1] == "-" && f[2] == "anthropics/skills"
		}
	}
	if !found {
		t.Fatalf("no pdf row in:\n%s", out.String())
	}
	if lines[len(lines)-2] != "" || lines[len(lines)-1] != "Install one with: tether skills install <name>" {
		t.Fatalf("footer in:\n%s", out.String())
	}

	out.Reset()
	if code := run([]string{"skills", "list", "--available", "zzz-none"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if out.String() != `No skill in the index matches "zzz-none".`+"\n" {
		t.Fatalf("stdout %q", out.String())
	}

	out.Reset()
	if code := run([]string{"skills", "list", "--available", "PDF"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "\npdf ") {
		t.Fatalf("the filter must ignore case:\n%s", out.String())
	}

	errb.Reset()
	if code := run([]string{"skills", "list", "--available", "a", "b"}, &out, &errb); code != 2 ||
		!strings.HasPrefix(errb.String(), "skills: unexpected argument: b\n") {
		t.Fatalf("exit %d: %q", code, errb.String())
	}
}

func TestSkillsListShowsInstalled(t *testing.T) {
	skillsHome(t)
	src := t.TempDir()
	writeTestSkill(t, src, "demo")
	withTerminal(t, false, "")
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "install", src, "--yes"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	out.Reset()
	if code := run([]string{"skills", "list"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	found := false
	for _, l := range strings.Split(out.String(), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "demo" {
			found = f[1] == "installed"
		}
	}
	if !found {
		t.Fatalf("no installed demo row in:\n%s", out.String())
	}

	out.Reset()
	if code := run([]string{"skills", "list", "--available"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.Contains(out.String(), " yes ") {
		t.Fatalf("demo is not in the index, so no row is installed:\n%s", out.String())
	}
}

func TestTermPrompter(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, " YES \n": true, "Yes": true, "n\n": false, "\n": false, "": false, "yep\n": false} {
		var out bytes.Buffer
		p := &termPrompter{r: bufio.NewReader(strings.NewReader(in)), out: &out}
		got, err := p.Confirm("Go?")
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", in, got, err, want)
		}
		if out.String() != "Go? [y/N] " {
			t.Errorf("%q: prompt %q", in, out.String())
		}
	}
	var out bytes.Buffer
	p := &termPrompter{r: bufio.NewReader(strings.NewReader("  demo \n")), out: &out}
	if got, err := p.Ask("Name:"); err != nil || got != "demo" || out.String() != "Name: " {
		t.Fatalf("got %q, %v; prompt %q", got, err, out.String())
	}
}

func TestTermPrompterStopsOnCancel(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	p := &termPrompter{ctx: ctx, r: bufio.NewReader(r), out: &out}
	cancel()
	if ok, err := p.Confirm("Go?"); ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, %v", ok, err)
	}
	var errb bytes.Buffer
	if code := skillsError(&errb, "install", fmt.Errorf("stage: %w", context.Canceled)); code != 1 || errb.String() != "Cancelled.\n" {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
}

func TestSkillsErrorSanitizes(t *testing.T) {
	var errb bytes.Buffer
	if code := skillsError(&errb, "install", errors.New("bad \x1b[31mred\x1b[0m name")); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if errb.String() != "skills install: bad red name\n" {
		t.Fatalf("stderr %q", errb.String())
	}
	errb.Reset()
	if code := skillsError(&errb, "install", skills.ErrCancelled); code != 1 || errb.Len() != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
}

// installDemo installs the skill "demo" from a new local folder with --yes
// and returns the source folder.
func installDemo(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	writeTestSkill(t, src, "demo")
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "install", src, "--yes"}, &out, &errb); code != 0 {
		t.Fatalf("install: exit %d: %s", code, errb.String())
	}
	return src
}

// changeDemoSource writes a new body into the SKILL.md of the source folder src.
func changeDemoSource(t *testing.T, src string) {
	t.Helper()
	body := "---\nname: demo\ndescription: A demo skill. Use when testing.\n---\nSay hello again.\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsUninstallCLI(t *testing.T) {
	home := skillsHome(t)
	withTerminal(t, false, "")
	installDemo(t)
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "uninstall", "demo", "--yes"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "demo")); !os.IsNotExist(err) {
		t.Fatalf("the skill folder must not exist: %v", err)
	}
	if !strings.Contains(out.String(), "Removed demo.") {
		t.Fatalf("stdout %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "state.db")); !os.IsNotExist(err) {
		t.Fatalf("the uninstall must not create state.db: %v", err)
	}

	errb.Reset()
	if code := run([]string{"skills", "uninstall", "tether-memory", "--yes"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
	want := "skills uninstall: \"tether-memory\" is not in the lockfile. Only installed skills can be removed this way.\n"
	if errb.String() != want {
		t.Fatalf("stderr %q", errb.String())
	}

	for _, args := range [][]string{
		{"skills", "uninstall"},
		{"skills", "uninstall", "a", "b"},
		{"skills", "uninstall", "demo", "--force"},
	} {
		errb.Reset()
		if code := run(args, &out, &errb); code != 2 {
			t.Fatalf("%v: exit %d: %s", args, code, errb.String())
		}
	}
}

func TestSkillsUninstallCronWarning(t *testing.T) {
	home := skillsHome(t)
	withTerminal(t, false, "")
	installDemo(t)
	d, err := db.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	store := cron.Store{DB: d}
	ctx := context.Background()
	job, err := store.Create(ctx, cron.Job{Name: "brief", Schedule: "every 2h", Prompt: "p", Skills: []string{"demo"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, cron.Job{Name: "other", Schedule: "every 2h", Prompt: "p", Skills: []string{"pdf"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	d.Close()

	var out, errb bytes.Buffer
	if code := run([]string{"skills", "uninstall", "demo", "--yes"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := `Cron job "brief" (` + job.ID + `) uses this skill. Its runs will skip it.`
	if !strings.Contains(out.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, out.String())
	}
	if strings.Contains(out.String(), `"other"`) {
		t.Fatalf("the job that does not use the skill shows:\n%s", out.String())
	}
}

func TestCronUsersNoStateDB(t *testing.T) {
	dir := t.TempDir()
	if got := cronUsers(dir)("demo"); got != nil {
		t.Fatalf("got %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); !os.IsNotExist(err) {
		t.Fatalf("cronUsers must not create state.db: %v", err)
	}
}

func TestSkillsCheckCLI(t *testing.T) {
	skillsHome(t)
	withTerminal(t, false, "")
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "check"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if out.String() != "No installed skills. Install one with: tether skills install <source>\n" {
		t.Fatalf("stdout %q", out.String())
	}

	src := installDemo(t)
	out.Reset()
	if code := run([]string{"skills", "check"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Fatalf("stdout %q", out.String())
	}

	changeDemoSource(t, src)
	out.Reset()
	if code := run([]string{"skills", "check", "demo"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "source changed") {
		t.Fatalf("stdout %q", out.String())
	}

	errb.Reset()
	if code := run([]string{"skills", "check", "nope"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(errb.String(), "skills check: ") || !strings.Contains(errb.String(), "is not in the lockfile") {
		t.Fatalf("stderr %q", errb.String())
	}

	for _, args := range [][]string{
		{"skills", "check", "--yes"},
		{"skills", "check", "a", "b"},
	} {
		errb.Reset()
		if code := run(args, &out, &errb); code != 2 {
			t.Fatalf("%v: exit %d: %s", args, code, errb.String())
		}
	}
}

func TestSkillsUpdateCLI(t *testing.T) {
	home := skillsHome(t)
	withTerminal(t, false, "")
	src := installDemo(t)
	changeDemoSource(t, src)
	var out, errb bytes.Buffer
	if code := run([]string{"skills", "update", "demo"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	// The error of each skill goes to stderr, before the summary line.
	if strings.Contains(out.String(), "--yes") {
		t.Fatalf("stdout %q", out.String())
	}
	if e := errb.String(); !strings.HasPrefix(e, "demo: ") || !strings.Contains(e, "--yes") ||
		!strings.HasSuffix(e, "\nskills update: 1 skill(s) were not updated\n") {
		t.Fatalf("stderr %q", e)
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"skills", "update", "demo", "--yes"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	b, err := os.ReadFile(filepath.Join(home, "skills", "demo", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Say hello again.") {
		t.Fatalf("SKILL.md %q", b)
	}

	errb.Reset()
	if code := run([]string{"skills", "update", "nope", "--yes"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
	errb.Reset()
	if code := run([]string{"skills", "update", "--skill", "x"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}
