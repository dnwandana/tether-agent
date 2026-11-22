package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
)

// notified is one NotifySignIn call.
type notified struct {
	ctx    context.Context
	req    SignInRequest
	answer func(allow bool)
}

// fakeNotifier sends each call to got.
type fakeNotifier struct{ got chan notified }

func newFakeNotifier() *fakeNotifier { return &fakeNotifier{got: make(chan notified, 16)} }

func (f *fakeNotifier) NotifySignIn(ctx context.Context, req SignInRequest, answer func(allow bool)) {
	f.got <- notified{ctx, req, answer}
}

func (f *fakeNotifier) next(t *testing.T) notified {
	t.Helper()
	select {
	case n := <-f.got:
		return n
	case <-time.After(2 * time.Second):
		t.Fatal("no NotifySignIn call")
	}
	return notified{}
}

// signInFixture is a SignIn with a clock that a test moves.
type signInFixture struct {
	st  *Store
	mux *http.ServeMux
	mu  sync.Mutex
	now time.Time
}

func newSignInFixture(t *testing.T, notifiers ...SignInNotifier) (*signInFixture, *SignIn) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	f := &signInFixture{st: NewStore(d), mux: http.NewServeMux(), now: time.UnixMilli(1_763_640_000_000)}
	clock := func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
	cfg := config.Default().Dashboard
	s := NewSignIn(f.st, NewAuth(cfg, f.st, clock), cfg, notifiers, clock)
	s.Register(f.mux)
	return f, s
}

func (f *signInFixture) advance(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }

// do sends one request from peer to the fixture. cookie and header can be "".
func (f *signInFixture) do(method, path, peer, body, cookie, header string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, strings.NewReader(body))
	r.Host = "127.0.0.1:8787"
	r.RemoteAddr = peer + ":40000"
	r.Header.Set("User-Agent", "Mozilla/5.0 (Android)")
	if method != "GET" {
		r.Header.Set("Origin", "http://127.0.0.1:8787")
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	if header != "" {
		r.Header.Set(HeaderName, header)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, r)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %q: %v", rec.Code, rec.Body.String(), err)
	}
	return v
}

func cookieValue(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c.Value
		}
	}
	return ""
}

func TestSignInTapAllows(t *testing.T) {
	tg, tray := newFakeNotifier(), newFakeNotifier()
	f, _ := newSignInFixture(t, tg, tray)
	rec := f.do("POST", "/auth/request", "10.8.0.5", `{"label":"  Chrome on Android\u0007 "}`, "", "")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	start := decodeBody[SignInStart](t, rec)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(start.ID) || !regexp.MustCompile(`^[0-9]{4}$`).MatchString(start.Code) ||
		!start.Expires.Equal(f.now.Add(5*time.Minute)) {
		t.Fatalf("%+v", start)
	}
	a, b := tg.next(t), tray.next(t)
	want := SignInRequest{ID: start.ID, Code: start.Code, Label: "Chrome on Android", UserAgent: "Mozilla/5.0 (Android)",
		IP: "10.8.0.5"}
	for _, got := range []SignInRequest{a.req, b.req} {
		exp, state := got.Expires, got.State
		got.Expires, got.State = time.Time{}, nil
		if !reflect.DeepEqual(got, want) || !exp.Equal(start.Expires) || state == nil || state() != "pending" {
			t.Fatalf("%+v", got)
		}
	}
	if p := decodeBody[SignInPoll](t, f.do("GET", "/auth/request/"+start.ID, "10.8.0.5", "", "", "")); p.State != "pending" || p.Session != "" {
		t.Fatalf("%+v", p)
	}
	a.answer(true)
	select {
	case <-b.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the first answer must end the context of the other notifiers")
	}
	b.answer(false) // too late
	rec = f.do("GET", "/auth/request/"+start.ID, "10.8.0.5", "", "", "")
	p := decodeBody[SignInPoll](t, rec)
	cookie := cookieValue(rec)
	if p.State != "allowed" || p.Session == "" || cookie == "" {
		t.Fatalf("%+v %q", p, cookie)
	}
	d, err := f.st.Lookup(context.Background(), cookie, p.Session, f.now)
	if err != nil || d.Label != "Chrome on Android" || d.IP != "10.8.0.5" || !d.Expires.Equal(f.now.Add(24*time.Hour)) {
		t.Fatalf("%+v %v", d, err)
	}
	if p := decodeBody[SignInPoll](t, f.do("GET", "/auth/request/"+start.ID, "10.8.0.5", "", "", "")); p.State != "expired" || p.Session != "" {
		t.Fatalf("a request gives one session: %+v", p)
	}
}

