package goals

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/config"
)

// The prompt texts below are copies of the Hermes hermes_cli/goals.py texts. A "{name}"
// field in a template gets its value from fill, in one pass, like the Python str.format.

// ContinuationPrefix is the first line of the plain, contract, and subgoals continuation
// prompts. It marks a turn that the goal loop sends.
const ContinuationPrefix = "[Continuing toward your standing goal]"

// continuationTemplate is the Hermes CONTINUATION_PROMPT_TEMPLATE.
const continuationTemplate = ContinuationPrefix + "\n" +
	"Goal: {goal}\n\n" +
	"Continue working toward this goal. Take the next concrete step. " +
	"If you believe the goal is complete, state so explicitly and stop. " +
	"If you are blocked and need input from the user, say so clearly and stop."

// continuationContractTemplate is the Hermes CONTINUATION_PROMPT_WITH_CONTRACT_TEMPLATE.
const continuationContractTemplate = ContinuationPrefix + "\n" +
	"Goal: {goal}\n\n" +
	"Completion contract:\n" +
	"{contract_block}\n\n" +
	"Continue working toward the outcome above. Take the next concrete step. " +
	"Stay within the stated boundaries and do not violate the constraints. " +
	"Before claiming the goal is done, satisfy the Verification criterion and " +
	"show the concrete evidence (command output, file contents, test result). " +
	"If you hit the stated stop condition or are otherwise blocked and need " +
	"user input, say so clearly and stop."

// continuationSubgoalsTemplate is the Hermes CONTINUATION_PROMPT_WITH_SUBGOALS_TEMPLATE.
const continuationSubgoalsTemplate = ContinuationPrefix + "\n" +
	"Goal: {goal}\n\n" +
	"Additional criteria the user added mid-loop:\n" +
	"{subgoals_block}\n\n" +
	"Continue working toward the goal AND all additional criteria. Take " +
	"the next concrete step. If you believe the goal and every " +
	"additional criterion are complete, state so explicitly and stop. " +
	"If you are blocked and need input from the user, say so clearly " +
	"and stop."

// continuationGateTemplate is the Hermes CONTINUATION_PROMPT_GATE_FAILED_TEMPLATE.
const continuationGateTemplate = "[Continuing toward your standing goal — a quality gate failed]\n" +
	"Goal: {goal}\n\n" +
	"The quality gate command below must pass before this goal can be " +
	"declared done, and it just failed (attempt {attempt}/{max_retries}):\n" +
	"  $ {command}\n" +
	"Exit code: {exit_code}\n" +
	"Output (tail):\n" +
	"```\n" +
	"{output}\n" +
	"```\n\n" +
	"Fix the underlying problem so this gate passes, then re-run it to " +
	"confirm. Do not declare the goal complete while any gate fails. If the " +
	"gate itself is wrong or cannot pass, say so clearly and stop."

