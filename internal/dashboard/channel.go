// Package dashboard is the web dashboard of the gateway: one more gateway channel with an HTTP
// server, device sessions, an SSE event hub, a renderer, a card prompter, and JSON handlers.
// It is off by default and exists only in the gateway role.
package dashboard

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dnwandana/tether-agent/internal/gateway"
)

// The names of the dashboard channel.
const (
	ChannelName = "dashboard"
	SessionKey  = "dashboard:owner"
	OwnerChatID = "owner"
)

// streamQueueBytes is the queue limit of each event stream.
const streamQueueBytes = 1 << 20

// statusEvery is the time between two status checks. The tests change it.
var statusEvery = 15 * time.Second

// textNotFound is the 404 text of an unknown API path.
const textNotFound = "Not found."

// defaultQueueMax is the queue size that the queue event shows when the config is not known.
const defaultQueueMax = 20

// Channel is the dashboard channel of the gateway.
type Channel struct {
	d      Deps
	log    *slog.Logger
	store  *Store
	auth   *Auth
	signIn *SignIn
	hub    *Hub
	turn   *TurnState
	cards  *Cards

	mu         sync.Mutex
	gw         *gateway.Gateway
	workCtx    context.Context
	routes     []Routes
	files      *Files
	srv        *http.Server
	addr       net.Addr
	lastStatus *Status
	// lastSession is the last session view that the session event sent.
	lastSession *SessionView

	// refreshReq holds one waiting refresh request. The worker reads it.
	refreshReq  chan struct{}
	refreshOnce sync.Once
	// queueReq holds one waiting queue request. The queue worker reads it.
	queueReq  chan struct{}
	queueOnce sync.Once
	// queueOf reads the queue of a key. Nil uses Gateway.Queue. The tests set it.
	queueOf  func(key string) []gateway.QueueItem
	quit     chan struct{} // closed by Shutdown
	quitOnce sync.Once

	ready    chan struct{} // closed when the listener is up
	readyOne sync.Once
	stopping atomic.Bool
	inflight atomic.Int64
}

var (
	_ gateway.Channel         = (*Channel)(nil)
	_ gateway.InflightChannel = (*Channel)(nil)
)

// NewChannel builds the channel. It does no network work and starts no listener.
func NewChannel(d Deps) (*Channel, error) {
	if d.DB == nil {
		return nil, errors.New("dashboard: the channel needs a database")
	}
	if d.Sessions == nil {
		return nil, errors.New("dashboard: the channel needs a session store")
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	c := &Channel{d: d, log: log, ready: make(chan struct{}), refreshReq: make(chan struct{}, 1), queueReq: make(chan struct{}, 1), quit: make(chan struct{}), workCtx: context.Background()}
	c.store = NewStore(d.DB)
	c.auth = NewAuth(d.Cfg, c.store, time.Now)
	c.hub = NewHub(streamQueueBytes)
	c.turn = NewTurnState()
	c.cards = NewCards(c.hub, d.SaveSecret, time.Now)
	if d.Redact != nil {
		c.cards.SetRedact(d.Redact)
	}
	c.signIn = NewSignIn(c.store, c.auth, d.Cfg, []SignInNotifier{c.cards}, time.Now)
	c.routes = append(c.routes, d.Routes...)
	return c, nil
}

// Name returns the channel name.
func (c *Channel) Name() string { return ChannelName }

// Cards returns the card prompter.
func (c *Channel) Cards() *Cards { return c.cards }

// Hub returns the event hub.
func (c *Channel) Hub() *Hub { return c.hub }

// SignIn returns the sign-in handler.
func (c *Channel) SignIn() *SignIn { return c.signIn }

// Gateway returns the gateway that Run got, or nil before Run.
func (c *Channel) Gateway() *gateway.Gateway {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gw
}

// WorkCtx returns the work context. It is the background context before SetTurnContext.
func (c *Channel) WorkCtx() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.workCtx
}

// SetTurnContext sets the work context. The streams and the status loop end with it.
func (c *Channel) SetTurnContext(ctx context.Context) {
	c.mu.Lock()
	c.workCtx = ctx
	c.mu.Unlock()
}

// Stopping reports whether the receive context ended.
func (c *Channel) Stopping() bool { return c.stopping.Load() }

