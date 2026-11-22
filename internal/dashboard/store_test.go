package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// openStore returns a Store over a new state.db in a temporary folder.
func openStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return NewStore(d)
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

var t0 = time.UnixMilli(1_763_640_000_000)

func TestCreateSessionAndLookup(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	id, cookie, header, err := s.CreateSession(ctx, "Chrome on Android", "Mozilla/5.0", "10.8.0.5", 24*time.Hour, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 || len(cookie) != 43 || len(header) != 43 || cookie == header {
		t.Fatalf("id %q cookie %q header %q", id, cookie, header)
	}
	d, err := s.Lookup(ctx, cookie, header, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != id || d.Label != "Chrome on Android" || d.UserAgent != "Mozilla/5.0" || d.IP != "10.8.0.5" ||
		!d.Created.Equal(t0) || !d.LastSeen.Equal(t0) || !d.Expires.Equal(t0.Add(24*time.Hour)) || d.Revoked {
		t.Fatalf("%+v", d)
	}
	// The table keeps only the hashes.
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM dashboard_sessions WHERE cookie_hash = ? AND header_hash = ?`,
		sha(cookie), sha(header)).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM dashboard_sessions WHERE cookie_hash = ? OR header_hash = ? OR id = ?`,
		cookie, header, cookie).Scan(&n); err != nil || n != 0 {
		t.Fatal("a raw value is in the table", n, err)
	}
	for name, c := range map[string][2]string{
		"no values":   {"", ""},
		"cookie only": {cookie, ""},
		"header only": {"", header},
		"swapped":     {header, cookie},
		"bad header":  {cookie, header + "x"},
	} {
		if _, err := s.Lookup(ctx, c[0], c[1], t0); !errors.Is(err, ErrNoSession) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := s.Lookup(ctx, cookie, header, t0.Add(24*time.Hour)); !errors.Is(err, ErrNoSession) {
		t.Fatal("an expired session must fail:", err)
	}
	if err := s.Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, cookie, header, t0); !errors.Is(err, ErrNoSession) {
		t.Fatal("a revoked session must fail at once:", err)
	}
	if err := s.Revoke(ctx, "nope"); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
}

func TestTouchWritesAtMostEveryFiveMinutes(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	id, cookie, header, _ := s.CreateSession(ctx, "Laptop", "ua", "127.0.0.1", time.Hour, t0)
	seen := func() time.Time {
		t.Helper()
		d, err := s.Lookup(ctx, cookie, header, t0)
		if err != nil {
			t.Fatal(err)
		}
		return d.LastSeen
	}
	if err := s.Touch(ctx, id, t0.Add(4*time.Minute)); err != nil || !seen().Equal(t0) {
		t.Fatal("a touch within 5 minutes must not write", err)
	}
	if err := s.Touch(ctx, id, t0.Add(6*time.Minute)); err != nil || !seen().Equal(t0.Add(6*time.Minute)) {
		t.Fatal("a touch after 5 minutes must write", err)
	}
}

func TestListAndRevokeOthers(t *testing.T) {
	ctx := context.Background()
	base := time.Now()
	s := openStore(t)
	_, _, _, _ = s.CreateSession(ctx, "A", "ua", "10.0.0.1", time.Hour, base)
	b, _, _, _ := s.CreateSession(ctx, "B", "ua", "10.0.0.2", time.Hour, base.Add(time.Minute))
	_, _, _, _ = s.CreateSession(ctx, "Old", "ua", "10.0.0.3", time.Minute, base)
	_, _, _, _ = s.CreateSession(ctx, "C", "ua", "10.0.0.4", time.Hour, base.Add(2*time.Minute))
	list, err := s.List(ctx, base.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, d := range list {
		labels = append(labels, d.Label)
	}
	if strings.Join(labels, ",") != "C,B,A" {
		t.Fatalf("the list must hold the live devices, newest first: %v", labels)
	}
	if n, err := s.RevokeOthers(ctx, b, base.Add(3*time.Minute)); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if list, _ = s.List(ctx, base.Add(3*time.Minute)); len(list) != 1 || list[0].ID != b {
		t.Fatalf("%+v", list)
	}
	if n, err := s.RevokeAll(ctx, base.Add(3*time.Minute)); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if list, _ = s.List(ctx, base); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

func TestCodeWorksOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	code, err := s.AddCode(ctx, 5*time.Minute, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`).MatchString(code) {
		t.Fatalf("code %q", code)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM dashboard_codes WHERE code_hash = ?`,
		sha(strings.ReplaceAll(code, "-", ""))).Scan(&n); err != nil || n != 1 {
		t.Fatal("the table must keep the hash of the code", n, err)
	}
	// Lower case and no dash are accepted.
	if err := s.ConsumeCode(ctx, strings.ToLower(strings.ReplaceAll(code, "-", "")), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeCode(ctx, code, t0.Add(time.Minute)); !errors.Is(err, ErrBadCode) {
		t.Fatal("a code works once:", err)
	}
	old, _ := s.AddCode(ctx, 5*time.Minute, t0)
	if err := s.ConsumeCode(ctx, old, t0.Add(5*time.Minute)); !errors.Is(err, ErrBadCode) {
		t.Fatal("an old code must fail:", err)
	}
	for _, bad := range []string{"", "ABCD-EFG", "ABCD-EFGHI", "IIII-IIII"} {
		if err := s.ConsumeCode(ctx, bad, t0); !errors.Is(err, ErrBadCode) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// AddCode deletes the expired codes.
	_, _ = s.AddCode(ctx, 5*time.Minute, t0)
	_, _ = s.AddCode(ctx, 5*time.Minute, t0.Add(10*time.Minute))
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM dashboard_codes`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

// RevokeOthers and RevokeAll do not count a session that expired.
func TestRevokeDoesNotCountExpired(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	now := time.Now()
	live, _, _, _ := s.CreateSession(ctx, "Live", "ua", "10.0.0.1", time.Hour, now)
	_, _, _, _ = s.CreateSession(ctx, "Live2", "ua", "10.0.0.2", time.Hour, now)
	_, _, _, _ = s.CreateSession(ctx, "Old", "ua", "10.0.0.3", time.Minute, now.Add(-time.Hour))
	if n, err := s.RevokeOthers(ctx, live, now); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	_, _, _, _ = s.CreateSession(ctx, "Old2", "ua", "10.0.0.4", time.Minute, now.Add(-time.Hour))
	if n, err := s.RevokeAll(ctx, now); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
