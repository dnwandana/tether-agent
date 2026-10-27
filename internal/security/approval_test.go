package security

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
)

// ctx is the context for the gate calls in the tests.
var ctx = context.Background()

// fixedJudge gives a fixed verdict.
type fixedJudge struct {
	risky bool
	err   error
	n     *int
}

func (j fixedJudge) Judge(context.Context, string, string) (bool, error) {
	if j.n != nil {
		*j.n++
	}
	return j.risky, j.err
}

// openGateDB opens a new state.db in a temporary folder.
func openGateDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// newGate returns a gate with its own state.db.
func newGate(t *testing.T, mode, cronMode string) *Gate {
	t.Helper()
	return &Gate{Mode: mode, CronMode: cronMode, Timeout: 5 * time.Second, DB: openGateDB(t)}
}

// alwaysApprove answers every question with "once".
type alwaysApprove struct{}

func (alwaysApprove) PromptApproval(context.Context, ApprovalRequest) (Scope, error) {
	return ScopeOnce, nil
}

// countingPrompter gives a fixed answer and counts the questions.
type countingPrompter struct {
	mu     sync.Mutex
	answer Scope
	err    error
	n      int
	last   ApprovalRequest
}

func (p *countingPrompter) PromptApproval(_ context.Context, req ApprovalRequest) (Scope, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	p.last = req
	return p.answer, p.err
}

// neverAnswers waits until the context ends.
type neverAnswers struct{}

func (neverAnswers) PromptApproval(ctx context.Context, _ ApprovalRequest) (Scope, error) {
	<-ctx.Done()
	return ScopeTimeout, ctx.Err()
}

// ignoresContext never returns. The gate must still apply its timeout.
type ignoresContext struct{ stop chan struct{} }

func (p ignoresContext) PromptApproval(context.Context, ApprovalRequest) (Scope, error) {
	<-p.stop
	return ScopeOnce, nil
}

// blockedReason returns the reason of a *BlockedError, or fails the test.
func blockedReason(t *testing.T, err error) string {
	t.Helper()
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("want *BlockedError, got %v", err)
	}
	if be.Error() != be.Reason {
		t.Fatalf("Error() = %q, want the reason %q", be.Error(), be.Reason)
	}
	return be.Reason
}

func TestHardlineBlocksInEveryMode(t *testing.T) {
	for _, mode := range []string{"manual", "smart", "off"} {
		g := newGate(t, mode, "approve")
		err := g.Check(ctx, ApprovalRequest{Command: "rm -rf /", Kind: "cron"}, alwaysApprove{})
		var be *BlockedError
		if !errors.As(err, &be) || !strings.Contains(be.Reason, "hardline") {
			t.Fatal(mode, err)
		}
	}
}

func TestHardlineIgnoresSessionAndAlways(t *testing.T) {
	g := newGate(t, "manual", "approve")
	// Approve the dangerous rules that a hardline command also matches.
	for _, c := range []string{"rm -rf /tmp/x", "rm -r foo"} {
		p, _ := MatchDangerous(c)
		g.approveSession("s", p.ID)
		if err := g.saveAlways(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	p := &countingPrompter{answer: ScopeAlways}
	for _, c := range []string{"rm -rf /", "rm -rf ~", "mkfs.ext4 /dev/sda1", "dd if=/dev/zero of=/dev/sda", ":(){ :|:& };:", "kill -1", "sudo reboot"} {
		reason := blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: c}, p))
		if !strings.HasPrefix(reason, "BLOCKED (hardline): ") {
			t.Fatalf("%s: %q", c, reason)
		}
	}
	if p.n != 0 {
		t.Fatal("the gate asked about a hardline command")
	}
}

func TestManualAsksForDangerousOnly(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeOnce}
	if g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "ls"}, p) != nil || p.n != 0 {
		t.Fatal("no ask for ls")
	}
	if g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p) != nil || p.n != 1 {
		t.Fatal("ask once")
	}
	g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p)
	if p.n != 2 {
		t.Fatal("once = this command only")
	}
}

