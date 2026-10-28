package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// ReviewAllowedTools are the only tools that a review run can call. The run
// sees the tool schemas of the parent, so the dispatcher enforces this list.
var ReviewAllowedTools = []string{"memory", "skill_view", "skill_manage"}

// reviewDeny is the tool result of a call to a tool outside ReviewAllowedTools.
const reviewDeny = "review runs may only use memory, skill_view, skill_manage"

// reviewMaxTurns limits the tool rounds of a review run (Hermes max_iterations=16).
const reviewMaxTurns = 16

// MemoryReviewPrompt asks the review run to save facts about the user. It is the text of
// Hermes _MEMORY_REVIEW_PROMPT.
const MemoryReviewPrompt = "Review the conversation above and consider saving to memory if appropriate.\n\n" +
	"Focus on:\n" +
	"1. Has the user revealed things about themselves — their persona, desires, " +
	"preferences, or personal details worth remembering?\n" +
	"2. Has the user expressed expectations about how you should behave, their work " +
	"style, or ways they want you to operate?\n\n" +
	"If something stands out, save it using the memory tool. " +
	"If nothing is worth saving, just say 'Nothing to save.' and stop."

// skillProtected is the part of the Hermes skill prompts about the skills that the review
// must not edit. The names are the Tether Agent names.
const skillProtected = "Protected skills (DO NOT edit these):\n" +
	"  • Bundled skills (shipped with Tether Agent, e.g. 'tether-self').\n" +
	"  • Skills from the external skill folders (skills.external_dirs). They are read-only.\n" +
	"If the only skills that need updating are protected, say\n" +
	"'Nothing to save.' and stop.\n\n"

// skillDoNotCapture is the part of the Hermes skill prompts about the things that are not
// skills.
const skillDoNotCapture = "  • Environment-dependent failures: missing binaries, fresh-install " +
	"errors, post-migration path mismatches, 'command not found', " +
	"unconfigured credentials, uninstalled packages. The user can fix " +
	"these — they are not durable rules.\n" +
	"  • Negative claims about tools or features ('browser tools do not " +
	"work', 'X tool is broken', 'cannot use Y from run_script'). These " +
	"harden into refusals the agent cites against itself for months " +
	"after the actual problem was fixed.\n" +
	"  • Session-specific transient errors that resolved before the " +
	"conversation ended. If retrying worked, the lesson is the retry " +
	"pattern, not the original failure.\n" +
	"  • One-off task narratives. A user asking 'summarize today's " +
	"market' or 'analyze this PR' is not a class of work that warrants " +
	"a skill.\n\n" +
	"If a tool failed because of setup state, capture the FIX (install " +
	"command, config step, env var to set) under an existing setup or " +
	"troubleshooting skill — never 'this tool does not work' as a " +
	"standalone constraint.\n\n"

