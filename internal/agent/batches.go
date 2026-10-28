package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// The values of delegation_batches.status.
const (
	BatchRunning   = "running"
	BatchDone      = "done"
	BatchCancelled = "cancelled"
	BatchLost      = "lost"
)

// Batches runs the background batches of delegate_task. A batch row is in
// delegation_batches. When every child of a batch is done, Enqueue puts a synthetic user
// turn with the results on the parent session key.
type Batches struct {
	DB *db.DB
	// Enqueue puts a synthetic user turn on a session key. The gateway sets it
	// (Gateway.Enqueue). When the key is busy, the turn waits in the queue.
	Enqueue func(ctx context.Context, sessionKey, text string) error
	// Owns reports whether this process handles the batches of a session key in
	// RecoverLost. Nil handles every key. The gateway and a chat process share state.db, so
	// each process must recover only the keys of its own channels.
	Owns func(sessionKey string) bool
	Log  *slog.Logger
	// Ctx is the base context of the batches. Its end stops every running child at a
	// process stop, and Start then refuses new batches. A batch that stops in this way
	// writes no end state and enqueues no turn. Its row stays "running", so RecoverLost
	// reports it at the next start. Nil uses context.Background().
	Ctx context.Context

	mu sync.Mutex
	// wg counts the batch goroutines, for Wait.
	wg sync.WaitGroup
	// running holds the cancel function of each running batch, by session key and batch id.
	running map[string]map[string]context.CancelFunc
}

// batchTask is one task in the tasks column.
type batchTask struct {
	Name     string   `json:"name"`
	Goal     string   `json:"goal"`
	Context  string   `json:"context,omitempty"`
	Role     string   `json:"role,omitempty"`
	Toolsets []string `json:"toolsets,omitempty"`
}

// Start writes the batch row and starts the children with run. It returns at once. The
// children keep the values of ctx, but not its end, so they survive the end of the parent
// turn. CancelKey(parentKey) and the end of Batches.Ctx stop them.
func (b *Batches) Start(ctx context.Context, parentKey, parentSessionID string, tasks []ChildSpec,
	run func(context.Context, ChildSpec) ChildResult) (batchID string, err error) {
	if len(tasks) == 0 {
		return "", errors.New("agent: the background batch has no tasks")
	}
	if b.DB == nil {
		return "", errors.New("agent: background delegation needs state.db")
	}
	base := b.base()
	if base.Err() != nil {
		return "", errors.New("agent: the process stops, so it starts no background batch")
	}
	id, err := newBatchID()
	if err != nil {
		return "", err
	}
	rows := make([]batchTask, len(tasks))
	for i, s := range tasks {
		rows[i] = batchTask{Name: s.Name, Goal: s.Goal, Context: s.Context, Role: s.Role, Toolsets: s.Toolsets}
	}
	tj, _ := json.Marshal(rows)
	dbCtx := context.WithoutCancel(ctx)
	if _, err := b.DB.ExecContext(dbCtx, `INSERT INTO delegation_batches
		(id, parent_session_key, parent_session_id, background, status, tasks, created_at) VALUES (?, ?, ?, 1, ?, ?, ?)`,
		id, parentKey, parentSessionID, BatchRunning, string(tj), time.Now().UnixMilli()); err != nil {
		return "", fmt.Errorf("agent: write the batch row: %w", err)
	}

	bctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopAfter := context.AfterFunc(base, cancel)
	b.mu.Lock()
	// Check the end again under the lock, so that wg.Add never runs after Wait starts.
	if base.Err() != nil {
		b.mu.Unlock()
		stopAfter()
		cancel()
		return "", errors.New("agent: the process stops, so it starts no background batch")
	}
	b.wg.Add(1)
	if b.running == nil {
		b.running = map[string]map[string]context.CancelFunc{}
	}
	if b.running[parentKey] == nil {
		b.running[parentKey] = map[string]context.CancelFunc{}
	}
	b.running[parentKey][id] = cancel
	b.mu.Unlock()

	go func() {
		defer b.wg.Done()
		defer stopAfter()
		defer cancel()
		results := make([]ChildResult, len(tasks))
		var wg sync.WaitGroup
		for i := range tasks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = run(bctx, tasks[i])
			}()
		}
		wg.Wait()
		status := BatchDone
		if bctx.Err() != nil {
			status = BatchCancelled
		}
		b.mu.Lock()
		delete(b.running[parentKey], id)
		if len(b.running[parentKey]) == 0 {
			delete(b.running, parentKey)
		}
		b.mu.Unlock()
		if base.Err() != nil {
			b.logger().Info("agent: the process stops, so the background delegation stopped; the next start reports it as lost",
				"batch", id, "key", parentKey)
			return
		}
		b.finish(id, status, results)
		if b.Enqueue == nil {
			b.logger().Warn("agent: background delegation finished, but no channel can receive the results", "batch", id, "key", parentKey)
			return
		}
		if err := b.Enqueue(base, parentKey, FormatBatchDone(id, results)); err != nil {
			b.logger().Error("agent: cannot deliver the results of a background delegation", "batch", id, "key", parentKey, "err", err.Error())
		}
	}()
	return id, nil
}

