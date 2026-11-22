package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dnwandana/tether-agent/internal/config"
)

// The limits of the sign-in.
const (
	signInLife     = 5 * time.Minute
	requestEvery   = time.Minute
	pendingPerPeer = 3
	pendingTotal   = 10
	labelMaxRunes  = 64
	agentMaxRunes  = 200
	unknownDevice  = "Unknown device"
	bodyMaxBytes   = 4 << 10
)

// The texts of the sign-in errors.
const (
	textRequestRate    = "One sign-in request each minute is permitted. Wait, then try again."
	textRequestPending = "Too many sign-in requests wait for an answer. Try again later."
	textCodeLocked     = "Too many wrong codes. Try again in 10 minutes."
	textCodeLockedAll  = "Too many wrong codes from all devices. Try again later, or sign in on the host with a code from tether dashboard link."
	textBadCode        = "The code is not valid or has expired."
	textBadBody        = "The request body is not valid JSON."
	textServerError    = "The dashboard could not finish the request."
)

func init() { registerAPI(SignInStart{}, SignInPoll{}, SignInBody{}, SignInState{}) }

// SignInRequest is one request of a new device. Code is the 4-digit check code. State
// returns the state of the request now: pending, allowed, denied, or expired. A notifier
// calls it after an answer, because an earlier answer of another notifier wins. State can be
// nil.
type SignInRequest struct {
	ID, Code, Label, UserAgent, IP string
	Expires                        time.Time
	State                          func() string
}

// SignInNotifier shows a sign-in request to the owner and calls answer once with the tap.
// ctx ends at the expiry or at the first answer of any notifier.
type SignInNotifier interface {
	NotifySignIn(ctx context.Context, req SignInRequest, answer func(allow bool))
}

// SignInStart is the response of POST /auth/request.
type SignInStart struct {
	ID      string    `json:"id"`
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
}

// SignInPoll is the response of GET /auth/request/{id} and POST /auth/signin. State is
// pending, allowed, denied, or expired. Session is the header value, only once.
type SignInPoll struct {
	State   string `json:"state"`
	Session string `json:"session"`
}

// SignInBody is the JSON body of POST /auth/request and POST /auth/signin.
type SignInBody struct {
	Label string `json:"label"`
	Code  string `json:"code"`
}

// SignInState is the response of GET /auth/state.
type SignInState struct {
	DeviceID string    `json:"device_id"`
	Label    string    `json:"label"`
	Expires  time.Time `json:"expires"`
	TLS      bool      `json:"tls"`
}

// pendingReq is one request of a device that waits for an answer or for a poll.
type pendingReq struct {
	req    SignInRequest
	state  string
	cancel context.CancelFunc
}

// SignIn runs the two ways to sign in a device and the /auth/* routes.
type SignIn struct {
	st      *Store
	auth    *Auth
	cfg     config.DashboardConfig
	now     func() time.Time
	limiter *Limiter

	mu         sync.Mutex
	notifiers  []SignInNotifier
	requests   map[string]*pendingReq
	peerOf     map[string]string
	lastByPeer map[string]time.Time
}

// NewSignIn returns a SignIn.
func NewSignIn(st *Store, auth *Auth, cfg config.DashboardConfig, notifiers []SignInNotifier, now func() time.Time) *SignIn {
	return &SignIn{
		st: st, auth: auth, cfg: cfg, now: now,
		limiter:    NewLimiter(5, 10*time.Minute, 10*time.Minute, 30, time.Hour),
		notifiers:  append([]SignInNotifier(nil), notifiers...),
		requests:   map[string]*pendingReq{},
		peerOf:     map[string]string{},
		lastByPeer: map[string]time.Time{},
	}
}

// Register adds the /auth/* routes to mux.
func (s *SignIn) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/request", s.handleRequest)
	mux.HandleFunc("GET /auth/request/{id}", s.handlePoll)
	mux.HandleFunc("POST /auth/signin", s.handleCode)
	mux.Handle("POST /auth/signout", s.auth.Middleware(http.HandlerFunc(s.handleSignOut)))
	mux.Handle("GET /auth/state", s.auth.Middleware(http.HandlerFunc(s.handleState)))
}

// AddNotifier adds a notifier. It sees each request that starts after the call.
func (s *SignIn) AddNotifier(n SignInNotifier) {
	s.mu.Lock()
	s.notifiers = append(s.notifiers, n)
	s.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// checkHost writes 403 and returns false when the host or the origin is not allowed.
func (s *SignIn) checkHost(w http.ResponseWriter, r *http.Request) bool {
	err := s.auth.CheckHostOrigin(r)
	if err == nil {
		return true
	}
	text := textBadOrigin
	if errors.Is(err, errBadHost) {
		text = textBadHost
	}
	authError(w, http.StatusForbidden, text)
	return false
}

// readBody decodes the JSON body. An empty body is an empty SignInBody.
func readBody(w http.ResponseWriter, r *http.Request) (SignInBody, bool) {
	var b SignInBody
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, bodyMaxBytes)).Decode(&b)
	if err != nil && !errors.Is(err, io.EOF) {
		authError(w, http.StatusBadRequest, textBadBody)
		return b, false
	}
	return b, true
}

// cleanLabel trims the label, drops control characters, and cuts it at 64 runes.
func cleanLabel(l string) string {
	l = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, l))
	if l == "" {
		return unknownDevice
	}
	return cutRunes(l, labelMaxRunes)
}

func cutRunes(v string, n int) string {
	if r := []rune(v); len(r) > n {
		return string(r[:n])
	}
	return v
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// checkCode returns 4 random digits.
func checkCode() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 4)
	for i, v := range b {
		out[i] = '0' + v%10
	}
	return string(out), nil
}