// SkillReviewPrompt asks the review run to update the skill library. It is the text of
// Hermes _SKILL_REVIEW_PROMPT, with the Tether Agent tool and skill names.
const SkillReviewPrompt = "Review the conversation above and update the skill library. Be " +
	"ACTIVE — most sessions produce at least one skill update, even if " +
	"small. A pass that does nothing is a missed learning opportunity, " +
	"not a neutral outcome.\n\n" +
	"Target shape of the library: CLASS-LEVEL skills, each with a rich " +
	"SKILL.md and a `references/` directory for session-specific detail. " +
	"Not a long flat list of narrow one-session-one-skill entries. This " +
	"shapes HOW you update, not WHETHER you update.\n\n" +
	"Signals to look for (any one of these warrants action):\n" +
	"  • User corrected your style, tone, format, legibility, or " +
	"verbosity. Frustration signals like 'stop doing X', 'this is too " +
	"verbose', 'don't format like this', 'why are you explaining', " +
	"'just give me the answer', 'you always do Y and I hate it', or an " +
	"explicit 'remember this' are FIRST-CLASS skill signals, not just " +
	"memory signals. Update the relevant skill(s) to embed the " +
	"preference so the next session starts already knowing.\n" +
	"  • User corrected your workflow, approach, or sequence of steps. " +
	"Encode the correction as a pitfall or explicit step in the skill " +
	"that governs that class of task.\n" +
	"  • Non-trivial technique, fix, workaround, debugging path, or " +
	"tool-usage pattern emerged that a future session would benefit " +
	"from. Capture it.\n" +
	"  • A skill that got loaded or consulted this session turned out " +
	"to be wrong, missing a step, or outdated. Patch it NOW.\n\n" +
	"Preference order — prefer the earliest action that fits, but do " +
	"pick one when a signal above fired:\n" +
	"  1. UPDATE A CURRENTLY-LOADED SKILL. Look back through the " +
	"conversation for skills you read via skill_view. If any of them " +
	"covers the territory of the new learning, PATCH that one first. It " +
	"is the skill that was in play, so it's the right one to extend.\n" +
	"  2. UPDATE AN EXISTING UMBRELLA (via the SKILLS index + skill_view). " +
	"If no loaded skill fits but an existing class-level skill does, " +
	"patch it. Add a subsection, a pitfall, or broaden a trigger.\n" +
	"  3. ADD A SUPPORT FILE under an existing umbrella. Use the right " +
	"directory per kind:\n" +
	"     • `references/<topic>.md` — session-specific detail (error " +
	"transcripts, reproduction recipes, provider quirks) AND " +
	"condensed knowledge banks: quoted research, API docs, external " +
	"authoritative excerpts, or domain notes you found while working " +
	"on the problem. Write it concise and for the value of the task, " +
	"not as a full mirror of upstream docs.\n" +
	"     • `scripts/<name>.<ext>` — statically re-runnable actions " +
	"the skill can invoke directly (verification scripts, fixture " +
	"generators, deterministic probes, anything the agent should run " +
	"rather than hand-type each time).\n" +
	"     Add support files via skill_manage action=write_file with " +
	"file starting 'references/' or 'scripts/'. " +
	"The umbrella's SKILL.md should gain a one-line pointer to any " +
	"new support file so future agents know it exists.\n" +
	"  4. CREATE A NEW CLASS-LEVEL UMBRELLA SKILL when no existing " +
	"skill covers the class. The name MUST be at the class level. " +
	"The name MUST NOT be a specific PR number, error string, feature " +
	"codename, library-alone name, or 'fix-X / debug-Y / audit-Z-today' " +
	"session artifact. If the proposed name only makes sense for " +
	"today's task, it's wrong — fall back to (1), (2), or (3).\n\n" +
	"User-preference embedding (important): when the user expressed a " +
	"style/format/workflow preference, the update belongs in the " +
	"SKILL.md body, not just in memory. Memory captures 'who the user " +
	"is and what the current situation and state of your operations " +
	"are'; skills capture 'how to do this class of task for this " +
	"user'. When they complain about how you handled a task, the " +
	"skill that governs that task needs to carry the lesson.\n\n" +
	"If you notice two existing skills that overlap, note it in your reply.\n\n" +
	skillProtected +
	"Do NOT capture (these become persistent self-imposed constraints " +
	"that bite you later when the environment changes):\n" +
	skillDoNotCapture +
	"'Nothing to save.' is a real option but should NOT be the " +
	"default. If the session ran smoothly with no corrections and " +
	"produced no new technique, just say 'Nothing to save.' and stop. " +
	"Otherwise, act."