func TestPrompterGetsPatternAndDescription(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeOnce}
	req := ApprovalRequest{SessionID: "s", SessionKey: "cli:user", Kind: "chat", Command: "chmod 777 /srv/www"}
	if err := g.Check(ctx, req, p); err != nil {
		t.Fatal(err)
	}
	want, _ := MatchDangerous(req.Command)
	if p.last.PatternID != want.ID || p.last.Description != want.Description || p.last.Command != req.Command || p.last.SessionKey != "cli:user" {
		t.Fatalf("%+v", p.last)
	}
}

func TestSessionScope(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeSession}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "a", Command: "rm -rf /tmp/x"}, p); err != nil {
		t.Fatal(err)
	}
	// The same rule (delete_in_root_path) with a different command: no question.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "a", Command: "rm -rf /tmp/y"}, p); err != nil || p.n != 1 {
		t.Fatal("same pattern in same session asked again", p.n, err)
	}
	// A different rule in the same session: a question.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "a", Command: "chmod 777 x"}, p); err != nil || p.n != 2 {
		t.Fatal("other pattern not asked", p.n, err)
	}
	// The same rule in another session: a question.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "b", Command: "rm -rf /tmp/x"}, p); err != nil || p.n != 3 {
		t.Fatal("other session not asked", p.n, err)
	}
	// ResetSession removes the session answers.
	g.ResetSession("a")
	if err := g.Check(ctx, ApprovalRequest{SessionID: "a", Command: "rm -rf /tmp/x"}, p); err != nil || p.n != 4 {
		t.Fatal("reset session not asked", p.n, err)
	}
}

func TestAlwaysPersistsAcrossGates(t *testing.T) {
	d := openGateDB(t)
	g1 := &Gate{Mode: "manual", CronMode: "deny", Timeout: time.Second, DB: d}
	p := &countingPrompter{answer: ScopeAlways}
	if err := g1.Check(ctx, ApprovalRequest{SessionID: "a", Command: "rm -rf /tmp/x"}, p); err != nil {
		t.Fatal(err)
	}
	pat, _ := MatchDangerous("rm -rf /tmp/x")
	var n int
	if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM approvals_always WHERE pattern_id=? AND created_at > 0", pat.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("approvals_always rows = %d, %v", n, err)
	}
	g2 := &Gate{Mode: "manual", CronMode: "deny", Timeout: time.Second, DB: d}
	if err := g2.Check(ctx, ApprovalRequest{SessionID: "other", Command: "rm -rf /tmp/z"}, p); err != nil || p.n != 1 {
		t.Fatal("always answer asked again", p.n, err)
	}
	// A second "Always" answer for the same rule does not fail.
	if err := g2.saveAlways(ctx, pat.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDenyAndTimeout(t *testing.T) {
	g := newGate(t, "manual", "deny")
	g.Timeout = 50 * time.Millisecond
	err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, neverAnswers{})
	if err == nil || !strings.Contains(err.Error(), "denied (timeout)") {
		t.Fatal(err)
	}
}

// Port of Hermes TestApprovalTimeoutIsNotConsent: silence is not consent,
// and the text names the evasion paths.
func TestTimeoutIsNotConsent(t *testing.T) {
	g := newGate(t, "manual", "deny")
	g.Timeout = 50 * time.Millisecond
	reason := blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf .git"}, neverAnswers{}))
	for _, want := range []string{"BLOCKED", "denied (timeout)", "recursive delete", "NOT consented", "Silence is not consent", "Do NOT retry", "rephrase", "different command"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("%q does not contain %q", reason, want)
		}
	}
}

func TestTimeoutWhenPrompterIgnoresContext(t *testing.T) {
	g := newGate(t, "manual", "deny")
	g.Timeout = 50 * time.Millisecond
	stop := make(chan struct{})
	defer close(stop)
	err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, ignoresContext{stop: stop})
	if err == nil || !strings.Contains(err.Error(), "denied (timeout)") {
		t.Fatal(err)
	}
}