// JudgeSystemPrompt is the Hermes JUDGE_SYSTEM_PROMPT.
// The WAIT rule names only the pid, because Tether Agent has no watch patterns and no
// process session ids.
const JudgeSystemPrompt = "You are a strict judge evaluating whether an autonomous agent has " +
	"achieved a user's stated goal. You receive the goal text, the agent's " +
	"most recent response, and — when present — a list of background " +
	"processes the agent has running. Decide one of four verdicts.\n\n" +
	"DONE — the goal is fully satisfied:\n" +
	"- The response explicitly confirms the goal was completed, OR\n" +
	"- The response clearly shows the final deliverable was produced.\n" +
	"DONE requires the deliverable to actually exist. If the response only " +
	"explains why the goal cannot be reached, the verdict is BLOCKED, not " +
	"DONE.\n\n" +
	"BLOCKED — the goal cannot be satisfied as stated:\n" +
	"- The response explains the goal is genuinely unachievable (impossible, " +
	"out of scope, no valid path to the deliverable), or refuses to " +
	"fabricate a deliverable that cannot exist, OR\n" +
	"- The response explains progress is blocked and the next step needs " +
	"user input to proceed.\n" +
	"Return BLOCKED with the reason describing what is blocking. BLOCKED is " +
	"a refusal, not a completion — never return BLOCKED for a goal that " +
	"was achieved.\n" +
	"When the block is an error the agent hit (an HTTP status, an API, " +
	"sign-in or token failure), quote the error text verbatim in the reason " +
	"and attribute it only to a provider, service or credential the response " +
	"itself names. Never infer one the response does not name — an unnamed " +
	"401 belongs to the model provider the agent was calling, not to some " +
	"other service's token.\n\n" +
	"WAIT — the goal is NOT done, but the next step is to wait for async " +
	"work to finish rather than act again. Choose this ONLY when the agent's " +
	"progress is genuinely gated on something running on its own:\n" +
	"- A background process listed below is still running AND the response " +
	"shows the agent is waiting on its result (e.g. a CI poller, build, " +
	"test run, deploy). Return its pid in ``wait_on_pid`` (releases on exit only).\n" +
	"- The agent says it is rate-limited / backing off / must wait a fixed " +
	"period — return seconds in ``wait_for_seconds``.\n" +
	"- The agent has delegated subagents still running (stated below as " +
	"active delegations) and the response says it is waiting on them with " +
	"nothing else dispatchable — return ``wait_for_seconds`` between 600 and " +
	"1800. Their results wake the agent on their own; re-poking it now only " +
	"produces a status recap.\n" +
	"Picking WAIT parks the loop without burning a turn; it resumes " +
	"automatically when the pid exits or the time elapses. Do NOT pick WAIT " +
	"just because work remains — only when re-poking now would be pure " +
	"busy-work because the agent can't progress until the async thing " +
	"finishes.\n\n" +
	"CONTINUE — not done, and there is a concrete next step the agent can " +
	"take right now. This is the default when in doubt.\n\n" +
	"Reply ONLY with a single JSON object on one line. Shapes:\n" +
	`{"verdict": "done", "reason": "<one sentence>"}` + "\n" +
	`{"verdict": "blocked", "reason": "<one sentence>"}` + "\n" +
	`{"verdict": "continue", "reason": "<one sentence>"}` + "\n" +
	`{"verdict": "wait", "wait_on_session": "<id>", "reason": "<one sentence>"}` + "\n" +
	`{"verdict": "wait", "wait_on_pid": <int>, "reason": "<one sentence>"}` + "\n" +
	`{"verdict": "wait", "wait_for_seconds": <int>, "reason": "<one sentence>"}` + "\n" +
	"The legacy shape {\"done\": <true|false>, \"reason\": \"...\"} is still " +
	"accepted (true=done, false=continue)."

// judgeDelegationsTemplate is the Hermes JUDGE_DELEGATIONS_BLOCK_TEMPLATE.
const judgeDelegationsTemplate = "Active delegations: the agent has {count} delegated subagent batch(es) still running; " +
	"their results are delivered to it automatically when they finish.\n\n"

// judgeBackgroundTemplate is the Hermes JUDGE_BACKGROUND_BLOCK_TEMPLATE.
const judgeBackgroundTemplate = "Background processes the agent currently has running (it may be waiting " +
	"on one of these):\n{background_lines}\n\n"

// judgeUserTemplate is the Hermes JUDGE_USER_PROMPT_TEMPLATE.
const judgeUserTemplate = "Goal:\n{goal}\n\n" +
	"Agent's most recent response:\n{response}\n\n" +
	"{background_block}" +
	"Current time: {current_time}\n\n" +
	"Is the goal satisfied — done, blocked, continue, or wait?"

// judgeUserSubgoalsTemplate is the Hermes JUDGE_USER_PROMPT_WITH_SUBGOALS_TEMPLATE.
const judgeUserSubgoalsTemplate = "Goal:\n{goal}\n\n" +
	"Additional criteria the user added mid-loop (all must also be " +
	"satisfied for the goal to be DONE):\n{subgoals_block}\n\n" +
	"Agent's most recent response:\n{response}\n\n" +
	"{background_block}" +
	"Current time: {current_time}\n\n" +
	"Decision: For each numbered criterion above, find concrete " +
	"evidence in the agent's response that the criterion is " +
	"satisfied. Do not accept generic phrases like 'all requirements " +
	"met' or 'implying it was done' — require specific evidence (a " +
	"file contents excerpt, an output line, a command result). If " +
	"ANY criterion lacks specific evidence in the response, the goal " +
	"is NOT done — return CONTINUE (or WAIT if blocked on a listed " +
	"background process).\n\n" +
	"Is the goal AND every additional criterion satisfied?"