// CombinedReviewPrompt asks for a memory review and a skill review in one run. It is the
// text of Hermes _COMBINED_REVIEW_PROMPT, with the Tether Agent tool and skill names.
const CombinedReviewPrompt = "Review the conversation above and update two things:\n\n" +
	"**Memory**: who the user is. Did the user reveal persona, " +
	"desires, preferences, personal details, or expectations about " +
	"how you should behave? Save facts about the user and durable " +
	"preferences with the memory tool.\n\n" +
	"**Skills**: how to do this class of task. Be ACTIVE — most " +
	"sessions produce at least one skill update. A pass that does " +
	"nothing is a missed learning opportunity, not a neutral outcome.\n\n" +
	"Target shape of the skill library: CLASS-LEVEL skills with a rich " +
	"SKILL.md and a `references/` directory for session-specific detail. " +
	"Not a long flat list of narrow one-session-one-skill entries.\n\n" +
	"Signals that warrant a skill update (any one is enough):\n" +
	"  • User corrected your style, tone, format, legibility, " +
	"verbosity, or approach. Frustration is a FIRST-CLASS skill " +
	"signal, not just a memory signal. 'stop doing X', 'don't format " +
	"like this', 'I hate when you Y' — embed the lesson in the skill " +
	"that governs that task so the next session starts fixed.\n" +
	"  • Non-trivial technique, fix, workaround, or debugging path " +
	"emerged.\n" +
	"  • A skill that was loaded or consulted turned out wrong, " +
	"missing, or outdated — patch it now.\n\n" +
	"Preference order for skills — pick the earliest that fits:\n" +
	"  1. UPDATE A CURRENTLY-LOADED SKILL. Check what skills were " +
	"loaded via skill_view in the conversation. If one " +
	"of them covers the learning, PATCH it first. It was in play; " +
	"it's the right place.\n" +
	"  2. UPDATE AN EXISTING UMBRELLA (the SKILLS index + skill_view to " +
	"find the right one). Patch it.\n" +
	"  3. ADD A SUPPORT FILE under an existing umbrella via " +
	"skill_manage action=write_file. Two kinds: " +
	"`references/<topic>.md` for session-specific detail OR condensed " +
	"knowledge banks (quoted research, API docs excerpts, domain " +
	"notes) written concise and task-focused; " +
	"`scripts/<name>.<ext>` for statically re-runnable actions " +
	"(verification, fixture generators, probes). Add a one-line " +
	"pointer in SKILL.md so future agents find them.\n" +
	"  4. CREATE A NEW CLASS-LEVEL UMBRELLA when nothing exists. " +
	"Name at the class level — NOT a PR number, error string, " +
	"codename, library-alone name, or 'fix-X / debug-Y' session " +
	"artifact. If the name only fits today's task, fall back to (1), " +
	"(2), or (3).\n\n" +
	"User-preference embedding: when the user complains about how " +
	"you handled a task, update the skill that governs that task — " +
	"memory alone isn't enough. Memory says 'who the user is and " +
	"what the current situation and state of your operations are'; " +
	"skills say 'how to do this class of task for this user'. Both " +
	"should carry user-preference lessons when relevant.\n\n" +
	"If you notice overlapping existing skills, mention it.\n\n" +
	skillProtected +
	"Do NOT capture as skills (these become persistent self-imposed " +
	"constraints that bite you later when the environment changes):\n" +
	skillDoNotCapture +
	"Act on whichever of the two dimensions has real signal. If " +
	"genuinely nothing stands out on either, say 'Nothing to save.' " +
	"and stop — but don't reach for that conclusion as a default."

// reviewToolNote ends each review prompt (Hermes).
const reviewToolNote = "\n\nYou can only call memory and skill management tools. Other tools will be denied " +
	"at runtime — do not attempt them."

// CompactionFocusNote asks a review at compaction to look at the messages that the
// compaction removes. The two numbers are the first and the last message, from 1.
const CompactionFocusNote = "\n\nFocus on messages %d to %d of the conversation above. Compaction removes " +
	"them from the context soon. Save what is worth keeping from them now."

// ReviewRequest describes one background review.
type ReviewRequest struct {
	Session *sessions.Session
	// View is a copy of the model view of the parent session.
	View []provider.Message
	// System and Tools are the system prompt and the tool schemas of the last parent
	// request. The review sends the same bytes, so the prompt cache hits.
	System string
	Tools  []provider.ToolSchema
	// Memory and Skills select the review prompts.
	Memory, Skills bool
	// FocusRange is View[start:end], the part that a compaction removes. It can be nil.
	FocusRange *[2]int
	// Notify receives one notice for each saved change. It can be nil.
	Notify func(text string)
	// ParentEnv gives the work dir, the data dir, and the logger of the parent. It can be nil.
	ParentEnv *tools.Env
}