func TestSignInDenyAndExpiry(t *testing.T) {
	n := newFakeNotifier()
	f, _ := newSignInFixture(t, n)
	denied := decodeBody[SignInStart](t, f.do("POST", "/auth/request", "10.8.0.5", `{"label":"A"}`, "", ""))
	n.next(t).answer(false)
	if p := decodeBody[SignInPoll](t, f.do("GET", "/auth/request/"+denied.ID, "10.8.0.5", "", "", "")); p.State != "denied" {
		t.Fatalf("%+v", p)
	}
	late := decodeBody[SignInStart](t, f.do("POST", "/auth/request", "10.8.0.6", `{"label":""}`, "", ""))
	got := n.next(t)
	if got.req.Label != "Unknown device" {
		t.Fatalf("%+v", got.req)
	}
	f.advance(5*time.Minute + time.Second)
	if p := decodeBody[SignInPoll](t, f.do("GET", "/auth/request/"+late.ID, "10.8.0.6", "", "", "")); p.State != "expired" {
		t.Fatalf("%+v", p)
	}
	got.answer(true) // after the expiry
	if p := decodeBody[SignInPoll](t, f.do("GET", "/auth/request/"+late.ID, "10.8.0.6", "", "", "")); p.State != "expired" || p.Session != "" {
		t.Fatalf("an answer after the expiry must not sign in: %+v", p)
	}
	if p := decodeBody[SignInPoll](t, f.do("GET", "/auth/request/unknown", "10.8.0.6", "", "", "")); p.State != "expired" {
		t.Fatalf("%+v", p)
	}
}

func TestSignInRequestLimits(t *testing.T) {
	f, _ := newSignInFixture(t)
	req := func(peer string) int { return f.do("POST", "/auth/request", peer, `{"label":"x"}`, "", "").Code }
	if req("10.0.0.1") != 200 {
		t.Fatal("first request")
	}
	rec := f.do("POST", "/auth/request", "10.0.0.1", `{"label":"x"}`, "", "")
	if rec.Code != 429 || rec.Body.String() != `{"error":"One sign-in request each minute is permitted. Wait, then try again."}`+"\n" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if req("10.0.0.2") != 200 {
		t.Fatal("another peer is not limited")
	}
	f.advance(61 * time.Second)
	if req("10.0.0.1") != 200 {
		t.Fatal("second request after a minute")
	}
	f.advance(61 * time.Second)
	if req("10.0.0.1") != 200 {
		t.Fatal("third request after two minutes")
	}
	f.advance(61 * time.Second)
	rec = f.do("POST", "/auth/request", "10.0.0.1", `{"label":"x"}`, "", "")
	if rec.Code != 429 || rec.Body.String() != `{"error":"Too many sign-in requests wait for an answer. Try again later."}`+"\n" {
		t.Fatal("3 pending requests for each peer:", rec.Code, rec.Body.String())
	}
	// 10.0.0.1 has 3 and 10.0.0.2 has 1. Six more peers make 10 in total.
	for i := 3; i <= 8; i++ {
		if c := req("10.0.0." + string(rune('0'+i))); c != 200 {
			t.Fatal(i, c)
		}
	}
	if req("10.0.0.9") != 429 {
		t.Fatal("10 pending requests in total")
	}
	f.advance(5*time.Minute + time.Second)
	if req("10.0.0.9") != 200 {
		t.Fatal("expired requests do not count")
	}
}

