package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func accessMux(p PairingFuncs, setup func(ctx context.Context) []SetupStep) *http.ServeMux {
	mux := http.NewServeMux()
	NewAccessAPI(p, setup).Register(mux)
	return mux
}

func accessDo(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func accessView(t *testing.T, rec *httptest.ResponseRecorder) AccessView {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var v AccessView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var (
	accessSince   = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	accessExpires = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
)

func accessPairing(approved *[]string, revoked *[]string) PairingFuncs {
	return PairingFuncs{
		List: func(context.Context) ([]PairingRow, error) {
			return []PairingRow{
				{Platform: "telegram", UserID: "41", Name: "Ann", Status: "pending", Since: accessSince, Expires: accessExpires},
				{Platform: "telegram", UserID: "42", Name: "Bob", Status: "approved", Since: accessSince},
			}, nil
		},
		Approve: func(_ context.Context, code string) error {
			if code == "BADCODE1" {
				return errors.New("pairing: code BADCODE1 is not found or expired")
			}
			*approved = append(*approved, code)
			return nil
		},
		Revoke: func(_ context.Context, platform, userID string) error {
			if userID == "99" {
				return errors.New("pairing: user 99 is not found for platform telegram")
			}
			*revoked = append(*revoked, platform+"/"+userID)
			return nil
		},
	}
}

func TestAccessPairingList(t *testing.T) {
	var approved, revoked []string
	v := accessView(t, accessDo(t, accessMux(accessPairing(&approved, &revoked), nil), http.MethodGet, "/api/pairing", ""))
	if !v.PairingOn || len(v.Pending) != 1 || len(v.Approved) != 1 {
		t.Fatalf("%+v", v)
	}
	p, a := v.Pending[0], v.Approved[0]
	if p.UserID != "41" || p.Name != "Ann" || p.Platform != "telegram" || !p.Since.Equal(accessSince) || !p.Expires.Equal(accessExpires) {
		t.Fatalf("pending: %+v", p)
	}
	if a.UserID != "42" || a.Name != "Bob" || a.Status != "approved" {
		t.Fatalf("approved: %+v", a)
	}
}

func TestAccessApproveAndRevoke(t *testing.T) {
	var approved, revoked []string
	mux := accessMux(accessPairing(&approved, &revoked), nil)
	if v := accessView(t, accessDo(t, mux, http.MethodPost, "/api/pairing/approve", `{"code":"K7QF2M9X"}`)); len(v.Pending) != 1 {
		t.Fatalf("the approve gives the new view: %+v", v)
	}
	if len(approved) != 1 || approved[0] != "K7QF2M9X" {
		t.Fatalf("approved %v", approved)
	}
	accessView(t, accessDo(t, mux, http.MethodPost, "/api/pairing/revoke", `{"platform":"telegram","user_id":"42"}`))
	if len(revoked) != 1 || revoked[0] != "telegram/42" {
		t.Fatalf("revoked %v", revoked)
	}
	for _, c := range []struct {
		path, body, want string
	}{
		{"/api/pairing/approve", `{"code":"  "}`, `{"error":"Type the pairing code."}`},
		{"/api/pairing/approve", `{"code":"BADCODE1"}`, `{"error":"pairing: code BADCODE1 is not found or expired"}`},
		{"/api/pairing/approve", `{"code":`, `{"error":"The request body is not valid JSON."}`},
		{"/api/pairing/revoke", `{"platform":"telegram"}`, `{"error":"Give the platform and the user id."}`},
		{"/api/pairing/revoke", `{"platform":"telegram","user_id":"99"}`, `{"error":"pairing: user 99 is not found for platform telegram"}`},
	} {
		rec := accessDo(t, mux, http.MethodPost, c.path, c.body)
		if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != c.want {
			t.Errorf("%s %s: %d %s", c.path, c.body, rec.Code, rec.Body.String())
		}
	}
	if len(approved) != 1 || len(revoked) != 1 {
		t.Fatalf("a bad request calls no func: %v %v", approved, revoked)
	}
}

func TestAccessPairingOff(t *testing.T) {
	mux := accessMux(PairingFuncs{}, nil)
	rec := accessDo(t, mux, http.MethodGet, "/api/pairing", "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"pairing_on":false`) ||
		!strings.Contains(body, `"pending":[]`) || !strings.Contains(body, `"approved":[]`) {
		t.Fatal(rec.Code, body)
	}
	const off = `{"error":"Pairing needs Telegram. Set telegram.enabled: true and restart the gateway."}`
	for path, payload := range map[string]string{
		"/api/pairing/approve": `{"code":"K7QF2M9X"}`,
		"/api/pairing/revoke":  `{"platform":"telegram","user_id":"42"}`,
	} {
		rec := accessDo(t, mux, http.MethodPost, path, payload)
		if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != off {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestAccessSetup(t *testing.T) {
	steps := []SetupStep{
		{Name: "Primary provider", State: "done", Detail: "openrouter · anthropic/claude-sonnet-4.5"},
		{Name: "HTTPS", State: "open", Detail: "The dashboard uses plain HTTP.", Command: "tailscale cert agent.tail1234.ts.net"},
	}
	rec := accessDo(t, accessMux(PairingFuncs{}, func(context.Context) []SetupStep { return steps }), http.MethodGet, "/api/setup", "")
	var got []SetupStep
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatal(rec.Code, err)
	}
	if len(got) != 2 || got[0] != steps[0] || got[1] != steps[1] {
		t.Fatalf("%+v", got)
	}
	rec = accessDo(t, accessMux(PairingFuncs{}, nil), http.MethodGet, "/api/setup", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("no func gives []: %s", rec.Body.String())
	}
}

func TestAccessTriesLeftAndBodyLimit(t *testing.T) {
	var approved, revoked []string
	p := accessPairing(&approved, &revoked)
	p.TriesLeft = func(context.Context) (int, error) { return 3, nil }
	mux := accessMux(p, nil)
	if v := accessView(t, accessDo(t, mux, "GET", "/api/pairing", "")); v.TriesLeft != 3 {
		t.Fatalf("%+v", v)
	}
	big := `{"code":"` + strings.Repeat("A", 4096) + `"}`
	if rec := accessDo(t, mux, "POST", "/api/pairing/approve", big); rec.Code != http.StatusBadRequest || len(approved) != 0 {
		t.Fatal(rec.Code, approved)
	}
	bigRevoke := `{"platform":"telegram","user_id":"` + strings.Repeat("1", 4096) + `"}`
	if rec := accessDo(t, mux, "POST", "/api/pairing/revoke", bigRevoke); rec.Code != http.StatusBadRequest || len(revoked) != 0 {
		t.Fatal(rec.Code, revoked)
	}
}