// RunReview runs one review: a fork of the parent conversation that can only save memory
// and skills. The parent session never changes. It returns the notices of the
// saved changes, and sends each notice to r.Notify.
func (a *Agent) RunReview(ctx context.Context, r ReviewRequest) (saved []string, err error) {
	if r.Session == nil {
		return nil, errors.New("agent: the review has no session")
	}
	if r.Session.Kind == KindReview {
		return nil, errors.New("agent: no reviews inside review runs")
	}
	if !r.Memory && !r.Skills {
		return nil, errors.New("agent: the review has nothing to do; set Memory or Skills")
	}
	sess, err := a.forkSession(ctx, r.Session, KindReview, "review", r.Session.ModelOverride)
	if err != nil {
		return nil, err
	}
	allow := map[string]bool{}
	if a.Tools != nil {
		for _, name := range ReviewAllowedTools {
			if _, ok := a.Tools.Get(name); ok {
				allow[name] = true
			}
		}
	}
	var (
		mu   sync.Mutex
		seen = map[string]bool{}
	)
	onResult := func(c provider.ToolCall, res tools.Result) {
		n := reviewNotice(c, res)
		mu.Lock()
		defer mu.Unlock()
		if n != "" && !seen[n] {
			seen[n] = true
			saved = append(saved, n)
		}
	}
	defer func() {
		if r.Notify != nil {
			for _, n := range saved {
				r.Notify(n)
			}
		}
	}()
	limits := delegationLimits(a.config())
	cctx, cancel := context.WithTimeout(ctx, time.Duration(limits.ChildTimeoutS)*time.Second)
	defer cancel()
	env := childEnv(r.ParentEnv, sess)
	defer releaseApprovals(env)
	_, err = a.RunTurn(cctx, Turn{
		Session:  sess,
		UserText: reviewPrompt(r.Memory, r.Skills, r.FocusRange),
		Env:      env,
		System:   r.System,
		MaxTurns: reviewMaxTurns,
		review: &reviewOpts{
			history:  append([]provider.Message(nil), r.View...),
			schemas:  r.Tools,
			allow:    allow,
			deny:     reviewDeny,
			onResult: onResult,
		},
	})
	if err != nil {
		return saved, fmt.Errorf("agent: review of %s: %w", r.Session.ID, err)
	}
	return saved, nil
}

// reviewPrompt returns the last user message of a review run.
func reviewPrompt(mem, skills bool, focus *[2]int) string {
	var p string
	switch {
	case mem && skills:
		p = CombinedReviewPrompt
	case mem:
		p = MemoryReviewPrompt
	default:
		p = SkillReviewPrompt
	}
	if focus != nil && focus[1] > focus[0] {
		p += fmt.Sprintf(CompactionFocusNote, focus[0]+1, focus[1])
	}
	return p + reviewToolNote
}

// reviewNotice returns the notice for one successful memory or skill write, or "".
func reviewNotice(c provider.ToolCall, res tools.Result) string {
	if res.IsError {
		return ""
	}
	var args struct {
		Action string `json:"action"`
		Target string `json:"target"`
		Name   string `json:"name"`
	}
	if json.Unmarshal([]byte(c.Args), &args) != nil {
		return ""
	}
	switch c.Name {
	case "memory":
		if args.Target == "user" {
			return "💾 User profile updated"
		}
		return "💾 Memory updated"
	case "skill_manage":
		name := strings.TrimSpace(args.Name)
		switch args.Action {
		case "create":
			return "🧩 Skill created: " + name
		case "delete":
			return "🧩 Skill deleted: " + name
		default:
			return "🧩 Skill updated: " + name
		}
	}
	return ""
}