// finish writes the end state of a batch row.
func (b *Batches) finish(id, status string, results []ChildResult) {
	rj, _ := json.Marshal(liteResults(results))
	if _, err := b.DB.ExecContext(context.Background(), `UPDATE delegation_batches SET status = ?, results = ?, finished_at = ? WHERE id = ?`,
		status, string(rj), time.Now().UnixMilli(), id); err != nil {
		b.logger().Error("agent: cannot update the batch row", "batch", id, "err", err.Error())
	}
}

// Wait waits until every batch goroutine returns, or until timeout. It returns false at
// the timeout. Cancel Batches.Ctx first, else the running children go on.
func (b *Batches) Wait(timeout time.Duration) bool {
	// A Start that passed its check under the lock has called wg.Add before this point,
	// so wg.Wait starts under the lock and never runs before that wg.Add.
	done := make(chan struct{})
	b.mu.Lock()
	go func() {
		b.wg.Wait()
		close(done)
	}()
	b.mu.Unlock()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// base returns Batches.Ctx, or context.Background() when it is nil.
func (b *Batches) base() context.Context {
	if b.Ctx == nil {
		return context.Background()
	}
	return b.Ctx
}

// CancelKey cancels the running background children of sessionKey. Each batch still
// enqueues its turn, with status "cancelled" for the stopped children. /stop, /new, and
// /reset call it.
func (b *Batches) CancelKey(sessionKey string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, cancel := range b.running[sessionKey] {
		cancel()
	}
}

// RecoverLost runs at startup. A restart loses the running background children, so for each
// background batch that is still "running", it enqueues "[Background delegation <id> was lost
// in a restart]" on the key of the batch and marks the row "lost". It skips the batches that
// this process runs and the keys that Owns refuses.
func (b *Batches) RecoverLost(ctx context.Context) error {
	if b.DB == nil {
		return nil
	}
	type lost struct{ id, key string }
	var found []lost
	rows, err := b.DB.QueryContext(ctx, `SELECT id, parent_session_key FROM delegation_batches
		WHERE status = ? AND background = 1 ORDER BY created_at`, BatchRunning)
	if err != nil {
		return fmt.Errorf("agent: read the running batches: %w", err)
	}
	for rows.Next() {
		var l lost
		if err := rows.Scan(&l.id, &l.key); err != nil {
			rows.Close()
			return fmt.Errorf("agent: read the running batches: %w", err)
		}
		found = append(found, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("agent: read the running batches: %w", err)
	}
	var errs []error
	for _, l := range found {
		if b.isRunning(l.key, l.id) || (b.Owns != nil && !b.Owns(l.key)) {
			continue
		}
		// The row is marked first, so a failed delivery is not repeated at each start.
		if _, err := b.DB.ExecContext(ctx, `UPDATE delegation_batches SET status = ?, finished_at = ? WHERE id = ? AND status = ?`,
			BatchLost, time.Now().UnixMilli(), l.id, BatchRunning); err != nil {
			errs = append(errs, fmt.Errorf("agent: mark batch %s lost: %w", l.id, err))
			continue
		}
		if b.Enqueue == nil {
			continue
		}
		if err := b.Enqueue(ctx, l.key, FormatBatchLost(l.id)); err != nil {
			errs = append(errs, fmt.Errorf("agent: report the lost batch %s on %s: %w", l.id, l.key, err))
		}
	}
	return errors.Join(errs...)
}

func (b *Batches) isRunning(key, id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.running[key][id]
	return ok
}

func (b *Batches) logger() *slog.Logger {
	if b.Log == nil {
		return slog.Default()
	}
	return b.Log
}

// FormatBatchDone returns the synthetic user turn of a finished batch:
// "[Background delegation <id> finished]\n<results JSON>".
func FormatBatchDone(id string, results []ChildResult) string {
	return "[Background delegation " + id + " finished]\n" + tools.FormatDelegateResults(liteResults(results))
}

// FormatBatchLost returns the synthetic user turn of a batch that a restart lost.
func FormatBatchLost(id string) string {
	return "[Background delegation " + id + " was lost in a restart]"
}

func newBatchID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("agent: make a batch id: %w", err)
	}
	return "bg_" + hex.EncodeToString(b[:]), nil
}