// AddRoutes adds routes to the API mux. Call it before Run.
func (c *Channel) AddRoutes(r ...Routes) {
	c.mu.Lock()
	c.routes = append(c.routes, r...)
	c.mu.Unlock()
}

// SetFiles sets the file links. The renderer and Send use them.
func (c *Channel) SetFiles(f *Files) {
	c.mu.Lock()
	c.files = f
	c.mu.Unlock()
}

// Inflight returns the number of messages that the channel got and did not submit yet.
func (c *Channel) Inflight() int { return int(c.inflight.Load()) }

// BeginInflight adds 1 to the inflight count. The done func takes 1 away, only once.
func (c *Channel) BeginInflight() (done func()) {
	c.inflight.Add(1)
	var once sync.Once
	return func() { once.Do(func() { c.inflight.Add(-1) }) }
}

// NewRenderer returns the renderer of one turn or one command reply.
func (c *Channel) NewRenderer(requestID string) gateway.Renderer {
	c.mu.Lock()
	files := c.files
	c.mu.Unlock()
	rd := RendererDeps{Hub: c.hub, Turn: c.turn, Redact: c.d.Redact, Partial: c.d.Partial,
		TurnCost: c.d.TurnCost, RequestID: requestID}
	if rd.Partial == nil {
		rd.Partial = func(string) int { return 0 }
	}
	if rd.TurnCost == nil {
		rd.TurnCost = func(context.Context, string, time.Time) (CostView, error) { return CostView{}, nil }
	}
	if files != nil {
		rd.Rewrite = func(text string) (string, []FileRef) { return files.Rewrite(text, false) }
	}
	r := NewRenderer(rd).(*renderer)
	r.onSession = func() { c.refreshSession(context.Background()) }
	return r
}

// sessionTimeout limits the reads of one session event.
const sessionTimeout = 10 * time.Second

// sessionView reads the view of the active session of the owner.
func (c *Channel) sessionView(ctx context.Context) SessionView {
	if c.d.Session != nil {
		return c.d.Session(ctx)
	}
	var v SessionView
	if sess, err := c.d.Sessions.Active(ctx, SessionKey); err == nil && sess != nil {
		v.ID, v.Title = sess.ID, sess.Title
	}
	return v
}

// refreshSession asks the refresh worker for a session refresh. It does not wait. A request
// that comes while one waits merges with it. The context is not used: the worker has its own.
func (c *Channel) refreshSession(context.Context) {
	c.refreshOnce.Do(func() { go c.refreshWorker() })
	select {
	case c.refreshReq <- struct{}{}:
	default:
	}
}

// refreshWorker runs the refreshes one at a time, so an older view is never sent last.
// It ends when Shutdown closes quit.
func (c *Channel) refreshWorker() {
	for {
		select {
		case <-c.quit:
			return
		case <-c.refreshReq:
			c.refreshNow()
		}
	}
}

// refreshNow sends the session event when the view of the active session changed, for
// example after /new, /resume, /rename, a model change, or the first turn of a new session.
func (c *Channel) refreshNow() {
	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()
	view := c.sessionView(ctx)
	c.mu.Lock()
	same := c.lastSession != nil && sameSessionView(*c.lastSession, view)
	c.lastSession = &view
	c.mu.Unlock()
	if !same {
		c.hub.Publish(Event{Name: "session", Data: view})
	}
}

// sameSessionView reports whether a and b show the same thing.
func sameSessionView(a, b SessionView) bool {
	if a.ID != b.ID || a.Title != b.Title || a.Model != b.Model || a.Entry != b.Entry || a.Effort != b.Effort || a.GoalLine != b.GoalLine {
		return false
	}
	if a.Context == nil || b.Context == nil {
		return a.Context == b.Context
	}
	return *a.Context == *b.Context
}

// Inbound returns the inbound message of the owner with text.
func (c *Channel) Inbound(text string) gateway.Inbound {
	return gateway.Inbound{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID, Text: text, IsText: true,
		Target: gateway.Target{Source: ChannelName, ChatID: OwnerChatID}, Owner: true, SenderID: OwnerChatID}
}

