// Package agent runs the agent loop: model calls, tool rounds, the steer queue, and the
// system prompt.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/pricing"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// NoteMaxTurns is the system note of the request before the last allowed tool round.
const NoteMaxTurns = "Stop calling tools and give your final answer now."

// NoteStopped ends the final text when the turn used all its tool rounds.
const NoteStopped = "[stopped: max tool rounds reached]"

// previewMax is the maximum number of characters in a tool preview.
const previewMax = 40

// streamIdleTimeout ends a model call when its stream sends no event for this time. A
// connection can stay open and send nothing. That connection must not hang the turn.
// Hermes uses 180 s for small contexts and up to 300 s for large contexts. Tests change it.
var streamIdleTimeout = 300 * time.Second

// Sink receives the live output of a turn. The loop calls it from the goroutine of the
// turn, except ToolStart and ToolEnd, which a parallel round calls under a lock.
type Sink interface {
	// Delta receives a part of the streamed answer text.
	Delta(text string)
	// ToolStart tells that a tool call starts. preview is the first string argument.
	ToolStart(name, preview string)
	// ToolEnd tells that a tool call ended.
	ToolEnd(name string, isError bool)
	// Boundary tells that the streamed text ends because tool calls follow.
	Boundary()
	// Notice receives a short status text for the user.
	Notice(text string)
}

// Agent runs the turns of all sessions. One Agent is shared by all channels.
type Agent struct {
	// Prov is the default provider.
	Prov provider.Provider
	// Providers holds every provider entry by name, for a session model override.
	Providers map[string]provider.Provider
	Tools     *tools.Registry
	Store     *sessions.Store
	// DB is the state.db handle. The steer injection updates a stored tool message with it.
	DB  *db.DB
	Cfg *config.Config
	Log *slog.Logger
	// SpillDir receives the full text of each truncated tool output.
	SpillDir string
	// Redactor masks the secrets in each tool result. A nil Redactor applies the patterns only.
	Redactor *security.Redactor
	// Prices gives the cost of each response. A nil Prices stores no cost.
	Prices *pricing.Service
	// Prompts holds the frozen system prompt of each session. RunTurn uses it when
	// Turn.System is empty. A nil Prompts sends no system prompt.
	Prompts *PromptCache
	// Memory gives the MEMORY and USER PROFILE sections of the system prompt.
	Memory *memory.Store
	// Skills gives the skills index of the system prompt.
	Skills *tools.SkillCatalog
	// MCPGen returns the MCP generation. A /reload-mcp changes it, so the prompt is rebuilt.
	MCPGen func() uint64
	// WorkDir is the working directory in the system prompt. Empty uses the process
	// working directory.
	WorkDir string
	// ChatInfo describes the chat of a session for the system prompt, for example the
	// chat type and the topic on Telegram. It can be nil.
	ChatInfo func(s *sessions.Session) string
	// Compactor compacts a long session before a model call. A nil Compactor never
	// compacts, but the view still applies the stored compactions.
	Compactor *ctxmgr.Compactor

	// Reviews counts the turns and the tool rounds of each session for the background
	// reviews.
	Reviews ReviewCounters
	// Background is the base context of the background reviews. Its end stops the running
	// reviews, and no new review starts. Nil means context.Background().
	Background context.Context

	// reviewMu orders WaitReviews after each goReview that passed its check.
	reviewMu sync.Mutex
	// reviewWG counts the running background reviews.
	reviewWG sync.WaitGroup

	// beforeCall is a test hook. It runs before the steer drain of each model call.
	beforeCall func(round int)
	// reviewHook is a test hook. When it is set, a triggered review calls it in place of
	// RunReview.
	reviewHook func(r ReviewRequest)
}

// Turn is the input of one user turn.
type Turn struct {
	Session  *sessions.Session
	UserText string
	Images   []provider.Image
	// Toolsets holds the allowed toolsets (tools.Resolve).
	Toolsets    map[string]bool
	RemoveTools []string
	// Env is the tool context. Its Values map must stay the same for a session across turns.
	Env  *tools.Env
	Sink Sink
	// System is the system prompt. When it is empty, RunTurn uses the frozen prompt of
	// the session from Agent.Prompts.
	System string
	// MaxTurns limits the tool rounds. 0 uses Cfg.Agent.MaxTurns.
	MaxTurns int
	// Steer holds the user guidance that arrives during the turn. It can be nil.
	Steer *SteerQueue
	// Notify receives the notices of the background reviews that the turn starts, for
	// example "💾 Memory updated". Wrap the send function with HoldNotices, so that a
	// notice waits for the answer. Nil drops the notices.
	Notify func(text string)

	// review makes the turn a review run. Only RunReview sets it.
	review *reviewOpts
}