// purgeLocked drops the requests that are expired. The caller holds s.mu.
func (s *SignIn) purgeLocked(now time.Time) {
	for id, p := range s.requests {
		if !now.Before(p.req.Expires) {
			p.cancel()
			delete(s.requests, id)
			delete(s.peerOf, id)
		}
	}
}

func (s *SignIn) handleRequest(w http.ResponseWriter, r *http.Request) {
	if !s.checkHost(w, r) {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	peer := s.auth.PeerIP(r)
	id, err1 := randHex(16)
	code, err2 := checkCode()
	if err1 != nil || err2 != nil {
		authError(w, http.StatusInternalServerError, textServerError)
		return
	}
	now := s.now()
	req := SignInRequest{ID: id, Code: code, Label: cleanLabel(body.Label),
		UserAgent: cutRunes(r.UserAgent(), agentMaxRunes), IP: peer, Expires: now.Add(signInLife)}
	ctx, cancel := context.WithDeadline(context.Background(), req.Expires)

	s.mu.Lock()
	s.purgeLocked(now)
	if last, ok := s.lastByPeer[peer]; ok && now.Sub(last) < requestEvery {
		s.mu.Unlock()
		cancel()
		authError(w, http.StatusTooManyRequests, textRequestRate)
		return
	}
	mine := 0
	total := 0
	for id, p := range s.requests {
		if p.state != "pending" || !now.Before(p.req.Expires) {
			continue
		}
		total++
		if s.peerOf[id] == peer {
			mine++
		}
	}
	if mine >= pendingPerPeer || total >= pendingTotal {
		s.mu.Unlock()
		cancel()
		authError(w, http.StatusTooManyRequests, textRequestPending)
		return
	}
	pr := &pendingReq{state: "pending", cancel: cancel}
	req.State = func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		if pr.state == "pending" && !s.now().Before(req.Expires) {
			return "expired"
		}
		return pr.state
	}
	pr.req = req
	s.requests[id] = pr
	s.peerOf[id] = peer
	s.lastByPeer[peer] = now
	for p, t := range s.lastByPeer {
		if now.Sub(t) >= requestEvery {
			delete(s.lastByPeer, p)
		}
	}
	notifiers := append([]SignInNotifier(nil), s.notifiers...)
	s.mu.Unlock()

	answer := func(allow bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if pr.state != "pending" || !s.now().Before(req.Expires) {
			return
		}
		pr.state = "denied"
		if allow {
			pr.state = "allowed"
		}
		pr.cancel()
	}
	for _, n := range notifiers {
		go n.NotifySignIn(ctx, req, answer)
	}
	writeJSON(w, SignInStart{ID: id, Code: code, Expires: req.Expires})
}

func (s *SignIn) handlePoll(w http.ResponseWriter, r *http.Request) {
	if !s.checkHost(w, r) {
		return
	}
	id := r.PathValue("id")
	now := s.now()
	s.mu.Lock()
	s.purgeLocked(now)
	p, ok := s.requests[id]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, SignInPoll{State: "expired"})
		return
	}
	state, req := p.state, p.req
	if state != "pending" {
		delete(s.requests, id)
		delete(s.peerOf, id)
	}
	s.mu.Unlock()
	if state != "allowed" {
		writeJSON(w, SignInPoll{State: state})
		return
	}
	s.issue(w, r, req.Label, req.UserAgent, req.IP, now)
}

// issue makes the device session, sets the cookie, and writes the allowed answer.
func (s *SignIn) issue(w http.ResponseWriter, r *http.Request, label, ua, ip string, now time.Time) {
	ttl := time.Duration(s.cfg.SessionHours) * time.Hour
	_, cookie, header, err := s.st.CreateSession(r.Context(), label, ua, ip, ttl, now)
	if err != nil {
		authError(w, http.StatusInternalServerError, textServerError)
		return
	}
	s.auth.SetSession(w, cookie)
	writeJSON(w, SignInPoll{State: "allowed", Session: header})
}

func (s *SignIn) handleCode(w http.ResponseWriter, r *http.Request) {
	if !s.checkHost(w, r) {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	peer := s.auth.PeerIP(r)
	now := s.now()
	release, err := s.limiter.Try(peer, now)
	if err != nil {
		text := textCodeLocked
		if errors.Is(err, errGlobalLocked) {
			text = textCodeLockedAll
		}
		authError(w, http.StatusTooManyRequests, text)
		return
	}
	if err := s.st.ConsumeCode(r.Context(), body.Code, now); err != nil {
		bad := errors.Is(err, ErrBadCode)
		release(bad)
		if bad {
			authError(w, http.StatusUnauthorized, textBadCode)
			return
		}
		authError(w, http.StatusInternalServerError, textServerError)
		return
	}
	release(false)
	s.issue(w, r, cleanLabel(body.Label), cutRunes(r.UserAgent(), agentMaxRunes), peer, now)
}

func (s *SignIn) handleSignOut(w http.ResponseWriter, r *http.Request) {
	d, ok := DeviceFrom(r.Context())
	if !ok {
		authError(w, http.StatusUnauthorized, textSignIn)
		return
	}
	if err := s.st.Revoke(r.Context(), d.ID); err != nil {
		authError(w, http.StatusInternalServerError, textServerError)
		return
	}
	s.auth.SetSession(w, "")
	writeJSON(w, map[string]string{})
}

func (s *SignIn) handleState(w http.ResponseWriter, r *http.Request) {
	d, ok := DeviceFrom(r.Context())
	if !ok {
		authError(w, http.StatusUnauthorized, textSignIn)
		return
	}
	writeJSON(w, SignInState{DeviceID: d.ID, Label: d.Label, Expires: d.Expires,
		TLS: s.cfg.TLS() || strings.HasPrefix(s.cfg.PublicURL, "https://")})
}
