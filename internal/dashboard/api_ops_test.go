package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

func opsRedact(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") }

// opsSetup makes a data dir with state.db and config.yaml, fills the empty fields of o, and
// returns the mux with the routes of NewOpsAPI(o).
func opsSetup(t *testing.T, o OpsOptions) (*http.ServeMux, OpsOptions) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	cfg := filepath.Join(dir, "config.yaml")
	text := "providers:\n  openai:\n    api_key: sk-proj-literal-0123456789\nagent:\n  note: uses sk-secret-value\n"
	if err := os.WriteFile(cfg, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	o.DataDir, o.ConfigPath, o.Version, o.Redact = dir, cfg, "v0.23.0", opsRedact
	if o.DotEnv == nil {
		o.DotEnv = func() (map[string]string, error) { return map[string]string{}, nil }
	}
	if o.Tasks == nil {
		o.Tasks = NewTasks(NewHub(1<<20), context.Background)
	}
	mux := http.NewServeMux()
	NewOpsAPI(o).Register(mux)
	return mux, o
}

func opsDo(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func opsTaskID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var v struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.TaskID == "" {
		t.Fatalf("%s %v", w.Body, err)
	}
	return v.TaskID
}

// opsWait waits until the task is not running any more and returns it.
func opsWait(t *testing.T, ts *Tasks, id string) TaskView {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if v, ok := ts.Get(id); ok && v.State != "running" {
			return v
		}
	}
	t.Fatalf("task %s did not end", id)
	return TaskView{}
}

func opsError(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var v struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%s %v", w.Body, err)
	}
	return v.Error
}

func TestOpsConfigMasked(t *testing.T) {
	mux, o := opsSetup(t, OpsOptions{})
	w := opsDo(mux, "GET", "/api/config", "")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	var v ConfigView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Path != o.ConfigPath || !strings.Contains(v.Text, "api_key: ${OPENAI_API_KEY}") ||
		!strings.Contains(v.Text, "uses [REDACTED]-value") ||
		strings.Contains(v.Text, "sk-proj-literal") || strings.Contains(v.Text, "sk-secret") {
		t.Fatalf("%+v", v)
	}
	if err := os.Remove(o.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if w := opsDo(mux, "GET", "/api/config", ""); w.Code != http.StatusNotFound {
		t.Fatal(w.Code, w.Body)
	}
}

func TestOpsBackupCreateListDownload(t *testing.T) {
	mux, o := opsSetup(t, OpsOptions{})
	v := opsWait(t, o.Tasks, opsTaskID(t, opsDo(mux, "POST", "/api/backup", "")))
	if v.State != "done" || v.Kind != "backup" || !strings.HasPrefix(v.Text, "Saved tether-backup-") {
		t.Fatalf("%+v", v)
	}
	w := opsDo(mux, "GET", "/api/backup", "")
	var list BackupView
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK {
		t.Fatal(w.Code, err)
	}
	if len(list.Files) != 1 || !strings.HasSuffix(list.Files[0].Name, ".tar.gz") || list.Files[0].Size <= 0 ||
		!strings.Contains(v.Text, list.Files[0].Name) {
		t.Fatalf("%+v", list)
	}
	f := list.Files[0]
	w = opsDo(mux, "GET", "/api/backup/"+f.Name, "")
	if w.Code != http.StatusOK || int64(w.Body.Len()) != f.Size ||
		w.Header().Get("Content-Disposition") != `attachment; filename="`+f.Name+`"` {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Disposition"))
	}
	if err := os.Symlink(o.ConfigPath, filepath.Join(o.DataDir, "backups", "link.tar.gz")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{".tmp-x.tar.gz", "state.db", "nope.tar.gz", "link.tar.gz"} {
		if w := opsDo(mux, "GET", "/api/backup/"+bad, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}

func TestOpsUpdate(t *testing.T) {
	info := UpdateInfo{Current: "v0.23.0", Latest: "v0.24.0", Newer: true,
		NotesURL: "https://github.com/dnwandana/tether-agent/releases/tag/v0.24.0", Writable: false, Path: "/usr/local/bin/tether"}
	release := make(chan struct{})
	var runs atomic.Int32
	mux, o := opsSetup(t, OpsOptions{
		UpdateCheck: func(context.Context) (UpdateInfo, error) { return info, nil },
		UpdateRun:   func(context.Context) error { runs.Add(1); <-release; return nil },
	})
	w := opsDo(mux, "GET", "/api/update", "")
	var got UpdateInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got != info {
		t.Fatalf("%+v %v", got, err)
	}
	if w := opsDo(mux, "POST", "/api/update", "{}"); w.Code != http.StatusBadRequest || opsError(t, w) != "Confirm the update." {
		t.Fatal(w.Code, w.Body)
	}
	w = opsDo(mux, "POST", "/api/update", `{"confirm": true}`)
	if w.Code != http.StatusForbidden || opsError(t, w) !=
		"The binary at /usr/local/bin/tether is not writable by this user. Run `tether update` as a user who can write it." {
		t.Fatal(w.Code, w.Body)
	}
	if runs.Load() != 0 {
		t.Fatal("an update of a binary that is not writable must not run")
	}
	info.Writable = true
	id := opsTaskID(t, opsDo(mux, "POST", "/api/update", `{"confirm": true}`))
	if w := opsDo(mux, "POST", "/api/update", `{"confirm": true}`); w.Code != http.StatusConflict {
		t.Fatal("a second update must wait for the first:", w.Code)
	}
	close(release)
	if v := opsWait(t, o.Tasks, id); v.State != "done" || v.Kind != "update" || runs.Load() != 1 {
		t.Fatalf("%+v runs=%d", v, runs.Load())
	}
}

// The errors of the backup and update tasks go to the page with the secrets masked.
func TestOpsTaskErrorsRedacted(t *testing.T) {
	mux, o := opsSetup(t, OpsOptions{
		DotEnv: func() (map[string]string, error) { return nil, errors.New("env read failed sk-secret") },
		UpdateCheck: func(context.Context) (UpdateInfo, error) {
			return UpdateInfo{Newer: true, Writable: true}, nil
		},
		UpdateRun: func(context.Context) error { return errors.New("replace failed sk-secret") },
	})
	for _, path := range []string{"/api/backup", "/api/update"} {
		v := opsWait(t, o.Tasks, opsTaskID(t, opsDo(mux, "POST", path, `{"confirm": true}`)))
		if v.State != "error" || strings.Contains(v.Text, "sk-secret") || !strings.Contains(v.Text, "[REDACTED]") {
			t.Errorf("%s: %+v", path, v)
		}
	}
}