// reviewOpts changes a turn into a review run. The request starts with a copy
// of the parent view and has the parent tool schemas, so the prompt cache hits.
type reviewOpts struct {
	// history goes before the stored messages in each request. It is not saved.
	history []provider.Message
	// schemas are the tool schemas of the request.
	schemas []provider.ToolSchema
	// allow holds the tools that the run can call. A call to another tool gets deny.
	allow map[string]bool
	deny  string
	// onResult receives each tool call and its result.
	onResult func(c provider.ToolCall, r tools.Result)
}

// TurnResult is the output of one turn.
type TurnResult struct {
	// Final is the last answer text. After an error it holds the partial text.
	Final      string
	ToolRounds int
	// Usage is the sum over the model calls of the turn. FirstTokenMS is from the first call.
	Usage provider.Usage
	// PendingSteer holds the steer text that the turn did not deliver.
	PendingSteer string
	// Media holds the MEDIA tags that the media tools of this turn returned, for example
	// text_to_speech, with the voice directive first when a result asks for a voice note.
	// Final does not include them. A messaging channel adds them with AppendMediaTags.
	Media []string

	// usedMemory and usedSkills tell that the turn called memory or skill_manage. Such a
	// call resets the review count (Hermes).
	usedMemory, usedSkills bool
}

// turnState holds the messages that the current turn saved.
type turnState struct {
	sessionID string
	userID    int64
	images    []provider.Image
	// msgs and ids are the messages of the turn and their stored ids, in order.
	msgs []provider.Message
	ids  []int64
	// patched holds the content of a stored message that changed in this turn, by id. It
	// keeps the request right when the database update fails.
	patched map[int64]string
	// history goes before the stored messages in each request (a review run).
	history []provider.Message
}