// judgeUserContractTemplate is the Hermes JUDGE_USER_PROMPT_WITH_CONTRACT_TEMPLATE.
const judgeUserContractTemplate = "Goal:\n{goal}\n\n" +
	"Completion contract (the authoritative definition of done):\n" +
	"{contract_block}\n\n" +
	"Agent's most recent response:\n{response}\n\n" +
	"{background_block}" +
	"Current time: {current_time}\n\n" +
	"Decision rules:\n" +
	"- The goal is DONE only when the Verification criterion is satisfied AND " +
	"the response shows concrete evidence of it (a command result, file " +
	"contents excerpt, test/benchmark output) — not a claim like 'done' or " +
	"'all tests pass' without evidence.\n" +
	"- If any stated Constraint was violated, the goal is NOT done — CONTINUE.\n" +
	"- If the response shows the agent is waiting on a listed background " +
	"process to satisfy the Verification criterion (e.g. CI is the " +
	"verification and it's still running), return WAIT on that process " +
	"instead of re-poking — re-poking now would be pure busy-work.\n" +
	"- If the response explains the work is genuinely unachievable or hits " +
	"the stated Stop condition and needs user input, the goal is NOT done — " +
	"return BLOCKED with the reason describing the block.\n" +
	"- Otherwise the goal is NOT done — CONTINUE.\n\n" +
	"Is the goal satisfied per its completion contract — done, blocked, continue, or wait?"

// DraftContractSystemPrompt is the Hermes DRAFT_CONTRACT_SYSTEM_PROMPT.
const DraftContractSystemPrompt = "You turn a user's plain-language objective into a structured completion " +
	"contract for an autonomous coding agent. The contract has five fields:\n" +
	"- outcome: the single end state that must be true when done\n" +
	"- verification: the specific test / command / artifact that PROVES the " +
	"outcome (must be concrete and checkable)\n" +
	"- constraints: what must NOT change or regress\n" +
	"- boundaries: which files, dirs, tools, or systems are in scope\n" +
	"- stop_when: the condition under which the agent should stop and ask " +
	"for human input instead of pushing on\n\n" +
	"Infer sensible, specific values from the objective and any project " +
	"context implied by it. Prefer concrete verification (a named test " +
	"command, a build, a benchmark) over vague phrases. Keep each field to " +
	"one or two sentences. If a field genuinely cannot be inferred, use an " +
	"empty string for it.\n\n" +
	"Reply ONLY with a single JSON object on one line:\n" +
	`{"outcome": "...", "verification": "...", "constraints": "...", ` +
	`"boundaries": "...", "stop_when": "..."}`

// goalAlreadySeenKick is the Hermes GOAL_ALREADY_SEEN_KICK.
const goalAlreadySeenKick = "[Goal set] Continue with the goal you were just given; there is no need to re-read it."

// The judge input cuts, in characters, from Hermes judge_goal and _render_background_block.
// A cut of 0 in JudgeInput.Chars uses its const here.
const (
	judgeGoalChars     = 2000
	judgeResponseChars = 4000
	judgeContractChars = 2500
	judgeSubgoalChars  = 2000
	judgeTodoChars     = 2000
	judgeProcChars     = 120
)

// The re-paste rule of Hermes goal_kick_prompt.
const (
	repasteMinChars = 400
	repasteMinShare = 0.8
)

// ProcLine is one running background process in the judge prompt.
type ProcLine struct {
	PID     int
	Command string
	Running time.Duration
	Tail    string
}

// JudgeInput holds the values of the judge user prompt.
type JudgeInput struct {
	Goal        string
	Contract    Contract
	Subgoals    []string
	Response    string
	Processes   []ProcLine
	Delegations int
	TodoBlock   string
	Now         time.Time
	// Chars holds the cuts of the judge input. A field of 0 uses its const.
	Chars config.GoalsJudgeChars
}

// GateFailure holds the values of the continuation prompt after a failed quality gate.
type GateFailure struct {
	Command    string
	Attempt    int
	MaxRetries int
	ExitCode   int
	Output     string
}

// fill replaces each "{name}" field of tmpl with its value in one pass. A value is not
// scanned again, so a "{goal}" in a goal text stays as typed.
func fill(tmpl string, pairs ...string) string {
	args := make([]string, 0, len(pairs))
	for i := 0; i+1 < len(pairs); i += 2 {
		args = append(args, "{"+pairs[i]+"}", pairs[i+1])
	}
	return strings.NewReplacer(args...).Replace(tmpl)
}

// cutRunes is the Hermes _truncate. It counts runes, because Python counts code points. A
// longer text keeps limit runes and gets the "… [truncated]" mark.
func cutRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	n := 0
	for i := range s {
		if n == limit {
			return s[:i] + "… [truncated]"
		}
		n++
	}
	return s
}

// oneLine replaces each new line with a space, trims the text, and cuts it for the process
// block of the judge prompt.
func oneLine(s string, limit int) string {
	return cutRunes(strings.TrimSpace(strings.ReplaceAll(s, "\n", " ")), limit)
}