func TestPrompterTimeoutAnswer(t *testing.T) {
	g := newGate(t, "manual", "deny")
	err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, &countingPrompter{answer: ScopeTimeout})
	if err == nil || !strings.Contains(err.Error(), "denied (timeout)") {
		t.Fatal(err)
	}
}

// Port of Hermes test_explicit_deny_carries_same_no_consent_shape.
func TestExplicitDeny(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeDeny}
	reason := blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf .git"}, p))
	if !strings.HasPrefix(reason, "BLOCKED by user: recursive delete") {
		t.Fatal(reason)
	}
	if !strings.Contains(reason, "NOT consented") || !strings.Contains(reason, "rephrase") || strings.Contains(reason, "Silence is not consent") {
		t.Fatal(reason)
	}
	// A denial does not approve the session.
	g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf .git"}, p)
	if p.n != 2 {
		t.Fatal("deny was remembered")
	}
}

func TestUnknownAnswerDenies(t *testing.T) {
	g := newGate(t, "manual", "deny")
	reason := blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, &countingPrompter{answer: "maybe"}))
	if !strings.HasPrefix(reason, "BLOCKED by user: ") {
		t.Fatal(reason)
	}
}

func TestPrompterErrorBlocks(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeOnce, err: errors.New("telegram down")}
	reason := blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p))
	if !strings.Contains(reason, "Do NOT retry") || !strings.Contains(reason, "telegram down") {
		t.Fatal(reason)
	}
}

func TestNoPrompterBlocks(t *testing.T) {
	g := newGate(t, "manual", "deny")
	blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, nil))
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "ls -la"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledContextBlocks(t *testing.T) {
	g := newGate(t, "manual", "deny")
	c, cancel := context.WithCancel(ctx)
	cancel()
	reason := blockedReason(t, g.Check(c, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, neverAnswers{}))
	if strings.Contains(reason, "timeout") {
		t.Fatal(reason)
	}
}

// Ports of Hermes tests/tools/test_cron_approval_mode.py.
func TestCronDenyMode(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeOnce}
	reason := blockedReason(t, g.Check(ctx, ApprovalRequest{Kind: "cron", Command: "rm -rf /tmp/stuff"}, p))
	if !strings.HasPrefix(reason, "BLOCKED: ") || !strings.Contains(reason, "cron_mode") || !strings.Contains(reason, "delete") {
		t.Fatal(reason)
	}
	for _, c := range []string{"chmod 777 /etc/passwd", "git push --force origin main"} {
		blockedReason(t, g.Check(ctx, ApprovalRequest{Kind: "cron", Command: c}, p))
	}
	if err := g.Check(ctx, ApprovalRequest{Kind: "cron", Command: "ls -la"}, p); err != nil {
		t.Fatal("safe command in cron deny mode", err)
	}
	if p.n != 0 {
		t.Fatal("prompter called for cron")
	}
	// Smart mode does not change the cron result.
	g.Mode, g.Judge = "smart", fixedJudge{risky: false}
	blockedReason(t, g.Check(ctx, ApprovalRequest{Kind: "cron", Command: "rm -rf /tmp/stuff"}, p))
}

func TestCronApproveMode(t *testing.T) {
	g := newGate(t, "manual", "approve")
	p := &countingPrompter{answer: ScopeDeny}
	if err := g.Check(ctx, ApprovalRequest{Kind: "cron", Command: "rm -rf /tmp/stuff"}, p); err != nil || p.n != 0 {
		t.Fatal(err, p.n)
	}
	// The sudo guard still applies.
	blockedReason(t, g.Check(ctx, ApprovalRequest{Kind: "cron", Command: "echo pw | sudo -S ls"}, p))
}