// RunTurn runs one user turn. It saves each message when the message is
// created. A cancelled ctx stops the turn. Then the partial text and the finished tool
// results stay saved. The pending steer text is dropped, and RunTurn returns ctx.Err().
// After any other end, TurnResult.PendingSteer holds the steer text that the turn did not
// deliver.
func (a *Agent) RunTurn(ctx context.Context, t Turn) (res TurnResult, err error) {
	if t.Session == nil {
		return res, errors.New("agent: the turn has no session")
	}
	defer func() {
		// A stop supersedes the steer, because the run that it guides has ended (Hermes).
		if pending := t.Steer.Drain(); ctx.Err() == nil {
			res.PendingSteer = pending
		}
	}()
	// lastReq is the last model request of the turn. The review triggers use its shape.
	var lastReq provider.Request
	if t.review == nil {
		defer func() {
			counted := res
			if err != nil {
				counted.Final = ""
			}
			// The review runs in its own goroutine, so this does not block the turn.
			a.MaybeStartReviews(context.WithoutCancel(ctx), t.Session, lastReq, counted, t.Notify)
		}()
	}
	cfg := a.config()
	maxTurns := t.MaxTurns
	if maxTurns <= 0 {
		maxTurns = cfg.Agent.MaxTurns
	}
	if maxTurns <= 0 {
		maxTurns = config.Default().Agent.MaxTurns
	}
	sink := t.Sink
	if sink == nil {
		sink = nopSink{}
	}
	env := a.turnEnv(t)
	// The writes use a context without cancel, so a /stop does not lose a finished message.
	dbCtx := context.WithoutCancel(ctx)
	prov := a.providerFor(t.Session)
	visible, schemas := a.visible(t)
	deny := ""
	if t.review != nil {
		visible, schemas, deny = t.review.allow, t.review.schemas, t.review.deny
	}
	// delegate_task gives the toolsets to the children, and execute_code reads the names.
	tools.SetTurnTools(env, t.Toolsets, visible)
	channel := env.Channel
	if channel == "" {
		channel = t.Session.Source
	}
	system, frozenAt := a.turnSystem(ctx, t, channel)
	if t.review == nil && !noReviews(t.Session.Kind) {
		a.Reviews.remember(t.Session.ID, system, schemas, env, t.Notify)
	}

	// The stored user message keeps only the image markers. The images go inline only to a
	// vision model.
	content, images := a.visionInput(t.Session, t.UserText, t.Images)
	ts := &turnState{sessionID: t.Session.ID, images: images, patched: map[int64]string{}}
	if t.review != nil {
		ts.history = t.review.history
	} else {
		// ts.msgs holds only the messages of this turn, so a file of an earlier turn is
		// never sent again.
		defer func() { res.Media = turnMedia(ts.msgs) }()
	}
	// A message on a later day than the frozen prompt tells the model the date.
	user := provider.Message{Role: provider.RoleUser, Content: NowPrefix(frozenAt, time.Now()) + content}
	if ts.userID, err = a.save(dbCtx, ts, user, sessions.MessageMeta{}); err != nil {
		return res, err
	}

	var (
		lastText string
		failures failureTracker
	)
	for round := 1; ; round++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if a.beforeCall != nil {
			a.beforeCall(round)
		}
		a.steerStored(dbCtx, t.Steer, ts)
		if a.compact(ctx, t, prov, sink) && t.System == "" {
			// The compaction count is part of the prompt key, so the prompt is rebuilt.
			system, _ = a.turnSystem(ctx, t, channel)
			if t.review == nil && !noReviews(t.Session.Kind) {
				a.Reviews.remember(t.Session.ID, system, schemas, env, t.Notify)
			}
		}
		msgs, err := a.requestMessages(dbCtx, ts)
		if err != nil {
			return res, err
		}
		req := provider.Request{System: system, Messages: msgs, Tools: schemas}
		if round == maxTurns-1 {
			req.Messages = append(req.Messages, provider.Message{Role: provider.RoleSystem, Content: NoteMaxTurns})
		}

		lastReq = req
		out, cerr := a.callModel(ctx, prov, req, sink)
		addUsage(&res.Usage, out.Usage, round == 1)
		meta := a.meta(prov, out)
		if cerr != nil {
			// A tool call of a failed stream has no result, so only the text is saved.
			if out.Text != "" {
				res.Final = out.Text
				if _, err := a.save(dbCtx, ts, provider.Message{Role: provider.RoleAssistant, Content: out.Text}, meta); err != nil {
					a.logger().Error("agent: cannot save the partial answer", "session", ts.sessionID, "err", err.Error())
				}
			}
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			return res, fmt.Errorf("agent: the model call to %s failed: %w", meta.Provider, cerr)
		}
		answer := provider.Message{Role: provider.RoleAssistant, Content: out.Text, ToolCalls: out.ToolCalls}
		if _, err := a.save(dbCtx, ts, answer, meta); err != nil {
			return res, err
		}
		if out.Text != "" {
			lastText = out.Text
		}
		if len(out.ToolCalls) == 0 {
			res.Final = out.Text
			return res, nil
		}

		sink.Boundary()
		results := a.runRound(ctx, out.ToolCalls, env, visible, sink, deny)
		if t.review != nil && t.review.onResult != nil {
			for i, c := range out.ToolCalls {
				t.review.onResult(c, results[i])
			}
		}
		res.ToolRounds = round
		for _, c := range out.ToolCalls {
			res.usedMemory = res.usedMemory || c.Name == "memory"
			res.usedSkills = res.usedSkills || c.Name == "skill_manage"
		}
		stop := false
		contents := make([]string, len(results))
		for i, c := range out.ToolCalls {
			if failures.Observe(a.failureKind(c, visible)) {
				stop = true
			}
			contents[i] = a.toolText(results[i].Text)
		}
		// The steer goes into the last result before the save, so no update is necessary.
		if g := t.Steer.Drain(); g != "" && ctx.Err() == nil {
			contents[len(contents)-1] += SteerMarker + g
		}
		for i, c := range out.ToolCalls {
			m := provider.Message{Role: provider.RoleTool, Content: contents[i], ToolCallID: c.ID, ToolName: c.Name}
			if _, err := a.save(dbCtx, ts, m, sessions.MessageMeta{}); err != nil {
				return res, err
			}
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if stop {
			res.Final = lastText
			return res, failures.Err()
		}
		if round >= maxTurns {
			res.Final = joinNote(lastText, NoteStopped)
			return res, nil
		}
	}
}

