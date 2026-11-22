package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
)

func securityDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func securityNow() time.Time { return time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC) }

// securityDo sends one request to h. A cookie adds the session cookie and the session header.
func securityDo(t *testing.T, h http.Handler, method, path, cookie, header string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = "127.0.0.1:8787"
	if method != http.MethodGet {
		req.Header.Set("Origin", "http://127.0.0.1:8787")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
		req.Header.Set(HeaderName, header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func securityCode(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))[:8] }

func securityMux(api *SecurityAPI) *http.ServeMux {
	mux := http.NewServeMux()
	api.Register(mux)
	return mux
}

func TestSecurityView(t *testing.T) {
	ctx := context.Background()
	d := securityDB(t)
	gate := &security.Gate{DB: d, Mode: "smart", CronMode: "approve"}
	for scope, mode := range map[string]string{"dashboard": security.ModeFull, "telegram:5:0": security.ModeReadOnly} {
		if err := gate.SetMode(ctx, scope, mode); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO approval_grants(id, pattern_id, content_sha256, workdir, via, programs, preview, created_at)
		VALUES ('g1', 'recursive_delete', 'x', '/srv', 'bash -c', '["/usr/bin/rm"]', 'rm -rf build/', 1000)`); err != nil {
		t.Fatal(err)
	}
	mark := time.Date(2026, 11, 19, 14, 2, 0, 0, time.UTC)
	secrets := gateway.SecretsDeps{
		Bound: func() map[string][]string {
			return map[string][]string{"NPM_TOKEN": {"npm publish"}, "GH_TOKEN": {"gh"}}
		},
		HasValue: func(name string) bool { return name == "GH_TOKEN" },
		Marks: func(context.Context) (map[string]time.Time, error) {
			return map[string]time.Time{"GH_TOKEN": mark}, nil
		},
		Pins: func(_ context.Context, name string) ([]security.PinTarget, error) {
			if name == "GH_TOKEN" {
				return []security.PinTarget{{Path: "/usr/bin/gh", SHA256: "ab12"}}, nil
			}
			return nil, nil
		},
	}
	fixed := SecurityFixed{ProtectedPaths: []string{"/home/u/.tether/state.db"}, Toolsets: map[string][]string{"dashboard": {"all"}}}
	mux := securityMux(NewSecurityAPI(gate, secrets, fixed, NewStore(d), securityNow))

	rec := securityDo(t, mux, http.MethodGet, "/api/security", "", "")
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var v SecurityView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.ProcessMode != "smart" || v.CronMode != "approve" {
		t.Fatalf("process mode %q, cron mode %q", v.ProcessMode, v.CronMode)
	}
	if v.Mode != "full" || len(v.Modes) != 2 || v.Modes[0].Scope != "dashboard" || v.Modes[1].Mode != "read-only" {
		t.Fatalf("modes: %q %+v", v.Mode, v.Modes)
	}
	if len(v.Grants) != 1 {
		t.Fatalf("grants: %+v", v.Grants)
	}
	g := v.Grants[0]
	if g.Rule != "recursive_delete" || g.Preview != "rm -rf build/" || g.Workdir != "/srv" || g.Via != "bash -c" ||
		!slices.Equal(g.Programs, []string{"/usr/bin/rm"}) || !g.Created.Equal(time.UnixMilli(1000)) ||
		g.Remove != "/approvals remove 1 "+securityCode("g1") {
		t.Fatalf("grant: %+v", g)
	}
	if v.RemoveAll != "/approvals remove all "+securityCode("g1") || v.GrantsOff {
		t.Fatalf("remove all %q, off %v", v.RemoveAll, v.GrantsOff)
	}
	if len(v.Secrets) != 2 {
		t.Fatalf("secrets: %+v", v.Secrets)
	}
	gh, npm := v.Secrets[0], v.Secrets[1]
	if gh.Name != "GH_TOKEN" || gh.State != "check" || !gh.FailedAt.Equal(mark) || !slices.Equal(gh.Commands, []string{"gh"}) ||
		!slices.Equal(gh.Programs, []string{"/usr/bin/gh"}) || gh.Set != "/secrets set GH_TOKEN" || gh.Unpin != "/secrets unpin GH_TOKEN" {
		t.Fatalf("GH_TOKEN: %+v", gh)
	}
	if npm.Name != "NPM_TOKEN" || npm.State != "not set" || len(npm.Programs) != 0 || !npm.FailedAt.IsZero() {
		t.Fatalf("NPM_TOKEN: %+v", npm)
	}
	if !slices.Equal(v.Fixed.ProtectedPaths, fixed.ProtectedPaths) || !slices.Equal(v.Fixed.Toolsets["dashboard"], []string{"all"}) {
		t.Fatalf("fixed: %+v", v.Fixed)
	}

	// A new state.db: the dashboard scope is guarded, and the lists are [] and not null.
	empty := securityMux(NewSecurityAPI(&security.Gate{DB: securityDB(t)}, gateway.SecretsDeps{}, SecurityFixed{}, nil, securityNow))
	body := securityDo(t, empty, http.MethodGet, "/api/security", "", "").Body.String()
	for _, want := range []string{`"mode":"guarded"`, `"modes":[]`, `"grants":[]`, `"remove_all":""`, `"secrets":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("body has no %s: %s", want, body)
		}
	}
}