// processBlock is the Hermes _render_background_block. It gives "" when no process has a
// pid, so the prompt stays the same as the prompt with no process.
func processBlock(procs []ProcLine, limit int) string {
	var lines []string
	for _, p := range procs {
		if p.PID <= 0 {
			continue
		}
		line := fmt.Sprintf("- pid %d: %s (running %ds)", p.PID, oneLine(p.Command, limit), int(p.Running.Seconds()))
		if tail := oneLine(p.Tail, limit); tail != "" {
			line += " | recent output: " + tail
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return fill(judgeBackgroundTemplate, "background_lines", strings.Join(lines, "\n"))
}

// JudgeUserPrompt returns the judge user prompt. The template choice is the Hermes
// judge_goal order: the contract, then the subgoals, then the plain template. The todo
// block of Tether Agent comes after the Hermes process and delegation blocks.
func JudgeUserPrompt(in JudgeInput) string {
	cut := func(n, fallback int) int {
		if n > 0 {
			return n
		}
		return fallback
	}
	background := processBlock(in.Processes, cut(in.Chars.Process, judgeProcChars))
	if in.Delegations > 0 {
		background += fill(judgeDelegationsTemplate, "count", strconv.Itoa(in.Delegations))
	}
	if strings.TrimSpace(in.TodoBlock) != "" {
		background += cutRunes(in.TodoBlock, cut(in.Chars.Todo, judgeTodoChars)) + "\n\n"
	}
	common := []string{
		"goal", cutRunes(in.Goal, cut(in.Chars.Goal, judgeGoalChars)),
		"response", cutRunes(in.Response, cut(in.Chars.Response, judgeResponseChars)),
		"background_block", background,
		"current_time", in.Now.Format("2006-01-02 15:04:05 MST"),
	}
	if !in.Contract.Empty() {
		block := cutRunes(in.Contract.Render(in.Subgoals), cut(in.Chars.Contract, judgeContractChars))
		return fill(judgeUserContractTemplate, append(common, "contract_block", block)...)
	}
	var kept []string
	for _, s := range in.Subgoals {
		if s = strings.TrimSpace(s); s != "" {
			kept = append(kept, s)
		}
	}
	if len(kept) > 0 {
		block := cutRunes(numbered(kept), cut(in.Chars.Subgoal, judgeSubgoalChars))
		return fill(judgeUserSubgoalsTemplate, append(common, "subgoals_block", block)...)
	}
	return fill(judgeUserTemplate, common...)
}

// numbered returns the Hermes "- N. text" block, numbered from 1.
func numbered(items []string) string {
	lines := make([]string, len(items))
	for i, s := range items {
		lines[i] = fmt.Sprintf("- %d. %s", i+1, s)
	}
	return strings.Join(lines, "\n")
}

// ContinuationPrompt returns the next prompt of the goal loop. A failed gate comes first.
// Then the order is the Hermes next_continuation_prompt order: the contract, the subgoals,
// and the plain template.
func ContinuationPrompt(st State, gate *GateFailure) string {
	switch {
	case gate != nil:
		output := gate.Output
		if output == "" {
			output = "(no output)"
		}
		return fill(continuationGateTemplate,
			"goal", st.Goal,
			"attempt", strconv.Itoa(gate.Attempt),
			"max_retries", strconv.Itoa(gate.MaxRetries),
			"command", gate.Command,
			"exit_code", strconv.Itoa(gate.ExitCode),
			"output", output)
	case !st.Contract.Empty():
		return fill(continuationContractTemplate, "goal", st.Goal, "contract_block", st.Contract.Render(st.Subgoals))
	case len(st.Subgoals) > 0:
		return fill(continuationSubgoalsTemplate, "goal", st.Goal, "subgoals_block", numbered(st.Subgoals))
	}
	return fill(continuationTemplate, "goal", st.Goal)
}

// KickPrompt returns the text of the first goal turn. It is the Hermes goal_kick_prompt: a
// goal that is almost all of the last user message gets a short pointer, so the agent does
// not read the same text two times.
func KickPrompt(goal, lastUserMessage string) string {
	g := strings.Join(strings.Fields(goal), " ")
	last := strings.Join(strings.Fields(lastUserMessage), " ")
	n := utf8.RuneCountInString(g)
	if n >= repasteMinChars && strings.Contains(last, g) &&
		float64(n) >= repasteMinShare*float64(utf8.RuneCountInString(last)) {
		return goalAlreadySeenKick
	}
	return goal
}