func TestCronAlwaysAnswerRuns(t *testing.T) {
	g := newGate(t, "manual", "deny")
	pat, _ := MatchDangerous("rm -rf /tmp/stuff")
	if err := g.saveAlways(ctx, pat.ID); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(ctx, ApprovalRequest{Kind: "cron", Command: "rm -rf /tmp/stuff"}, nil); err != nil {
		t.Fatal(err)
	}
}

// A child of a cron run has no user either. Hermes sets HERMES_CRON_SESSION for the
// full process, so the children of a cron job also follow cron_mode.
func TestCronChildFollowsCronMode(t *testing.T) {
	g := newGate(t, "manual", "approve")
	req := ApprovalRequest{SessionID: "c1", Kind: "subagent", RootKind: "cron", Command: "rm -rf /tmp/stuff"}
	if err := g.Check(ctx, req, nil); err != nil {
		t.Fatal(err)
	}
	g.CronMode = "deny"
	p := &countingPrompter{answer: ScopeOnce}
	reason := blockedReason(t, g.Check(ctx, req, p))
	if !strings.Contains(reason, "cron_mode") || p.n != 0 {
		t.Fatal(reason, p.n)
	}
}

func TestCronModeParsing(t *testing.T) {
	for in, want := range map[string]bool{
		"": false, "deny": false, "approve": true, "off": true, "allow": true, "yes": true,
		"APPROVE": true, " Approve ": true, "bogus": false, "false": false,
	} {
		if got := cronApproves(in); got != want {
			t.Fatalf("%q: got %v", in, got)
		}
	}
}

func TestOffModeRunsDangerous(t *testing.T) {
	g := newGate(t, "off", "deny")
	p := &countingPrompter{answer: ScopeDeny}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p); err != nil || p.n != 0 {
		t.Fatal(err, p.n)
	}
	// Hermes: approvals.mode=off also wins over cron_mode=deny.
	if err := g.Check(ctx, ApprovalRequest{Kind: "cron", Command: "rm -rf /tmp/x"}, p); err != nil {
		t.Fatal(err)
	}
	reason := blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "echo hunter2 | sudo -S apt install x"}, p))
	if !strings.Contains(reason, "sudo -S") {
		t.Fatal(reason)
	}
	g.SudoPasswordSet = true
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "echo hunter2 | sudo -S ls"}, p); err != nil {
		t.Fatal("sudo guard is off when a sudo password is set", err)
	}
}

func TestUnknownModeAsks(t *testing.T) {
	g := newGate(t, "bogus", "deny")
	p := &countingPrompter{answer: ScopeOnce}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p); err != nil || p.n != 1 {
		t.Fatal(err, p.n)
	}
}

// Port of Hermes TestNormalizationBypass: hidden characters do not get past
// the gate.
func TestNormalizedCommandStillAsks(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeDeny}
	for _, c := range []string{"r\u200bm -rf /tmp/x", "\uff52\uff4d -rf /tmp/x", "\x1b[31mrm\x1b[0m -rf /tmp/x"} {
		blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: c}, p))
	}
	if p.n != 3 {
		t.Fatal(p.n)
	}
	blockedReason(t, g.Check(ctx, ApprovalRequest{Command: "rm -rf \u200b/"}, p))
}

func TestDefaultTimeoutIs600s(t *testing.T) {
	g := NewGate(config.Default().Approvals, nil)
	if g.Timeout != 600*time.Second || g.Mode != "manual" || g.CronMode != "deny" {
		t.Fatalf("%+v", g)
	}
	if (&Gate{}).timeout() != 600*time.Second {
		t.Fatal("zero Timeout must use 600 s")
	}
}

func TestNoDBStillWorks(t *testing.T) {
	g := &Gate{Mode: "manual", CronMode: "deny", Timeout: time.Second}
	p := &countingPrompter{answer: ScopeAlways}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p); err != nil {
		t.Fatal(err)
	}
	// Without a DB, "Always" holds for the session.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p); err != nil || p.n != 1 {
		t.Fatal(err, p.n)
	}
}