func TestSecurityLogPage(t *testing.T) {
	ctx := context.Background()
	d := securityDB(t)
	for i, scope := range []string{"dashboard", "cli", "dashboard"} {
		if _, err := d.ExecContext(ctx, `INSERT INTO approval_log(at, scope, decision, source, preview)
			VALUES (?, ?, 'allowed', 'owner', ?)`, int64(i+1), scope, fmt.Sprintf("cmd %d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	mux := securityMux(NewSecurityAPI(&security.Gate{DB: d}, gateway.SecretsDeps{}, SecurityFixed{}, nil, securityNow))
	rec := securityDo(t, mux, http.MethodGet, "/api/security/log?scope=dashboard&offset=1&limit=1", "", "")
	var p LogPage
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(rec.Code, err)
	}
	if p.Total != 2 || p.Offset != 1 || p.Limit != 1 || len(p.Rows) != 1 || p.Rows[0].Preview != "cmd 1" ||
		p.Rows[0].Scope != "dashboard" || p.Rows[0].Decision != "allowed" || !p.Rows[0].At.Equal(time.UnixMilli(1)) {
		t.Fatalf("%+v", p)
	}
	for path, limit := range map[string]int{"/api/security/log": 50, "/api/security/log?limit=500": 200} {
		p = LogPage{}
		if err := json.Unmarshal(securityDo(t, mux, http.MethodGet, path, "", "").Body.Bytes(), &p); err != nil || p.Limit != limit || p.Total != 3 {
			t.Errorf("%s: %+v %v", path, p, err)
		}
	}
	for _, path := range []string{"/api/security/log?limit=x", "/api/security/log?offset=-1"} {
		rec := securityDo(t, mux, http.MethodGet, path, "", "")
		if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != `{"error":"offset and limit must be whole numbers."}` {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestSecurityDevices(t *testing.T) {
	ctx := context.Background()
	d := securityDB(t)
	st := NewStore(d)
	idA, cookieA, headerA, err := st.CreateSession(ctx, "Chrome on Android", "Mozilla/5.0 (Android)", "10.8.0.5", 24*time.Hour, securityNow())
	if err != nil {
		t.Fatal(err)
	}
	idB, _, _, err := st.CreateSession(ctx, "Firefox on Linux", "Mozilla/5.0 (X11)", "127.0.0.1", 24*time.Hour, securityNow())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DashboardConfig{Enabled: true, Listen: "127.0.0.1:8787", SessionHours: 24, MaxUploadMB: 20}
	h := NewAuth(cfg, st, securityNow).Middleware(securityMux(NewSecurityAPI(&security.Gate{DB: d}, gateway.SecretsDeps{}, SecurityFixed{}, st, securityNow)))
	rows := func(rec *httptest.ResponseRecorder) []DeviceRow {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Code, rec.Body.String())
		}
		var out []DeviceRow
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	got := rows(securityDo(t, h, http.MethodGet, "/api/devices", cookieA, headerA))
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	for _, r := range got {
		switch r.ID {
		case idA:
			if !r.ThisDevice || r.Label != "Chrome on Android" || r.Browser != "Mozilla/5.0 (Android)" || r.IP != "10.8.0.5" ||
				!r.Expires.Equal(securityNow().Add(24*time.Hour)) {
				t.Fatalf("this device: %+v", r)
			}
		case idB:
			if r.ThisDevice {
				t.Fatalf("other device: %+v", r)
			}
		default:
			t.Fatalf("unknown row %+v", r)
		}
	}
	rec := securityDo(t, h, http.MethodPost, "/api/devices/nope/revoke", cookieA, headerA)
	if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `{"error":"No signed-in device has this id."}` {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if got := rows(securityDo(t, h, http.MethodPost, "/api/devices/"+idB+"/revoke", cookieA, headerA)); len(got) != 1 || got[0].ID != idA {
		t.Fatalf("after revoke: %+v", got)
	}
	if _, _, _, err := st.CreateSession(ctx, "Safari on iPhone", "Mozilla/5.0 (iPhone)", "10.8.0.7", 24*time.Hour, securityNow()); err != nil {
		t.Fatal(err)
	}
	if got := rows(securityDo(t, h, http.MethodPost, "/api/devices/revoke-others", cookieA, headerA)); len(got) != 1 || got[0].ID != idA {
		t.Fatalf("after revoke-others: %+v", got)
	}
	rows(securityDo(t, h, http.MethodPost, "/api/devices/"+idA+"/revoke", cookieA, headerA))
	if rec := securityDo(t, h, http.MethodGet, "/api/devices", cookieA, headerA); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked session gets 401 at once: %d", rec.Code)
	}
}