// HoldNotices holds the review notices until the main answer is delivered. A
// notice that arrives before release waits. release sends the held notices in order, and
// later notices go out at once. The notices keep their order.
func HoldNotices(send func(string)) (notify func(string), release func()) {
	var (
		mu       sync.Mutex
		sendMu   sync.Mutex
		released bool
		held     []string
	)
	notify = func(text string) {
		mu.Lock()
		if !released {
			held = append(held, text)
			mu.Unlock()
			return
		}
		mu.Unlock()
		sendMu.Lock()
		defer sendMu.Unlock()
		send(text)
	}
	release = func() {
		// sendMu is taken first, so a notice after the release waits for the held ones.
		sendMu.Lock()
		defer sendMu.Unlock()
		mu.Lock()
		if released {
			mu.Unlock()
			return
		}
		released = true
		out := held
		held = nil
		mu.Unlock()
		for _, text := range out {
			send(text)
		}
	}
	return notify, release
}

// reviewMinUserTurns is the number of user turns that a session needs before a review at
// compaction or at /new.
const reviewMinUserTurns = 6

// ReviewCounters counts, for each session, the user turns and the tool rounds since the
// last review. It also keeps the system prompt, the tool schemas, the tool
// context, and the notice function of the last turn of each session. A review at
// compaction or at /new uses them, so its prompt cache hits. The zero value is ready to
// use. It is safe for concurrent use.
type ReviewCounters struct {
	mu sync.Mutex
	m  map[string]*reviewState
}

// reviewState is the review data of one session.
type reviewState struct {
	userTurns, toolRounds int
	// hydrated is true when userTurns includes the user turns before this process.
	hydrated bool
	// haveShape is true when system and schemas are set.
	haveShape bool
	system    string
	schemas   []provider.ToolSchema
	env       *tools.Env
	notify    func(string)
}

// state returns the data of the session. The caller holds c.mu.
func (c *ReviewCounters) state(id string) *reviewState {
	if c.m == nil {
		c.m = map[string]*reviewState{}
	}
	st := c.m[id]
	if st == nil {
		st = &reviewState{}
		c.m[id] = st
	}
	return st
}

// remember keeps the request shape and the notice function of the current turn.
func (c *ReviewCounters) remember(id, system string, schemas []provider.ToolSchema, env *tools.Env, notify func(string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state(id)
	st.haveShape, st.system, st.schemas, st.env = true, system, schemas, env
	if notify != nil {
		st.notify = notify
	}
}

// shape returns a copy of the data of the session.
func (c *ReviewCounters) shape(id string) reviewState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.state(id)
}

// reset sets both counters of the session to 0, because a full review starts.
func (c *ReviewCounters) reset(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.m[id]; st != nil {
		st.userTurns, st.toolRounds = 0, 0
	}
}

// forget removes the data of the session.
func (c *ReviewCounters) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id)
}

// noReviews reports whether a session of the kind never gets a review.
func noReviews(kind string) bool {
	return kind == "cron" || kind == KindSubagent || kind == KindReview
}