// View returns the model view of the session history, as the last turn sent it: the
// observed group messages are in the block of the last addressed user message (withObserved).
func (a *Agent) View(ctx context.Context, sessionID string) ([]provider.Message, error) {
	stored, err := a.viewStored(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return plain(withObserved(stored, lastAddressed(stored))), nil
}

// storedView returns the messages of viewStored without the observed block. The
// compactor counts these messages.
func (a *Agent) storedView(ctx context.Context, sessionID string) ([]provider.Message, error) {
	stored, err := a.viewStored(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return plain(stored), nil
}

func plain(stored []sessions.StoredMessage) []provider.Message {
	out := make([]provider.Message, len(stored))
	for i, m := range stored {
		out[i] = m.Msg
	}
	return out
}

// viewStored returns the messages of the model view, with their stored ids. The latest
// compaction replaces the messages that it covers with one summary message, which has id 0.
// Meta.Observed is the only metadata that each message keeps. A tool call with no result
// gets a synthetic result with id 0.
func (a *Agent) viewStored(ctx context.Context, sessionID string) ([]sessions.StoredMessage, error) {
	msgs, err := a.Store.Messages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("agent: read the history: %w", err)
	}
	comps, err := ctxmgr.LoadCompactions(ctx, a.DB, sessionID)
	if err != nil {
		return nil, fmt.Errorf("agent: read the history: %w", err)
	}
	if len(comps) == 0 {
		return repairToolCalls(msgs), nil
	}
	observed := map[int64]bool{}
	for _, m := range msgs {
		if m.Meta.Observed {
			observed[m.ID] = true
		}
	}
	view := ctxmgr.BuildStoredView(msgs, comps)
	out := make([]sessions.StoredMessage, len(view))
	for i, v := range view {
		out[i] = sessions.StoredMessage{ID: v.ID, Msg: v.Msg, Meta: sessions.MessageMeta{Observed: observed[v.ID]}}
	}
	return repairToolCalls(out), nil
}

// compact runs the compactor before a model call. It returns true when the
// session was compacted. A failure does not stop the turn. After the third failure in a
// row, the sink tells the user.
func (a *Agent) compact(ctx context.Context, t Turn, prov provider.Provider, sink Sink) bool {
	// A review run reads a copy of the parent view, so it does not compact.
	if a.Compactor == nil || t.review != nil {
		return false
	}
	did, err := a.Compactor.Maybe(ctx, t.Session, prov, a.contextWindow(prov), false)
	switch {
	case errors.Is(err, ctxmgr.ErrCompactionFailed):
		sink.Notice("⚠️ " + err.Error())
	case err != nil:
		a.logger().Warn("agent: cannot check the compaction", "session", t.Session.ID, "err", err.Error())
	}
	return did
}

// ErrNoCompactor is the error of CompactNow when the agent has no compactor.
var ErrNoCompactor = errors.New("agent: compaction is not configured")

// CompactNow compacts the session now, below the threshold too (the /compact command). It
// returns the number of messages that the summary replaced, or 0 when the session is too
// short to compact. A failed summary call is an error.
func (a *Agent) CompactNow(ctx context.Context, s *sessions.Session) (int, error) {
	if a.Compactor == nil {
		return 0, ErrNoCompactor
	}
	before, err := a.storedView(ctx, s.ID)
	if err != nil {
		return 0, err
	}
	cfg := a.Compactor.Cfg
	if _, _, ok := ctxmgr.SelectRange(before, cfg.ProtectFirstN, cfg.ProtectLastN); !ok {
		return 0, nil
	}
	prov := a.providerFor(s)
	did, err := a.Compactor.Maybe(ctx, s, prov, a.contextWindow(prov), true)
	if err != nil {
		return 0, err
	}
	if !did {
		// Maybe logs the cause of a failed summary call and returns no error for it.
		return 0, errors.New("agent: the compaction did not complete. The log has the cause")
	}
	after, err := a.storedView(ctx, s.ID)
	if err != nil {
		return 0, err
	}
	return len(before) - len(after) + 1, nil
}

// DefaultModel returns the model of the default provider and its context window. When the
// provider has no model name, the model comes from the config entry of the provider.
func (a *Agent) DefaultModel() (model string, contextWindow int) {
	if a.Prov == nil {
		return "", 0
	}
	model = a.Prov.Model()
	if model == "" {
		model = a.config().Providers[a.Prov.Name()].Model
	}
	return model, a.contextWindow(a.Prov)
}

// ModelState is the model that answers now for the default provider.
type ModelState struct {
	Model         string
	ContextWindow int
	// Fallback.Active is false when the primary answers.
	Fallback provider.FallbackStatus
}

// CurrentModel returns the model that answers now. When the default provider calls its
// fallback, the result names the fallback model and its context window. Else it is the
// same as DefaultModel.
func (a *Agent) CurrentModel() ModelState {
	if a.Prov == nil {
		return ModelState{}
	}
	st, _ := provider.StatusOf(a.Prov)
	if !st.Active {
		model, window := a.DefaultModel()
		return ModelState{Model: model, ContextWindow: window, Fallback: st}
	}
	var window int
	if p, ok := a.Providers[st.FallbackName]; ok {
		window = a.contextWindow(p)
	} else {
		window = a.modelWindow(st.FallbackName, st.FallbackModel)
	}
	return ModelState{Model: st.FallbackModel, ContextWindow: window, Fallback: st}
}

// contextWindow returns the context window of the model of prov.
func (a *Agent) contextWindow(prov provider.Provider) int {
	return a.modelWindow(prov.Name(), prov.Model())
}

// modelWindow returns the context window of model in the config entry name. An empty
// model uses the model of the entry.
func (a *Agent) modelWindow(name, model string) int {
	pc := a.config().Providers[name]
	if model != "" {
		pc.Model = model
	}
	if a.Prices != nil {
		return a.Prices.ContextWindow(name, pc)
	}
	if pc.ContextWindow > 0 {
		return pc.ContextWindow
	}
	return pricing.DefaultContextWindow
}

// steerStored delivers the pending steer text into the last tool message of the turn,
// which is already stored. It updates the stored message, so the transcript matches what
// the model sees. With no tool message in the turn, the text stays pending.
func (a *Agent) steerStored(ctx context.Context, q *SteerQueue, ts *turnState) {
	text := q.Drain()
	if text == "" {
		return
	}
	out, ok := InjectSteer(ts.msgs, text)
	if !ok {
		q.restash(text)
		return
	}
	i := lastToolIndex(out)
	ts.msgs = out
	id := ts.ids[i]
	ts.patched[id] = out[i].Content
	if a.DB == nil {
		a.logger().Warn("agent: no database; the steer text is not saved in the transcript", "session", ts.sessionID)
		return
	}
	if _, err := a.DB.ExecContext(ctx, `UPDATE messages SET content = ? WHERE id = ?`, out[i].Content, id); err != nil {
		a.logger().Error("agent: cannot save the steer text in the transcript", "session", ts.sessionID, "message", id, "err", err.Error())
	}
}

// requestMessages returns the view for the next request. The user message of the turn
// gets its images back, because the store keeps only a marker for each image. The
// observed group messages go into a context-only block at the start of the user message
// of the turn (withObserved).
func (a *Agent) requestMessages(ctx context.Context, ts *turnState) ([]provider.Message, error) {
	stored, err := a.viewStored(ctx, ts.sessionID)
	if err != nil {
		return nil, err
	}
	stored = withObserved(stored, ts.userID)
	out := make([]provider.Message, 0, len(ts.history)+len(stored))
	out = append(out, ts.history...)
	for _, sm := range stored {
		m := sm.Msg
		if sm.ID == ts.userID && len(ts.images) > 0 {
			m.Images = ts.images
		}
		if c, ok := ts.patched[sm.ID]; ok {
			m.Content = c
		}
		out = append(out, m)
	}
	return out, nil
}

// save appends m to the store and to the turn state.
func (a *Agent) save(ctx context.Context, ts *turnState, m provider.Message, meta sessions.MessageMeta) (int64, error) {
	id, err := a.Store.Append(ctx, ts.sessionID, m, meta)
	if err != nil {
		return 0, fmt.Errorf("agent: save a %s message: %w", m.Role, err)
	}
	ts.msgs = append(ts.msgs, m)
	ts.ids = append(ts.ids, id)
	return id, nil
}

// callModel streams one request. It returns when the stream ends, when ctx ends, or when
// the stream sends no event for streamIdleTimeout. The result keeps the partial text.
func (a *Agent) callModel(ctx context.Context, prov provider.Provider, req provider.Request, sink Sink) (provider.Result, error) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := prov.Stream(cctx, req)
	if err != nil {
		return provider.Result{}, err
	}
	// The relay stops at the end of ctx even when the provider does not close ch.
	relay := make(chan provider.Event)
	var stalled atomic.Bool
	go func() {
		defer close(relay)
		idle := time.NewTimer(streamIdleTimeout)
		defer idle.Stop()
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				idle.Reset(streamIdleTimeout)
				select {
				case relay <- ev:
				case <-cctx.Done():
					return
				}
			case <-idle.C:
				stalled.Store(true)
				cancel()
				return
			case <-cctx.Done():
				return
			}
		}
	}()
	res, err := provider.Collect(relay, sink.Delta)
	switch {
	case ctx.Err() != nil:
		err = ctx.Err()
	case stalled.Load():
		// The stamp names the fallback when the fallback served the stream.
		name := prov.Name()
		if res.Provider != "" {
			name = res.Provider
		}
		err = fmt.Errorf("provider %s: the stream sent no data for %s", name, streamIdleTimeout)
	}
	return res, err
}