func liteResult(r ChildResult) tools.ChildResultLite {
	return tools.ChildResultLite{Status: r.Status, Summary: r.Summary, ToolCalls: r.ToolCalls, DurationS: r.DurationS, Error: r.Error}
}

func liteResults(rs []ChildResult) []tools.ChildResultLite {
	out := make([]tools.ChildResultLite, len(rs))
	for i, r := range rs {
		out[i] = liteResult(r)
	}
	return out
}

// Delegator connects delegate_task to the agent. It implements tools.ChildRunner and
// tools.BackgroundStarter. The children of top-level runs, foreground and background, share
// one limit of delegation.max_concurrent_children per process. The children of a child
// (an orchestrator) do not take from that limit, because the parent child holds a slot
// while it waits for them.
type Delegator struct {
	Agent *Agent
	// Batches runs the background batches. Nil turns background mode off.
	Batches *Batches

	semOnce sync.Once
	sem     chan struct{}
	// runChild is a test hook. Nil uses Agent.RunChild.
	runChild func(context.Context, ChildSpec) ChildResult
}

// RunChild runs one child and waits for it.
func (d *Delegator) RunChild(ctx context.Context, spec tools.ChildSpecLite) tools.ChildResultLite {
	cs, err := d.childSpec(ctx, spec.ParentEnv, spec)
	if err != nil {
		return tools.ChildResultLite{Status: ChildError, Error: err.Error()}
	}
	return liteResult(d.run(ctx, cs))
}

// StartBackground starts the children as one background batch on the session key of env.
// The children report no progress lines, because the status message of the turn is closed
// before they end.
func (d *Delegator) StartBackground(ctx context.Context, env *tools.Env, specs []tools.ChildSpecLite) (string, error) {
	if d.Batches == nil {
		return "", errors.New("background delegation is not configured")
	}
	if env == nil || env.SessionKey == "" {
		return "", errors.New("background delegation needs a session key")
	}
	quiet := *env
	quiet.Progress = nil
	tasks := make([]ChildSpec, len(specs))
	for i, s := range specs {
		s.OnToolStart = nil
		cs, err := d.childSpec(ctx, &quiet, s)
		if err != nil {
			return "", err
		}
		tasks[i] = cs
	}
	return d.Batches.Start(ctx, env.SessionKey, env.SessionID, tasks, d.run)
}

// childSpec builds a ChildSpec from a tools.ChildSpecLite. The parent session and the parent
// toolsets come from parentEnv.
func (d *Delegator) childSpec(ctx context.Context, parentEnv *tools.Env, spec tools.ChildSpecLite) (ChildSpec, error) {
	if d.Agent == nil || d.Agent.Store == nil {
		return ChildSpec{}, errors.New("delegation is not configured")
	}
	if parentEnv == nil || parentEnv.SessionID == "" {
		return ChildSpec{}, errors.New("the child has no parent session")
	}
	parent, err := d.Agent.Store.Get(context.WithoutCancel(ctx), parentEnv.SessionID)
	if err != nil {
		return ChildSpec{}, fmt.Errorf("cannot read the parent session %s: %w", parentEnv.SessionID, err)
	}
	sets, _ := tools.TurnTools(parentEnv)
	cs := ChildSpec{
		Goal:           spec.Goal,
		Context:        spec.Context,
		Role:           spec.Role,
		Toolsets:       spec.Toolsets,
		ParentSession:  parent,
		ParentToolsets: sets,
		Depth:          spec.Depth,
		Name:           spec.Name,
		ParentEnv:      parentEnv,
	}
	if spec.OnToolStart != nil {
		cs.Sink = progressSink{onStart: spec.OnToolStart}
	}
	return cs, nil
}

// run runs one child under the process limit.
func (d *Delegator) run(ctx context.Context, spec ChildSpec) ChildResult {
	if spec.Depth == 0 {
		sem := d.limit()
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ChildResult{Status: ChildCancelled, Error: "the child was cancelled before it started"}
		}
		defer func() { <-sem }()
	}
	if d.runChild != nil {
		return d.runChild(ctx, spec)
	}
	return d.Agent.RunChild(ctx, spec)
}

func (d *Delegator) limit() chan struct{} {
	d.semOnce.Do(func() {
		n := d.Agent.config().Delegation.MaxConcurrentChildren
		if n <= 0 {
			n = config.Default().Delegation.MaxConcurrentChildren
		}
		d.sem = make(chan struct{}, n)
	})
	return d.sem
}

// progressSink sends the tool starts of a child to onStart and drops the rest.
type progressSink struct {
	nopSink
	onStart func(name, preview string)
}

func (p progressSink) ToolStart(name, preview string) { p.onStart(name, preview) }