// Send shows a message that has no live renderer as a notice.
func (c *Channel) Send(ctx context.Context, to gateway.Target, msg gateway.Outbound) error {
	if to.Source != ChannelName {
		return fmt.Errorf("dashboard: the target %q is not a dashboard target", to.Source)
	}
	text := msg.Text
	if c.d.Redact != nil {
		text = c.d.Redact(text)
	}
	c.mu.Lock()
	files := c.files
	c.mu.Unlock()
	for _, p := range msg.Files {
		// A file name can hold Markdown characters such as "]", which would break the link.
		name := gateway.EscapeMarkdown(filepath.Base(p))
		line := "📎 " + name
		if files != nil {
			if id, ok := files.Link(p, false); ok {
				line = "[📎 " + name + "](/api/files/" + id + ")"
			}
		}
		if text != "" {
			text += "\n"
		}
		text += line
	}
	n := Notice{Text: text, At: time.Now()}
	c.hub.publishWith(Event{Name: "notice", Data: n}, func() { c.hub.AddNotice(text) })
	return nil
}

// notFound writes the 404 JSON body.
func notFound(w http.ResponseWriter, r *http.Request) {
	authError(w, http.StatusNotFound, textNotFound)
}

// handler builds the mux of all routes.
func (c *Channel) handler() http.Handler {
	c.mu.Lock()
	routes := append([]Routes(nil), c.routes...)
	c.mu.Unlock()
	api := http.NewServeMux()
	api.HandleFunc("GET /api/events", c.events)
	api.HandleFunc("/api/", notFound)
	for _, r := range routes {
		r.Register(api)
	}
	mux := http.NewServeMux()
	mux.Handle("/", StaticHandler(DistFS()))
	mux.HandleFunc("/auth/", notFound)
	c.signIn.Register(mux)
	mux.Handle("/api/", c.auth.Middleware(api))
	return mux
}

// currentStatus returns the cached status, or reads Deps.Status when nothing is cached.
func (c *Channel) currentStatus(ctx context.Context) Status {
	c.mu.Lock()
	cached := c.lastStatus
	c.mu.Unlock()
	var s Status
	if cached != nil {
		s = *cached
	} else if c.d.Status != nil {
		s = c.d.Status(ctx)
	}
	s.TLS = c.d.Cfg.TLS() || strings.HasPrefix(c.d.Cfg.PublicURL, "https://")
	return s
}