func TestCodeSignIn(t *testing.T) {
	f, _ := newSignInFixture(t)
	code, err := f.st.AddCode(context.Background(), 5*time.Minute, f.now)
	if err != nil {
		t.Fatal(err)
	}
	rec := f.do("POST", "/auth/signin", "10.8.0.7", `{"code":"`+code+`","label":"Laptop"}`, "", "")
	p := decodeBody[SignInPoll](t, rec)
	if rec.Code != 200 || p.State != "allowed" || p.Session == "" || cookieValue(rec) == "" {
		t.Fatalf("%d %+v", rec.Code, p)
	}
	if d, err := f.st.Lookup(context.Background(), cookieValue(rec), p.Session, f.now); err != nil || d.Label != "Laptop" {
		t.Fatalf("%+v %v", d, err)
	}
	rec = f.do("POST", "/auth/signin", "10.8.0.7", `{"code":"`+code+`"}`, "", "")
	if rec.Code != 401 || rec.Body.String() != `{"error":"The code is not valid or has expired."}`+"\n" {
		t.Fatal("a code works once:", rec.Code, rec.Body.String())
	}
}

func TestCodeSignInLockout(t *testing.T) {
	f, _ := newSignInFixture(t)
	good, _ := f.st.AddCode(context.Background(), 5*time.Minute, f.now)
	for range 5 {
		if c := f.do("POST", "/auth/signin", "10.8.0.8", `{"code":"AAAA-AAAA"}`, "", "").Code; c != 401 {
			t.Fatal(c)
		}
	}
	rec := f.do("POST", "/auth/signin", "10.8.0.8", `{"code":"`+good+`"}`, "", "")
	if rec.Code != 429 || rec.Body.String() != `{"error":"Too many wrong codes. Try again in 10 minutes."}`+"\n" {
		t.Fatal("five wrong codes lock that peer:", rec.Code, rec.Body.String())
	}
	if c := f.do("POST", "/auth/signin", "10.8.0.9", `{"code":"`+good+`"}`, "", "").Code; c != 200 {
		t.Fatal("another peer is not locked:", c)
	}
}

func TestSignOutAndState(t *testing.T) {
	f, _ := newSignInFixture(t)
	_, cookie, header, _ := f.st.CreateSession(context.Background(), "Phone", "ua", "10.8.0.5", 24*time.Hour, f.now)
	rec := f.do("GET", "/auth/state", "10.8.0.5", "", cookie, header)
	st := decodeBody[SignInState](t, rec)
	if rec.Code != 200 || st.Label != "Phone" || st.DeviceID == "" || st.TLS {
		t.Fatalf("%d %+v", rec.Code, st)
	}
	if c := f.do("GET", "/auth/state", "10.8.0.5", "", cookie, "").Code; c != 401 {
		t.Fatal("the cookie alone must get 401:", c)
	}
	rec = f.do("POST", "/auth/signout", "10.8.0.5", "", cookie, header)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if cs := rec.Result().Cookies(); len(cs) != 1 || cs[0].MaxAge != -1 {
		t.Fatalf("sign-out must clear the cookie: %+v", cs)
	}
	if c := f.do("GET", "/auth/state", "10.8.0.5", "", cookie, header).Code; c != 401 {
		t.Fatal("a signed-out session must get 401:", c)
	}
}

func TestSignInChecksHostAndOrigin(t *testing.T) {
	f, s := newSignInFixture(t)
	r := httptest.NewRequest("POST", "http://evil.example/auth/request", strings.NewReader(`{"label":"x"}`))
	r.Host = "evil.example"
	r.RemoteAddr = "10.8.0.5:1"
	r.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:8787/auth/signin", strings.NewReader(`{"code":"x"}`))
	r.Host = "127.0.0.1:8787"
	r.RemoteAddr = "10.8.0.5:1"
	r.Header.Set("Origin", "http://evil.example")
	rec = httptest.NewRecorder()
	f.mux.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatal("a POST from another origin must get 403:", rec.Code)
	}
	// A notifier that comes later gets the next request.
	late := newFakeNotifier()
	s.AddNotifier(late)
	f.do("POST", "/auth/request", "10.8.0.5", `{"label":"x"}`, "", "")
	late.next(t)
}

// An answered request does not count for the pending limits.
func TestSignInAnsweredRequestsDoNotCount(t *testing.T) {
	n := newFakeNotifier()
	f, _ := newSignInFixture(t, n)
	for i := range pendingPerPeer + 1 {
		if rec := f.do("POST", "/auth/request", "10.0.0.1", `{"label":"x"}`, "", ""); rec.Code != 200 {
			t.Fatal(i, rec.Code, rec.Body.String())
		}
		n.next(t).answer(false)
		f.advance(61 * time.Second)
	}
}