// MaybeStartReviews counts the turn and starts a background review when a count is
// reached. These rules apply:
//   - A memory review is due after memory.review_interval_turns user turns.
//   - A skill review is due after skills.review_interval_tool_rounds tool rounds.
//   - Two reviews that are due together run as one.
//   - A review starts only after a turn with a final answer.
//   - A cron, subagent, or review session never gets a review.
//   - A count needs its tool (memory or skill_manage) in lastReq.Tools.
//   - A turn that called the tool itself resets the count of the tool (Hermes).
//
// The review gets notify. MaybeStartReviews does not wait for the review.
func (a *Agent) MaybeStartReviews(ctx context.Context, sess *sessions.Session, lastReq provider.Request, res TurnResult, notify func(string)) {
	if sess == nil || noReviews(sess.Kind) {
		return
	}
	cfg := a.config()
	memEvery, skillEvery := cfg.Memory.ReviewIntervalTurns, cfg.Skills.ReviewIntervalToolRounds
	memOn := memEvery > 0 && hasSchema(lastReq.Tools, "memory")
	skillOn := skillEvery > 0 && hasSchema(lastReq.Tools, "skill_manage")
	if !memOn && !skillOn {
		return
	}
	c := &a.Reviews
	c.mu.Lock()
	hydrated := c.state(sess.ID).hydrated
	c.mu.Unlock()
	prior := 0
	if !hydrated && memOn {
		// A new process starts the count from the stored user turns, and keeps the
		// 1-in-N cadence (Hermes). The turn that just ended is not a prior turn.
		if n, err := a.userTurns(ctx, sess.ID); err == nil && n > 0 {
			prior = (n - 1) % memEvery
		} else if err != nil {
			a.logger().Warn("agent: cannot count the user turns", "session", sess.ID, "err", err.Error())
		}
	}

	c.mu.Lock()
	st := c.state(sess.ID)
	if !st.hydrated {
		st.hydrated = true
		st.userTurns = prior
	}
	if memOn {
		st.userTurns++
		if res.usedMemory {
			st.userTurns = 0
		}
	}
	if skillOn {
		st.toolRounds += res.ToolRounds
		if res.usedSkills {
			st.toolRounds = 0
		}
	}
	mem := memOn && st.userTurns >= memEvery
	skill := skillOn && st.toolRounds >= skillEvery
	// A due review waits for a turn with a final answer.
	if res.Final == "" || !mem && !skill {
		c.mu.Unlock()
		return
	}
	if mem {
		st.userTurns = 0
	}
	if skill {
		st.toolRounds = 0
	}
	env := st.env
	c.mu.Unlock()

	a.startReview(ctx, ReviewRequest{Session: sess, System: lastReq.System, Tools: lastReq.Tools,
		Memory: mem, Skills: skill, Notify: notify, ParentEnv: env})
}

// ReviewOnCompaction starts one memory and skill review of the part of the view that a
// compaction replaces: view[start:end]. It needs a session with 6 or more user
// turns. It returns at once, and the review runs in the background. It is the
// Compactor.OnCompact hook.
func (a *Agent) ReviewOnCompaction(ctx context.Context, sess *sessions.Session, view []provider.Message, start, end int) {
	if sess == nil || noReviews(sess.Kind) {
		return
	}
	// Read the shape now: the turn rebuilds its prompt after the compaction.
	r, ok := a.fullReview(ctx, sess)
	if !ok {
		return
	}
	r.View = append([]provider.Message(nil), view...)
	r.FocusRange = &[2]int{start, end}
	a.goReview(ctx, func(ctx context.Context) {
		if !a.enoughTurns(ctx, sess) {
			return
		}
		a.Reviews.reset(sess.ID)
		a.startReview(ctx, r)
	})
}

// ReviewBeforeNew starts one memory and skill review of a session that /new replaces.
// It needs a session with 6 or more user turns. It returns at once, and the
// review runs in the background. It is the gateway.Deps.BeforeNew hook.
func (a *Agent) ReviewBeforeNew(ctx context.Context, old *sessions.Session) {
	if old == nil || noReviews(old.Kind) {
		return
	}
	r, ok := a.fullReview(ctx, old)
	// The session ends here, so its counters go.
	a.Reviews.forget(old.ID)
	if !ok {
		return
	}
	a.goReview(ctx, func(ctx context.Context) {
		if !a.enoughTurns(ctx, old) {
			return
		}
		a.startReview(ctx, r)
	})
}

// fullReview returns a memory and skill review request of the session with the request
// shape of its last turn. ok is false when the registry has neither the memory tool nor
// skill_manage.
func (a *Agent) fullReview(ctx context.Context, sess *sessions.Session) (r ReviewRequest, ok bool) {
	mem, skill := a.hasTool("memory"), a.hasTool("skill_manage")
	if !mem && !skill {
		return r, false
	}
	st := a.Reviews.shape(sess.ID)
	r = ReviewRequest{Session: sess, Memory: mem, Skills: skill, Notify: st.notify, ParentEnv: st.env,
		System: st.system, Tools: st.schemas}
	if !st.haveShape {
		// No turn of this session ran in this process: use the frozen prompt and the
		// tools of the channel.
		if a.Prompts != nil {
			r.System, _ = a.Prompts.Get(ctx, sess, a.promptKey(ctx, sess), sess.Source)
		}
		if a.Tools != nil {
			_, r.Tools = a.Tools.Visible(tools.Resolve(a.config().Toolsets[sess.Source], nil, nil), nil)
		}
	}
	return r, true
}

