package obs

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
)

func TestRotatesAtSizeAndKeepsMaxFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tether.log")
	w, err := NewRotatingWriter(p, 1, 3) // 1 MB
	if err != nil {
		t.Fatal(err)
	}
	line := bytes.Repeat([]byte("x"), 1023)
	line = append(line, '\n')
	for i := 0; i < 1024*4; i++ { // about 4 MB
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"tether.log", "tether.log.1", "tether.log.2"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatal(n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "tether.log.3")); err == nil {
		t.Fatal("max 3 files")
	}
}

func TestNoFilePassesTheLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tether.log")
	w, err := NewRotatingWriter(p, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := append(bytes.Repeat([]byte("y"), 999), '\n')
	for i := 0; i < 3000; i++ {
		w.Write(line)
	}
	w.Close()
	for _, n := range []string{"tether.log", "tether.log.1", "tether.log.2"} {
		fi, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > 1<<20 {
			t.Fatalf("%s has %d bytes", n, fi.Size())
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s has mode %o", n, fi.Mode().Perm())
		}
	}
}

func TestRotationShiftsOldFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.log")
	w, err := NewRotatingWriter(p, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("z"), 700*1024)
	for _, tag := range []string{"one\n", "two\n", "three\n", "four\n"} {
		w.Write(append([]byte(tag), big...))
	}
	w.Close()
	for n, want := range map[string]string{"a.log": "four", "a.log.1": "three", "a.log.2": "two"} {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil || !bytes.HasPrefix(b, []byte(want+"\n")) {
			t.Fatalf("%s: want %s first, err %v", n, want, err)
		}
	}
}

func TestReopenAppendsAndCountsSize(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "b.log")
	if err := os.WriteFile(p, bytes.Repeat([]byte("o"), 1<<20-10), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewRotatingWriter(p, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("this line does not fit\n"))
	w.Close()
	if b, _ := os.ReadFile(p); string(b) != "this line does not fit\n" {
		t.Fatalf("got %d bytes", len(b))
	}
	if fi, err := os.Stat(p + ".1"); err != nil || fi.Size() != 1<<20-10 {
		t.Fatal("old file must move to .1")
	}
}

func TestRotationRemovesExtraOldFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.log")
	for _, n := range []string{".1", ".2", ".3", ".4"} {
		os.WriteFile(p+n, []byte("old"), 0o600)
	}
	w, err := NewRotatingWriter(p, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(bytes.Repeat([]byte("q"), 1<<20))
	w.Write([]byte("next\n"))
	w.Close()
	for _, n := range []string{".2", ".3", ".4"} {
		if _, err := os.Stat(p + n); err == nil {
			t.Fatalf("%s must be removed", n)
		}
	}
}

func TestWriteAfterCloseFails(t *testing.T) {
	w, err := NewRotatingWriter(filepath.Join(t.TempDir(), "sub", "d.log"), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("write after close must fail")
	}
}

func TestLoggerRedactsAndIsJSON(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "info", security.NewRedactor([]string{"supersecretvalue1"}))
	log.Info("call failed", "url", "https://x?api_key=abc123", "detail", "supersecretvalue1")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "supersecretvalue1") || strings.Contains(buf.String(), "abc123") {
		t.Fatal(buf.String())
	}
}

func TestLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(&buf, "warn", security.NewRedactor(nil)).Info("hidden")
	if buf.Len() != 0 {
		t.Fatal("info must be filtered at warn")
	}
}

func TestLevelNames(t *testing.T) {
	for _, c := range []struct {
		name  string
		level slog.Level
	}{{"debug", slog.LevelDebug}, {"INFO", slog.LevelInfo}, {"warning", slog.LevelWarn}, {"error", slog.LevelError}, {"", slog.LevelInfo}, {"bogus", slog.LevelInfo}} {
		if got := ParseLevel(c.name); got != c.level {
			t.Errorf("%q: got %v", c.name, got)
		}
	}
}

type secretValuer struct{}

func (secretValuer) LogValue() slog.Value {
	return slog.StringValue("token sk-proj-abc123def456ghi789jkl012")
}

type secretStruct struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

func TestLoggerRedactsEverywhere(t *testing.T) {
	const key = "sk-proj-abc123def456ghi789jkl012"
	var buf bytes.Buffer
	log := NewLogger(&buf, "debug", security.NewRedactor(nil))
	log = log.With("pre", "key "+key).WithGroup("g")
	log.Info("msg "+key,
		slog.Group("nested", slog.String("inner", key), slog.Group("deeper", "x", key)),
		"err", errors.New("call with "+key+" failed"),
		"valuer", secretValuer{},
		"obj", secretStruct{Name: "n", Token: "plainopaquetoken99"},
		"count", 3,
	)
	out := buf.String()
	if strings.Contains(out, "abc123def456") || strings.Contains(out, "plainopaquetoken99") {
		t.Fatal(out)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err, out)
	}
	g := m["g"].(map[string]any)
	if g["count"] != float64(3) {
		t.Fatalf("non-string values must stay: %v", g["count"])
	}
	if obj, ok := g["obj"].(map[string]any); !ok || obj["name"] != "n" {
		t.Fatalf("a struct must stay a JSON object: %v", g["obj"])
	}
}
