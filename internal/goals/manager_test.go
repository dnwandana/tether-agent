package goals

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// mgrFix returns a manager over a new state.db with the deps d, the database, and a chat
// session. It sets d.Store and d.Sessions.
func mgrFix(t *testing.T, d Deps) (*Manager, *db.DB, *sessions.Session) {
	t.Helper()
	dbh, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbh.Close() })
	d.Store = &Store{DB: dbh}
	d.Sessions = sessions.New(dbh)
	sess, err := d.Sessions.Create(context.Background(), sessions.Session{Key: "telegram:dm:1", Source: "telegram"})
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(d), dbh, sess
}

// mgrState returns an active goal with epoch 1.
func mgrState(goal string) State {
	return State{Goal: goal, Status: StatusActive, MaxTurns: 20, Epoch: 1}
}

// mgrSet writes st as the goal of sessionID.
func mgrSet(t *testing.T, m *Manager, sessionID string, f func(st *State)) State {
	t.Helper()
	st, err := m.Update(context.Background(), sessionID, func(s *State, _ bool) error { f(s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// mgrProv is a provider with a name. It makes no call.
type mgrProv struct{ name string }

func (p mgrProv) Name() string  { return p.name }
func (p mgrProv) Model() string { return p.name + "-model" }
func (p mgrProv) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	return nil, errors.New("mgrProv makes no call")
}

func TestManagerUpdateCreatesAndChanges(t *testing.T) {
	ctx := context.Background()
	m, _, sess := mgrFix(t, Deps{})
	if _, ok, err := m.Load(ctx, sess.ID); ok || err != nil {
		t.Fatal("no row gives false", ok, err)
	}
	var exists []bool
	st, err := m.Update(ctx, sess.ID, func(s *State, ok bool) error {
		exists = append(exists, ok)
		*s = mgrState("ship it")
		return nil
	})
	if err != nil || st.Goal != "ship it" {
		t.Fatal(st, err)
	}
	if _, err := m.Update(ctx, sess.ID, func(s *State, ok bool) error {
		exists = append(exists, ok)
		s.TurnsUsed = 3
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(exists) != 2 || exists[0] || !exists[1] {
		t.Fatal(exists)
	}
	got, ok, err := m.Load(ctx, sess.ID)
	if err != nil || !ok || got.TurnsUsed != 3 || got.Goal != "ship it" {
		t.Fatal(got, ok, err)
	}
}

func TestManagerUpdateErrorWritesNothing(t *testing.T) {
	ctx := context.Background()
	m, _, sess := mgrFix(t, Deps{})
	boom := errors.New("no goal")
	if _, err := m.Update(ctx, sess.ID, func(s *State, _ bool) error { s.Goal = "x"; return boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, ok, _ := m.Load(ctx, sess.ID); ok {
		t.Fatal("an error from f writes nothing")
	}
}

func TestManagerUpdateRetriesOnceOnConflict(t *testing.T) {
	ctx := context.Background()
	m, dbh, sess := mgrFix(t, Deps{})
	store := &Store{DB: dbh}
	mgrSet(t, m, sess.ID, func(s *State) { *s = mgrState("ship it") })
	calls := 0
	_, err := m.Update(ctx, sess.ID, func(s *State, _ bool) error {
		calls++
		if calls == 1 { // another writer changes the row first
			cur, rev, err := store.Get(ctx, sess.ID)
			if err != nil {
				return err
			}
			cur.Subgoals = []string{"from the other writer"}
			if _, err := store.Put(ctx, sess.ID, cur, rev); err != nil {
				return err
			}
		}
		s.TurnsUsed = 7
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
	got, _, _ := m.Load(ctx, sess.ID)
	if got.TurnsUsed != 7 || len(got.Subgoals) != 1 || got.Subgoals[0] != "from the other writer" {
		t.Fatalf("the retry runs f on the new row: %+v", got)
	}
	_, err = m.Update(ctx, sess.ID, func(s *State, _ bool) error {
		cur, rev, _ := store.Get(ctx, sess.ID)
		_, _ = store.Put(ctx, sess.ID, cur, rev)
		s.TurnsUsed = 9
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("a second conflict returns ErrConflict:", err)
	}
	if got, _, _ := m.Load(ctx, sess.ID); got.TurnsUsed == 9 {
		t.Fatal("a failed update writes nothing")
	}
}

func TestManagerIsCurrentReadsOnlyTheCache(t *testing.T) {
	m, dbh, sess := mgrFix(t, Deps{})
	mgrSet(t, m, sess.ID, func(s *State) { *s = mgrState("ship it") })
	if !m.IsCurrent(sess.ID, 1) || m.IsCurrent(sess.ID, 2) {
		t.Fatal("epoch 1 is current")
	}
	st := mgrSet(t, m, sess.ID, func(s *State) { s.SetStatus(StatusPaused, "paused by the owner") })
	if m.IsCurrent(sess.ID, 1) || m.IsCurrent(sess.ID, st.Epoch) {
		t.Fatal("a paused goal is not current")
	}
	st = mgrSet(t, m, sess.ID, func(s *State) { s.Resume() })
	if !m.IsCurrent(sess.ID, st.Epoch) || m.IsCurrent(sess.ID, 1) {
		t.Fatal("the resumed epoch is current")
	}
	mgrSet(t, m, sess.ID, func(s *State) { s.Subgoals = append(s.Subgoals, "add a test") })
	if !m.IsCurrent(sess.ID, st.Epoch) {
		t.Fatal("a subgoal edit keeps the epoch")
	}
	if err := dbh.Close(); err != nil {
		t.Fatal(err)
	}
	if !m.IsCurrent(sess.ID, st.Epoch) || m.IsCurrent("no-such-session", 1) {
		t.Fatal("IsCurrent makes no database call")
	}
}

func TestManagerDeleteAndActive(t *testing.T) {
	ctx := context.Background()
	m, dbh, sess := mgrFix(t, Deps{})
	other, err := sessions.New(dbh).Create(ctx, sessions.Session{Key: "telegram:dm:2", Source: "telegram"})
	if err != nil {
		t.Fatal(err)
	}
	mgrSet(t, m, sess.ID, func(s *State) { *s = mgrState("ship it") })
	mgrSet(t, m, other.ID, func(s *State) { *s = mgrState("other"); s.SetStatus(StatusPaused, "") })

	fresh := NewManager(Deps{Store: &Store{DB: dbh}}) // a new process
	if fresh.IsCurrent(sess.ID, 1) {
		t.Fatal("a new manager has an empty cache")
	}
	act, err := fresh.Active(ctx)
	if err != nil || len(act) != 1 || act[0].SessionID != sess.ID {
		t.Fatal(act, err)
	}
	if !fresh.IsCurrent(sess.ID, 1) {
		t.Fatal("Active fills the cache")
	}
	if err := m.Delete(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.Load(ctx, sess.ID); ok || m.IsCurrent(sess.ID, 1) {
		t.Fatal("Delete removes the row and the cache entry")
	}
}

func TestManagerJudgeProvider(t *testing.T) {
	cfg := config.GoalsConfig{MaxTurns: 20, JudgeTimeoutS: 30}
	var chained []string
	m, _, sess := mgrFix(t, Deps{
		Cfg:         func() config.GoalsConfig { return cfg },
		ProviderFor: func(s *sessions.Session) provider.Provider { return mgrProv{"session-" + s.ID} },
		ChainFor: func(entry string) provider.Provider {
			chained = append(chained, entry)
			return mgrProv{"chain-" + entry}
		},
	})
	if p := m.JudgeProvider(sess); p.Name() != "session-"+sess.ID || len(chained) != 0 {
		t.Fatal("an empty judge_provider uses the chain of the session:", p.Name(), chained)
	}
	cfg.JudgeProvider = "cheap"
	if p := m.JudgeProvider(sess); p.Name() != "chain-cheap" || len(chained) != 1 || chained[0] != "cheap" {
		t.Fatal(p.Name(), chained)
	}
}

func TestManagerJudgeRecordsInTheJudgeSession(t *testing.T) {
	ctx := context.Background()
	m, dbh, sess := mgrFix(t, Deps{
		Cfg: func() config.GoalsConfig { return config.GoalsConfig{MaxTurns: 20, JudgeTimeoutS: 7} },
		Meta: func(p provider.Provider, out provider.Result) sessions.MessageMeta {
			return sessions.MessageMeta{Provider: p.Name(), Model: p.Model(), Usage: out.Usage}
		},
	})
	st := mgrState("ship it")
	j := m.Judge(sess, &st)
	if j.Timeout != 7*time.Second || j.Record == nil {
		t.Fatalf("%+v", j)
	}
	j.Record(ctx, "Goal:\nship it", mgrProv{"judge"}, provider.Result{Text: `{"verdict":"continue","reason":"x"}`})
	if st.JudgeSessionID == "" {
		t.Fatal("the first record makes the judge session")
	}
	store := sessions.New(dbh)
	js, err := store.Get(ctx, st.JudgeSessionID)
	if err != nil || js.Kind != sessions.KindGoalJudge || js.ParentID != sess.ID || js.Key != "" {
		t.Fatalf("%+v %v", js, err)
	}
	if msgs, err := store.Messages(ctx, js.ID); err != nil || len(msgs) != 2 {
		t.Fatal("one call saves the prompt and the reply:", len(msgs), err)
	}
	if got := NewManager(Deps{}).Judge(sess, &st).Timeout; got != 30*time.Second {
		t.Fatal("a nil Cfg uses the default of 30 s:", got)
	}
}

func TestManagerWaitEnv(t *testing.T) {
	procs := tools.NewProcRegistry()
	procs.Add("s1", 555, "watch.sh", "", time.Unix(100, 0))
	now := time.Unix(1_800_000_000, 0)
	m, _, _ := mgrFix(t, Deps{
		Procs: procs,
		Alive: func(pid int) bool { return pid == 9 },
		RunningBatches: func(key string) int {
			if key == "telegram:dm:1" {
				return 2
			}
			return 0
		},
		Now: func() time.Time { return now },
	})
	env := m.WaitEnv("telegram:dm:1")
	if !env.Alive(9) || env.Alive(10) || env.Batches() != 2 || !env.Now.Equal(now) {
		t.Fatalf("%+v", env)
	}
	if p, ok := env.Proc(555); !ok || p.Command != "watch.sh" {
		t.Fatal(p, ok)
	}
	env = NewManager(Deps{}).WaitEnv("k")
	if !env.Alive(os.Getpid()) || env.Batches() != 0 || env.Now.IsZero() {
		t.Fatal("nil deps use PIDAlive, 0 batches, and the real clock")
	}
	if _, ok := env.Proc(555); ok {
		t.Fatal("a nil registry finds no process")
	}
}

func TestManagerWaitEnvBarrierLimit(t *testing.T) {
	m := NewManager(Deps{Cfg: func() config.GoalsConfig { return config.GoalsConfig{MaxBarrierWaitS: 600} }})
	if got := m.WaitEnv("k").BarrierLimit(); got != 10*time.Minute {
		t.Fatal("the config limit:", got)
	}
	m = NewManager(Deps{Cfg: func() config.GoalsConfig { return config.GoalsConfig{} }})
	if got := m.WaitEnv("k").BarrierLimit(); got != MaxBarrierWait {
		t.Fatal("0 keeps the const:", got)
	}
}