// enoughTurns reports whether the session has reviewMinUserTurns or more user turns.
func (a *Agent) enoughTurns(ctx context.Context, sess *sessions.Session) bool {
	n, err := a.userTurns(ctx, sess.ID)
	if err != nil {
		a.logger().Warn("agent: cannot count the user turns; no review", "session", sess.ID, "err", err.Error())
		return false
	}
	return n >= reviewMinUserTurns
}

// startReview runs the review in its own goroutine. A request without a view gets the
// current model view of the session.
func (a *Agent) startReview(ctx context.Context, r ReviewRequest) {
	a.goReview(ctx, func(ctx context.Context) {
		if r.View == nil {
			v, err := a.View(ctx, r.Session.ID)
			if err != nil {
				a.logger().Warn("agent: review not started", "session", r.Session.ID, "err", err.Error())
				return
			}
			r.View = v
		}
		if a.reviewHook != nil {
			a.reviewHook(r)
			return
		}
		a.logger().Info("review start", "session", r.Session.ID, "memory", r.Memory, "skills", r.Skills, "compaction", r.FocusRange != nil)
		saved, err := a.RunReview(ctx, r)
		if err != nil {
			a.logger().Warn("review failed", "session", r.Session.ID, "err", err.Error())
			return
		}
		a.logger().Info("review done", "session", r.Session.ID, "saved", len(saved))
	})
}

// goReview runs f in a background goroutine that WaitReviews counts. The context of f
// keeps the values of ctx, but the end of ctx does not stop f. The end of Agent.Background
// stops f. After the end of Agent.Background, goReview does not start f.
func (a *Agent) goReview(ctx context.Context, f func(context.Context)) {
	base := a.Background
	if base == nil {
		base = context.Background()
	}
	a.reviewMu.Lock()
	if base.Err() != nil {
		a.reviewMu.Unlock()
		return
	}
	a.reviewWG.Add(1)
	a.reviewMu.Unlock()
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(base, cancel)
	go func() {
		defer a.reviewWG.Done()
		defer stop()
		defer cancel()
		f(rctx)
	}()
}

// WaitReviews waits until every background review returns, or until timeout. It returns
// false at the timeout. Cancel Agent.Background first, else the running reviews go on.
func (a *Agent) WaitReviews(timeout time.Duration) bool {
	// A goReview that passed its check under the lock has called reviewWG.Add before
	// this point, so reviewWG.Wait starts under the lock and never runs before that
	// reviewWG.Add.
	done := make(chan struct{})
	a.reviewMu.Lock()
	go func() {
		a.reviewWG.Wait()
		close(done)
	}()
	a.reviewMu.Unlock()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// userTurns returns the number of user messages of the session. Observed group messages
// do not count.
func (a *Agent) userTurns(ctx context.Context, sessionID string) (int, error) {
	if a.DB != nil {
		var n int
		err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
			WHERE session_id = ? AND role = 'user' AND observed = 0`, sessionID).Scan(&n)
		return n, err
	}
	msgs, err := a.Store.Messages(ctx, sessionID)
	n := 0
	for _, m := range msgs {
		if m.Msg.Role == provider.RoleUser && !m.Meta.Observed {
			n++
		}
	}
	return n, err
}

// hasTool reports whether the registry has the tool.
func (a *Agent) hasTool(name string) bool {
	if a.Tools == nil {
		return false
	}
	_, ok := a.Tools.Get(name)
	return ok
}

// hasSchema reports whether the schemas include the tool.
func hasSchema(schemas []provider.ToolSchema, name string) bool {
	for _, s := range schemas {
		if s.Name == name {
			return true
		}
	}
	return false
}