// meta returns the stored data of one model response. The row names the provider that
// served the call, which can be the fallback of prov. The adapter stamps that provider on
// the stream. With no stamp, the row names prov. The cost is stamped now, so a later price
// change does not change the stored row.
func (a *Agent) meta(prov provider.Provider, out provider.Result) sessions.MessageMeta {
	m := sessions.MessageMeta{
		Usage:        out.Usage,
		Model:        prov.Model(),
		Provider:     prov.Name(),
		FinishReason: out.FinishReason,
	}
	if out.Provider != "" {
		m.Provider, m.Model = out.Provider, out.Model
	}
	if a.Prices == nil {
		return m
	}
	pc, ok := a.config().Providers[m.Provider]
	if !ok {
		return m
	}
	// A model override replaces the model of the entry, so the price is for that model.
	if m.Model != "" {
		pc.Model = m.Model
	}
	if usd, known, flat := a.Prices.Cost(m.Provider, pc, out.Usage); known && !flat {
		m.CostUSD = usd
	}
	return m
}

// toolText returns the stored text of one tool result: redacted, then truncated.
func (a *Agent) toolText(s string) string {
	s = a.Redactor.Redact(s)
	max := a.config().Context.ToolOutputMaxChars
	out, err := ctxmgr.Truncate(s, max, a.SpillDir)
	if err == nil {
		return out
	}
	a.logger().Warn("agent: cannot write the spill file; the tool output is cut without a copy",
		"dir", a.SpillDir, "err", err.Error())
	return cutNoSpill(ctxmgr.SanitizeUTF8(s), max)
}