// The global limit does not stop a loopback peer, and it has its own text.
func TestCodeSignInGlobalLimitSkipsLoopback(t *testing.T) {
	f, _ := newSignInFixture(t)
	good, _ := f.st.AddCode(context.Background(), 5*time.Minute, f.now)
	for p := range 6 {
		for range 5 {
			if c := f.do("POST", "/auth/signin", "203.0.113."+strconv.Itoa(p+1), `{"code":"AAAA-AAAA"}`, "", "").Code; c != 401 {
				t.Fatal(p, c)
			}
		}
	}
	rec := f.do("POST", "/auth/signin", "203.0.113.50", `{"code":"`+good+`"}`, "", "")
	if rec.Code != 429 || rec.Body.String() != `{"error":"`+textCodeLockedAll+`"}`+"\n" {
		t.Fatal("30 wrong codes stop a remote peer with the global text:", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/auth/signin", "127.0.0.1", `{"code":"`+good+`"}`, "", ""); rec.Code != 200 {
		t.Fatal("the global limit must not stop a loopback peer:", rec.Code, rec.Body.String())
	}
}

// Parallel wrong codes of one peer do not pass the limit before the first failure is recorded.
func TestCodeSignInParallelTries(t *testing.T) {
	f, _ := newSignInFixture(t)
	start := make(chan struct{})
	codes := make(chan int, 40)
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			<-start
			codes <- f.do("POST", "/auth/signin", "10.8.0.8", `{"code":"AAAA-AAAA"}`, "", "").Code
		})
	}
	close(start)
	wg.Wait()
	close(codes)
	wrong := 0
	for c := range codes {
		switch c {
		case 401:
			wrong++
		case 429:
		default:
			t.Fatal(c)
		}
	}
	if wrong > 5 {
		t.Fatalf("%d wrong codes reached the store; the limit is 5", wrong)
	}
}

// A tap on the dashboard card after another notifier answered shows the real answer.
func TestSignInCardShowsFinalState(t *testing.T) {
	n := newFakeNotifier()
	f, _ := newSignInFixture(t, n)
	cards, _, ev := cardsFixture(t, nil, time.Now)
	f.do("POST", "/auth/request", "10.8.0.5", `{"label":"A"}`, "", "")
	got := n.next(t)
	got.answer(false) // Telegram denies first.
	cards.NotifySignIn(context.Background(), got.req, got.answer)
	card := cardOf(t, nextEvent(t, ev), "card")
	if err := cards.Answer(context.Background(), card.ID, CardAnswer{Answer: "allow"}); err != nil {
		t.Fatal(err)
	}
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != signInDenied {
		t.Fatalf("the card must show the real answer: %q", done.Header)
	}
	// The request expires before the tap.
	f.advance(61 * time.Second)
	f.do("POST", "/auth/request", "10.8.0.5", `{"label":"B"}`, "", "")
	got = n.next(t)
	cards.NotifySignIn(context.Background(), got.req, got.answer)
	card = cardOf(t, nextEvent(t, ev), "card")
	f.advance(5*time.Minute + time.Second)
	if err := cards.Answer(context.Background(), card.ID, CardAnswer{Answer: "allow"}); err != nil {
		t.Fatal(err)
	}
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != signInClosed {
		t.Fatalf("an expired request must not show allowed: %q", done.Header)
	}
}

// When another notifier answers, the dashboard card closes with that answer.
func TestSignInCardClosesWithOtherAnswer(t *testing.T) {
	n := newFakeNotifier()
	f, s := newSignInFixture(t, n)
	f.mu.Lock()
	f.now = time.Now()
	f.mu.Unlock()
	cards, _, ev := cardsFixture(t, nil, time.Now)
	s.AddNotifier(cards)
	f.do("POST", "/auth/request", "10.8.0.5", `{"label":"A"}`, "", "")
	cardOf(t, nextEvent(t, ev), "card")
	n.next(t).answer(true)
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != signInAllowed {
		t.Fatalf("%q", done.Header)
	}
}
