package main

import (
	"bytes"
	"context"
	"fmt"
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

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
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
	var out, errb bytes.Buffer
	if run([]string{"nope"}, &out, &errb) != 2 {
		t.Fatal("want exit 2")
	}
	if !strings.Contains(errb.String(), "unknown command: nope") {
		t.Fatal(errb.String())
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

	// --log-stderr sends the logs to stderr, and no args also start the chat.
	stdin = strings.NewReader("again\n")
	out.Reset()
	if code := run([]string{"--data-dir", dir, "--log-stderr"}, &out, &errb); code != 0 {
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
	// No config: providers.primary is missing, so the config check fails.
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"doctor", "--data-dir", dir}, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "✗ config") || !strings.Contains(out.String(), "→ run tether setup") {
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
	for _, want := range []string{"# Tools", "## terminal", "### run_command", "### run_script", "```json"} {
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