// cutNoSpill keeps the head (60%) and the tail (30%) of s when no spill file can be written.
func cutNoSpill(s string, max int) string {
	r := []rune(s)
	head, tail := max*6/10, max*3/10
	return string(r[:head]) + fmt.Sprintf("\n[… %d chars omitted. The full output could not be saved.]\n", len(r)-head-tail) +
		string(r[len(r)-tail:])
}

// providerFor returns the provider of the session. ModelOverride is "name" or
// "name:model", where name is a key of Providers. An unknown name uses the default.
func (a *Agent) providerFor(s *sessions.Session) provider.Provider {
	if s == nil || s.ModelOverride == "" {
		return a.Prov
	}
	name, model, _ := strings.Cut(s.ModelOverride, ":")
	p, ok := a.Providers[name]
	if !ok || p == nil {
		a.logger().Warn("agent: the model override names an unknown provider; the default provider is used",
			"session", s.ID, "override", s.ModelOverride)
		return a.Prov
	}
	if model == "" {
		return p
	}
	return pinned{Provider: p, model: model}
}

// providerEntry returns the name of the provider entry of the session: the name of the
// model override when it names a known provider, else "primary".
func (a *Agent) providerEntry(s *sessions.Session) string {
	if s != nil && s.ModelOverride != "" {
		name, _, _ := strings.Cut(s.ModelOverride, ":")
		if p, ok := a.Providers[name]; ok && p != nil {
			return name
		}
	}
	return "primary"
}

