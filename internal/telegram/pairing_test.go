package telegram

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// pairClock is a settable clock for the pairing tests.
type pairClock struct{ t time.Time }

func (c *pairClock) now() time.Time          { return c.t }
func (c *pairClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func pairStore(t *testing.T) (*Pairing, *pairClock) {
	t.Helper()
	clk := &pairClock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	return &Pairing{DB: openTestDB(t), Now: clk.now}, clk
}

func mustRequest(t *testing.T, p *Pairing, userID string) string {
	t.Helper()
	code, _, err := p.Request(ctx, "telegram", userID, "user"+userID)
	if err != nil || code == "" {
		t.Fatalf("request for %s: code %q, err %v", userID, code, err)
	}
	return code
}

func TestCodeShapeAndReply(t *testing.T) {
	p, _ := pairStore(t)
	code, reply, err := p.Request(ctx, "telegram", "6", "ana")
	if err != nil || len(code) != PairCodeLen || strings.ContainsAny(code, "0O1I") {
		t.Fatal(code, err)
	}
	for _, r := range code {
		if !strings.ContainsRune(PairAlphabet, r) {
			t.Fatalf("code %q has %q, which is not in the alphabet", code, r)
		}
	}
	if !strings.Contains(reply, "I don't recognize you yet") ||
		!strings.Contains(reply, "Here's your pairing code: `"+code+"`") ||
		!strings.Contains(reply, "tether pairing approve telegram "+code) {
		t.Fatal(reply)
	}
}

func TestCodeStoredAsHash(t *testing.T) {
	p, _ := pairStore(t)
	code := mustRequest(t, p, "6")
	var stored string
	if err := p.DB.QueryRow("SELECT code FROM pairing WHERE user_id='6'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "" || strings.Contains(stored, code) {
		t.Fatalf("the table holds the plain code: %q", stored)
	}
}

func TestCodeUsesAlphabetWithoutBias(t *testing.T) {
	// Byte i maps to letter i. No byte is rejected, because the alphabet
	// has 32 letters and 256 is a multiple of 32.
	src := make([]byte, 0, 64)
	for i := range 32 {
		src = append(src, byte(i))
	}
	got, err := newPairCode(bytes.NewReader(src[:PairCodeLen]))
	if err != nil {
		t.Fatal(err)
	}
	if got != PairAlphabet[:PairCodeLen] {
		t.Fatalf("got %q", got)
	}
}

func TestApproveThenAllowed(t *testing.T) {
	p, _ := pairStore(t)
	code := mustRequest(t, p, "6")
	if p.IsApproved(ctx, "telegram", "6") {
		t.Fatal("approved before approve")
	}
	// The owner can type the code in lower case with spaces around it.
	uid, err := p.Approve(ctx, "telegram", " "+strings.ToLower(code)+" ")
	if err != nil || uid != "6" {
		t.Fatal(uid, err)
	}
	if !p.IsApproved(ctx, "telegram", "6") {
		t.Fatal("not approved")
	}
	if p.IsApproved(ctx, "other", "6") {
		t.Fatal("approval leaked to another platform")
	}
	// A code works once.
	if _, err := p.Approve(ctx, "telegram", code); !errors.Is(err, ErrPairCodeNotFound) {
		t.Fatal(err)
	}
}

func TestExpiresAfter1h(t *testing.T) {
	p, clk := pairStore(t)
	code := mustRequest(t, p, "6")
	clk.advance(61 * time.Minute)
	_, err := p.Approve(ctx, "telegram", code)
	if !errors.Is(err, ErrPairCodeExpired) || !strings.Contains(err.Error(), "expired") {
		t.Fatal(err)
	}
	if p.IsApproved(ctx, "telegram", "6") {
		t.Fatal("expired code approved the user")
	}
}

func TestOneRequestPer10Min(t *testing.T) {
	p, clk := pairStore(t)
	first := mustRequest(t, p, "6")
	// Hermes stays silent for a second request in 10 min, so the user does
	// not get a reply for each message.
	clk.advance(9 * time.Minute)
	code, reply, err := p.Request(ctx, "telegram", "6", "ana")
	if !errors.Is(err, ErrPairRateLimited) || code != "" || reply != "" {
		t.Fatal(code, reply, err)
	}
	var n int
	if err := p.DB.QueryRow("SELECT count(*) FROM pairing").Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	// The first code still works.
	if uid, err := p.Approve(ctx, "telegram", first); err != nil || uid != "6" {
		t.Fatal(uid, err)
	}
}

func TestNewCodeAfter10MinReplacesOld(t *testing.T) {
	p, clk := pairStore(t)
	first := mustRequest(t, p, "6")
	clk.advance(11 * time.Minute)
	second := mustRequest(t, p, "6")
	if _, err := p.Approve(ctx, "telegram", first); !errors.Is(err, ErrPairCodeNotFound) {
		t.Fatal(err)
	}
	if uid, err := p.Approve(ctx, "telegram", second); err != nil || uid != "6" {
		t.Fatal(uid, err)
	}
}

func TestMax3Pending(t *testing.T) {
	p, _ := pairStore(t)
	for _, u := range []string{"1", "2", "3"} {
		mustRequest(t, p, u)
	}
	code, reply, err := p.Request(ctx, "telegram", "4", "dan")
	if !errors.Is(err, ErrTooManyPending) || err.Error() != "too many pending pairing requests" || code != "" {
		t.Fatal(code, err)
	}
	if reply != PairBusyReply {
		t.Fatal(reply)
	}
	// Hermes records a rate limit, so the next message gets no reply.
	if _, reply, err := p.Request(ctx, "telegram", "4", "dan"); !errors.Is(err, ErrPairRateLimited) || reply != "" {
		t.Fatal(reply, err)
	}
	// Another platform has its own limit.
	if code, _, err := p.Request(ctx, "other", "4", "dan"); err != nil || code == "" {
		t.Fatal(code, err)
	}
}

func TestExpiredCodesDoNotCountAsPending(t *testing.T) {
	p, clk := pairStore(t)
	for _, u := range []string{"1", "2", "3"} {
		mustRequest(t, p, u)
	}
	clk.advance(61 * time.Minute)
	mustRequest(t, p, "4")
}

func TestLockoutAfterFailures(t *testing.T) {
	p, clk := pairStore(t)
	code := mustRequest(t, p, "6")
	for i := range PairMaxFailures {
		if _, err := p.Approve(ctx, "telegram", "AAAAAAAA"); !errors.Is(err, ErrPairCodeNotFound) {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
	if _, err := p.Approve(ctx, "telegram", code); !errors.Is(err, ErrPairLockedOut) {
		t.Fatal(err)
	}
	if p.IsApproved(ctx, "telegram", "6") {
		t.Fatal("approved during the lockout")
	}
	// A request during the lockout gets the busy reply (Hermes).
	if _, reply, err := p.Request(ctx, "telegram", "7", "bo"); !errors.Is(err, ErrPairLockedOut) || reply != PairBusyReply {
		t.Fatal(reply, err)
	}
	// The lockout is for one platform only.
	other := mustRequestOn(t, p, "other", "8")
	if _, err := p.Approve(ctx, "other", other); err != nil {
		t.Fatal(err)
	}
	// The lockout ends after 1 h. The first code expired in that time.
	clk.advance(61 * time.Minute)
	if _, err := p.Approve(ctx, "telegram", code); !errors.Is(err, ErrPairCodeExpired) {
		t.Fatal(err)
	}
	fresh := mustRequest(t, p, "9")
	if uid, err := p.Approve(ctx, "telegram", fresh); err != nil || uid != "9" {
		t.Fatal(uid, err)
	}
}

func TestLockoutResetsFailureCount(t *testing.T) {
	p, clk := pairStore(t)
	for range PairMaxFailures {
		_, _ = p.Approve(ctx, "telegram", "AAAAAAAA")
	}
	clk.advance(61 * time.Minute)
	// Four new failures do not start a new lockout.
	for range PairMaxFailures - 1 {
		_, _ = p.Approve(ctx, "telegram", "AAAAAAAA")
	}
	code := mustRequest(t, p, "6")
	if _, err := p.Approve(ctx, "telegram", code); err != nil {
		t.Fatal(err)
	}
}

func mustRequestOn(t *testing.T, p *Pairing, platform, userID string) string {
	t.Helper()
	code, _, err := p.Request(ctx, platform, userID, "")
	if err != nil || code == "" {
		t.Fatalf("request on %s: code %q, err %v", platform, code, err)
	}
	return code
}

func TestListAndRevoke(t *testing.T) {
	p, clk := pairStore(t)
	code := mustRequest(t, p, "6")
	mustRequest(t, p, "7")
	if _, err := p.Approve(ctx, "telegram", code); err != nil {
		t.Fatal(err)
	}
	clk.advance(5 * time.Minute)
	rows, err := p.List(ctx, "telegram")
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	byUser := map[string]PairRow{}
	for _, r := range rows {
		byUser[r.UserID] = r
	}
	if byUser["6"].Status != PairApproved || byUser["6"].UserName != "user6" || byUser["6"].ApprovedAt.IsZero() {
		t.Fatalf("%+v", byUser["6"])
	}
	if byUser["7"].Status != PairPending || byUser["7"].ExpiresAt.Sub(byUser["7"].CreatedAt) != time.Hour {
		t.Fatalf("%+v", byUser["7"])
	}
	if err := p.Revoke(ctx, "telegram", "6"); err != nil {
		t.Fatal(err)
	}
	if p.IsApproved(ctx, "telegram", "6") {
		t.Fatal("still approved after revoke")
	}
	if err := p.Revoke(ctx, "telegram", "6"); !errors.Is(err, ErrPairUserNotFound) {
		t.Fatal(err)
	}
	// List does not show revoked rows or expired codes.
	clk.advance(time.Hour)
	if rows, err := p.List(ctx, ""); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}

func TestRequestAfterRevoke(t *testing.T) {
	p, clk := pairStore(t)
	code := mustRequest(t, p, "6")
	if _, err := p.Approve(ctx, "telegram", code); err != nil {
		t.Fatal(err)
	}
	if err := p.Revoke(ctx, "telegram", "6"); err != nil {
		t.Fatal(err)
	}
	clk.advance(11 * time.Minute)
	again := mustRequest(t, p, "6")
	if uid, err := p.Approve(ctx, "telegram", again); err != nil || uid != "6" {
		t.Fatal(uid, err)
	}
}