// events serves GET /api/events. The hub adds the stream and takes the memory parts of the
// snapshot first. Then fill reads the database parts. An event that comes during these reads
// waits in the stream queue and goes out after hello, so the page cannot miss it.
func (c *Channel) events(w http.ResponseWriter, r *http.Request) {
	status := c.currentStatus(r.Context())
	fill := func(r *http.Request, sn *Snapshot) {
		ctx := r.Context()
		if sess, err := c.d.Sessions.Active(ctx, SessionKey); err != nil {
			c.log.Warn("dashboard: cannot read the active session", "err", err)
		} else if sess != nil {
			sn.SessionID = sess.ID
		}
		if sn.SessionID != "" {
			if err := c.d.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM messages WHERE session_id = ?`, sn.SessionID).Scan(&sn.LastMessageID); err != nil {
				c.log.Warn("dashboard: cannot read the last message", "err", err)
			}
		}
		if c.d.Session != nil {
			sn.Session = c.d.Session(ctx)
		}
	}
	c.hub.handler(func() Snapshot {
		return Snapshot{Turn: c.turn.View(), Cards: c.cards.Open(), Status: status,
			Notices: c.hub.Notices(), Answered: c.hub.Answered(), BuildID: BuildID()}
	}, fill)(w, r)
}

// statusLoop sends the status while a turn runs, and when it changed, until ctx ends.
func (c *Channel) statusLoop(ctx context.Context) {
	t := time.NewTicker(statusEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if c.hub.Streams() == 0 || c.d.Status == nil {
			continue
		}
		s := c.d.Status(ctx)
		s.TLS = c.d.Cfg.TLS() || strings.HasPrefix(c.d.Cfg.PublicURL, "https://")
		c.mu.Lock()
		changed := c.lastStatus == nil || *c.lastStatus != s
		c.lastStatus = &s
		c.mu.Unlock()
		if changed || c.turn.View() != nil {
			c.hub.Publish(Event{Name: "status", Data: s})
		}
	}
}

// queueChanged asks the queue worker to send the queue of the dashboard key. It does not wait.
// A request that comes while one waits merges with it.
func (c *Channel) queueChanged(key string) {
	if key != SessionKey {
		return
	}
	c.queueOnce.Do(func() { go c.queueWorker() })
	select {
	case c.queueReq <- struct{}{}:
	default:
	}
}

// queueWorker sends the queue one time for each request, one at a time. Each send reads the
// queue again, so an older queue is never sent last. It ends when Shutdown closes quit.
func (c *Channel) queueWorker() {
	for {
		select {
		case <-c.quit:
			return
		case <-c.queueReq:
			c.queueNow()
		}
	}
}

// queueNow sends the queue of the dashboard key and keeps it in the turn state.
func (c *Channel) queueNow() {
	c.mu.Lock()
	gw, read := c.gw, c.queueOf
	c.mu.Unlock()
	if read == nil {
		if gw == nil {
			return
		}
		read = gw.Queue
	}
	items := []QueueItem{}
	for _, q := range read(SessionKey) {
		// The preview is a part of the text of the user or of the goal, so it can hold a secret.
		preview := q.Preview
		if c.d.Redact != nil {
			preview = c.d.Redact(preview)
		}
		items = append(items, QueueItem{Kind: q.Kind, Preview: preview})
	}
	max := defaultQueueMax
	if gw != nil && gw.Deps != nil && gw.Deps.Cfg != nil && gw.Deps.Cfg.Gateway.MaxQueuedMessages > 0 {
		max = gw.Deps.Cfg.Gateway.MaxQueuedMessages
	}
	c.hub.publishWith(Event{Name: "queue", Data: map[string]any{"items": items, "max": max}},
		func() { c.turn.SetQueue(items) })
}

// RestartProgress is the data of a restart event during the drain of a gateway stop.
// Reason is "stopping", the same as the first restart event.
type RestartProgress struct {
	Reason   string `json:"reason"`
	ElapsedS int    `json:"elapsed_s"`
	DrainS   int    `json:"drain_s"`
	Turns    int    `json:"turns"`
	Goals    int    `json:"goals"`
}

func init() { registerAPI(RestartProgress{}) }

// PublishRestart sends the drain progress of a stop to the open pages. The hub drops it
// after it closes.
func (c *Channel) PublishRestart(p RestartProgress) {
	c.hub.Publish(Event{Name: "restart", Data: p})
}

// Run serves the dashboard until ctx ends. The server and the streams stay until the work
// context ends.
func (c *Channel) Run(ctx context.Context, gw *gateway.Gateway) error {
	c.mu.Lock()
	c.gw = gw
	work := c.workCtx
	c.mu.Unlock()
	if gw != nil {
		gw.OnQueueChange(c.queueChanged)
	}
	ln, err := ListenRetry(ctx, c.d.Cfg.Listen, c.log)
	if err != nil {
		c.log.Error("dashboard: cannot listen; the gateway runs with no dashboard", "addr", c.d.Cfg.Listen, "err", err)
		return err
	}
	if c.d.Cfg.TLS() {
		rel, err := NewCertReloader(c.d.Cfg.TLSCert, c.d.Cfg.TLSKey)
		if err != nil {
			_ = ln.Close()
			c.log.Error("dashboard: cannot load the certificate; the gateway runs with no dashboard", "err", err)
			return err
		}
		ln = tls.NewListener(ln, &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: rel.GetCertificate})
	}
	srv := NewHTTPServer(SecurityHeaders(c.handler(), c.d.Cfg.TLS()))
	c.mu.Lock()
	c.srv, c.addr = srv, ln.Addr()
	c.mu.Unlock()
	c.readyOne.Do(func() { close(c.ready) })
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("dashboard: the server stopped", "err", err)
		}
	}()
	go c.statusLoop(work)
	go func() {
		<-work.Done()
		c.hub.Close()
	}()
	<-ctx.Done()
	c.stopping.Store(true)
	c.hub.Publish(Event{Name: "restart", Data: map[string]string{"reason": "stopping"}})
	return nil
}

// Shutdown closes the streams, then stops the server. It returns nil when no server runs.
func (c *Channel) Shutdown(ctx context.Context) error {
	c.quitOnce.Do(func() { close(c.quit) })
	c.hub.Close()
	c.mu.Lock()
	srv := c.srv
	c.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}