// visionInput applies the vision rule to the user message of a turn. It returns
// the text to save and the images to send inline. Each image of the turn gets an
// "[image: <path>]" marker in the text, so the transcript keeps only the paths.
//   - The provider entry of the session has vision: true. The images of the turn and the
//     files of the markers in text go inline.
//   - Else no image goes inline, and the markers stay as text for view_image.
//   - When no vision provider exists, each marker becomes NoVisionMarker.
func (a *Agent) visionInput(s *sessions.Session, text string, images []provider.Image) (string, []provider.Image) {
	cfg := a.config()
	content := text
	for _, img := range images {
		marker := "[image]"
		if img.Path != "" {
			marker = "[image: " + img.Path + "]"
		}
		if img.Path == "" || !strings.Contains(content, marker) {
			if content != "" {
				content += "\n"
			}
			content += marker
		}
	}
	if cfg.Providers[a.providerEntry(s)].Vision {
		out := append([]provider.Image(nil), images...)
		have := map[string]bool{}
		for _, img := range images {
			have[img.Path] = true
		}
		for _, p := range tools.ImagesFromMarkers(text) {
			if have[p] {
				continue
			}
			have[p] = true
			img, err := tools.LoadImageFile(p, 0)
			if err != nil {
				a.logger().Warn("agent: the image of a marker does not go inline", "path", p, "err", err.Error())
				continue
			}
			out = append(out, img)
		}
		return content, out
	}
	if _, ok := tools.VisionProvider(cfg, a.Providers); !ok {
		content = tools.ReplaceImageMarkers(content, tools.NoVisionMarker)
	}
	return content, nil
}

// pinned is a provider entry with a model that replaces the model of the entry.
type pinned struct {
	provider.Provider
	model string
}

func (p pinned) Model() string { return p.model }

func (p pinned) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	return p.Provider.Stream(ctx, req)
}

// visible returns the visible tool names and schemas of the turn.
func (a *Agent) visible(t Turn) (map[string]bool, []provider.ToolSchema) {
	if a.Tools == nil {
		return map[string]bool{}, nil
	}
	return a.Tools.Visible(t.Toolsets, t.RemoveTools)
}

// turnEnv returns the tool context of the turn.
func (a *Agent) turnEnv(t Turn) *tools.Env {
	env := t.Env
	if env == nil {
		env = &tools.Env{SessionID: t.Session.ID, SessionKey: t.Session.Key, Kind: t.Session.Kind, Channel: t.Session.Source}
	}
	if env.Values == nil {
		env.Values = map[string]any{}
	}
	if env.Log == nil {
		env.Log = a.logger()
	}
	return env
}

func (a *Agent) config() *config.Config {
	if a.Cfg == nil {
		c := config.Default()
		a.Cfg = &c
	}
	return a.Cfg
}

func (a *Agent) logger() *slog.Logger {
	if a.Log == nil {
		return slog.Default()
	}
	return a.Log
}

// addUsage adds u to sum. The first call of the turn gives FirstTokenMS.
func addUsage(sum *provider.Usage, u provider.Usage, first bool) {
	sum.InputTokens += u.InputTokens
	sum.OutputTokens += u.OutputTokens
	sum.CacheReadTokens += u.CacheReadTokens
	sum.TotalMS += u.TotalMS
	if first {
		sum.FirstTokenMS = u.FirstTokenMS
	}
}

// joinNote puts note after text, with a blank line between them.
func joinNote(text, note string) string {
	if text == "" {
		return note
	}
	return text + "\n\n" + note
}

// argPreview returns the first string argument of the JSON object args, on one line and
// cut to previewMax characters. It returns "" when args has no string argument.
func argPreview(args string) string {
	dec := json.NewDecoder(strings.NewReader(args))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return ""
	}
	for dec.More() {
		if _, err := dec.Token(); err != nil { // the key
			return ""
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return ""
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			continue
		}
		s = strings.Join(strings.Fields(s), " ")
		if utf8.RuneCountInString(s) > previewMax {
			s = string([]rune(s)[:previewMax-3]) + "..."
		}
		return s
	}
	return ""
}

// nopSink ignores all output.
type nopSink struct{}

func (nopSink) Delta(string)          {}
func (nopSink) ToolStart(_, _ string) {}
func (nopSink) ToolEnd(string, bool)  {}
func (nopSink) Boundary()             {}
func (nopSink) Notice(string)         {}
