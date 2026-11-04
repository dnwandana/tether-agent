package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// scopeFixture holds two Telegram chats and one CLI session in one state.db.
type scopeFixture struct {
	store             *sessions.Store
	jobs              *cron.Store
	alice, bob, local *sessions.Session
	aliceEnv, bobEnv  *Env
	localEnv          *Env
}

func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	f := scopeFixture{store: sessions.New(d), jobs: &cron.Store{DB: d, Loc: time.UTC,
		ValidToolset: func(s string) bool { return ValidToolset(s, nil) }}}
	ctx := context.Background()
	mk := func(key, src, chat, text string) *sessions.Session {
		s, err := f.store.Create(ctx, sessions.Session{Key: key, Source: src, ChatID: chat, Title: text})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.Append(ctx, s.ID, provider.Message{Role: provider.RoleUser, Content: "zebra " + text}, sessions.MessageMeta{}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	f.alice = mk("telegram:dm:100", "telegram", "100", "alice")
	f.bob = mk("telegram:group:-5:200", "telegram", "-5", "bob")
	f.local = mk("cli:user", "cli", "", "local")
	f.aliceEnv = &Env{SessionID: f.alice.ID, SessionKey: f.alice.Key, Channel: "telegram"}
	f.bobEnv = &Env{SessionID: f.bob.ID, SessionKey: f.bob.Key, Channel: "telegram"}
	f.localEnv = &Env{SessionID: f.local.ID, SessionKey: f.local.Key, Channel: "cli"}
	return f
}

func TestSessionSearchScope(t *testing.T) {
	f := newScopeFixture(t)
	tool := SessionSearchTool{Store: f.store, Cron: f.jobs}
	out := runToolEnv(t, tool, `{"query":"zebra","limit":20}`, f.aliceEnv)
	if !strings.Contains(out, "1 hits") || !strings.Contains(out, f.alice.ID) {
		t.Fatal("alice sees only her chat:", out)
	}
	if out := runToolEnv(t, tool, `{"query":"zebra","limit":20}`, f.localEnv); !strings.Contains(out, "3 hits") {
		t.Fatal("the local owner sees all:", out)
	}
	for _, args := range []string{`{"session_id":"` + f.bob.ID + `"}`, `{"session_id":"` + f.local.ID + `","around_message_id":1}`} {
		if out := runToolEnvErr(t, tool, args, f.aliceEnv); !strings.Contains(out, "no session") {
			t.Fatal(args, out)
		}
	}
	runToolEnv(t, tool, `{"session_id":"`+f.alice.ID+`"}`, f.aliceEnv)
	// A fork of the chat of bob has no key, and reads as bob.
	fork, _ := f.store.Create(context.Background(), sessions.Session{ParentID: f.bob.ID, Kind: "subagent", Source: "telegram"})
	forkEnv := &Env{SessionID: fork.ID, Channel: "telegram"}
	if out := runToolEnv(t, tool, `{"query":"zebra","limit":20}`, forkEnv); !strings.Contains(out, "1 hits") || !strings.Contains(out, f.bob.ID) {
		t.Fatal(out)
	}
}

func TestCronJobScope(t *testing.T) {
	f := newScopeFixture(t)
	tool := CronJobTool{Store: f.jobs, Sessions: f.store}
	created := runToolEnv(t, tool, `{"action":"create","name":"alice job","prompt":"p","schedule":"every 1h"}`, f.aliceEnv)
	id := strings.Fields(strings.SplitN(created, "\n", 2)[0])[2]
	if out := runToolEnv(t, tool, `{"action":"list"}`, f.bobEnv); !strings.Contains(out, "no cron jobs") {
		t.Fatal("bob must not see the job of alice:", out)
	}
	for _, action := range []string{"pause", "remove", "run"} {
		if out := runToolEnvErr(t, tool, `{"action":"`+action+`","job_id":"`+id+`"}`, f.bobEnv); !strings.Contains(out, "not found") {
			t.Fatal(action, out)
		}
	}
	if out := runToolEnvErr(t, tool, `{"action":"update","job_id":"`+id+`","prompt":"x"}`, f.bobEnv); !strings.Contains(out, "not found") {
		t.Fatal(out)
	}
	if out := runToolEnv(t, tool, `{"action":"list"}`, f.aliceEnv); !strings.Contains(out, id) {
		t.Fatal(out)
	}
	if out := runToolEnv(t, tool, `{"action":"list"}`, f.localEnv); !strings.Contains(out, id) {
		t.Fatal("the local owner sees all jobs:", out)
	}
	// Delivery stays in the chat of the caller.
	if out := runToolEnvErr(t, tool, `{"action":"update","job_id":"`+id+`","deliver":"telegram:-5"}`, f.aliceEnv); !strings.Contains(out, "outside this chat") {
		t.Fatal(out)
	}
	runToolEnv(t, tool, `{"action":"update","job_id":"`+id+`","deliver":"telegram:100:3"}`, f.aliceEnv)
	runToolEnv(t, tool, `{"action":"update","job_id":"`+id+`","deliver":"telegram:-5"}`, f.localEnv)
	// A run of the job of alice reads the sessions of alice only.
	run, _ := f.store.Create(context.Background(), sessions.Session{Key: "cron:" + id, Source: "cron", Kind: "cron"})
	search := SessionSearchTool{Store: f.store, Cron: f.jobs}
	out := runToolEnv(t, search, `{"query":"zebra","limit":20}`, &Env{SessionID: run.ID, SessionKey: run.Key, Channel: "cron"})
	if !strings.Contains(out, "1 hits") || !strings.Contains(out, f.alice.ID) {
		t.Fatal(out)
	}
}
