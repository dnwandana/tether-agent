package goals

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// The verdict kinds of the judge.
const (
	VerdictDone     = "done"
	VerdictContinue = "continue"
	VerdictBlocked  = "blocked"
	VerdictWait     = "wait"
)

// The notice kinds that ApplyCounters returns when a failure counter reaches its limit.
const (
	NoticeParseFail     = "parse_fail"
	NoticeTransportFail = "transport_fail"
)

// The Hermes limits of the judge.
const (
	// judgeMaxTokens is the output limit (goals.py:37). A reasoning model uses hidden
	// tokens before the visible JSON line, so a small limit cuts the verdict.
	judgeMaxTokens = 4096
	// judgeEffort keeps the judge call cheap.
	judgeEffort = "low"
	// maxParseFailures is the count of parse failures in a row that pauses the goal
	// (goals.py:43).
	maxParseFailures = 3
	// maxTransportFailures is the count of API errors in a row that pauses the goal
	// (goals.py:46).
	maxTransportFailures = 5
)

// jsonObjectRE finds the first JSON object in a reply that has other text around it.
var jsonObjectRE = regexp.MustCompile(`(?s)\{.*?\}`)

// Verdict is the parsed reply of the judge. A wait verdict has exactly one target:
// WaitSession, WaitPID, or WaitSeconds.
type Verdict struct {
	Kind            string
	Reason          string
	WaitPID         int
	WaitSession     string
	WaitSeconds     float64
	ParseFailed     bool
	TransportFailed bool
}

// ParseVerdict parses the reply of the judge, like the Hermes _parse_judge_response. It fails
// open to continue. A wait with no target is continue.
func ParseVerdict(text string) Verdict {
	if strings.TrimSpace(text) == "" {
		return Verdict{Kind: VerdictContinue, Reason: "judge returned empty response", ParseFailed: true}
	}
	data := extractJSONObject(text)
	if data == nil {
		return Verdict{
			Kind:        VerdictContinue,
			Reason:      fmt.Sprintf("judge reply was not JSON: %q", cutRunes(text, 200)),
			ParseFailed: true,
		}
	}

	reason := ""
	if r := data["reason"]; pyTruthy(r) {
		if s, ok := r.(string); ok {
			reason = strings.TrimSpace(s)
		} else {
			reason = strings.TrimSpace(fmt.Sprint(r))
		}
	}
	if reason == "" {
		reason = "no reason provided"
	}

	var kind string
	if s, ok := data["verdict"].(string); ok {
		kind = strings.ToLower(strings.TrimSpace(s))
	} else {
		done := false
		if s, ok := data["done"].(string); ok {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true", "yes", "1", "done":
				done = true
			}
		} else {
			done = pyTruthy(data["done"])
		}
		kind = VerdictContinue
		if done {
			kind = VerdictDone
		}
	}
	switch kind {
	case VerdictDone, VerdictBlocked, VerdictContinue, VerdictWait:
	default:
		kind = VerdictContinue
	}
	if kind != VerdictWait {
		return Verdict{Kind: kind, Reason: reason}
	}

	// The session wins over the pid, and the pid wins over the seconds. The session target
	// is the first truthy value of its keys, like the Python "or" chain.
	var sess any
	for _, k := range []string{"wait_on_session", "session_id", "wait_session"} {
		if v := data[k]; pyTruthy(v) {
			sess = v
			break
		}
	}
	if s, ok := sess.(string); ok && strings.TrimSpace(s) != "" {
		return Verdict{Kind: VerdictWait, Reason: reason, WaitSession: strings.TrimSpace(s)}
	}
	if pid, ok := firstPositiveInt(data, "wait_on_pid", "pid", "wait_pid"); ok {
		return Verdict{Kind: VerdictWait, Reason: reason, WaitPID: int(pid)}
	}
	if secs, ok := firstPositiveInt(data, "wait_for_seconds", "seconds", "wait_seconds"); ok {
		return Verdict{Kind: VerdictWait, Reason: reason, WaitSeconds: secs}
	}
	return Verdict{Kind: VerdictContinue, Reason: reason + " (wait verdict had no target — continuing)"}
}

// extractJSONObject returns the JSON object of a reply, like the Hermes _extract_json_object. It
// removes a code fence, decodes the text, and else decodes the first {...} in the text. It
// returns nil when it finds no object.
func extractJSONObject(raw string) map[string]any {
	text := strings.TrimSpace(raw)
	if strings.HasPrefix(text, "```") {
		text = strings.Trim(text, "`")
		// The first line of a fence can name the language, for example "json".
		if nl := strings.Index(text, "\n"); nl != -1 {
			text = text[nl+1:]
		}
	}
	var data any
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		m := jsonObjectRE.FindString(text)
		if m == "" {
			return nil
		}
		if err := json.Unmarshal([]byte(m), &data); err != nil {
			return nil
		}
	}
	obj, _ := data.(map[string]any)
	return obj
}

// firstPositiveInt returns the first value of keys that is an integer above 0, like the Hermes
// _first_int. A number is cut to an integer like the Python int. A string must parse as an
// integer. Other values are skipped.
func firstPositiveInt(data map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		var n float64
		switch v := data[k].(type) {
		case float64:
			n = math.Trunc(v)
		case string:
			i, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				continue
			}
			n = float64(i)
		default:
			continue
		}
		if n > 0 && n <= math.MaxInt32 {
			return n, true
		}
	}
	return 0, false
}

// pyTruthy reports whether v is true in Python, for a value that json.Unmarshal decodes.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// Judge calls the judge model. Record, when it is not nil, keeps each good call, for example
// in the hidden judge session.
type Judge struct {
	Timeout time.Duration
	Record  func(ctx context.Context, prompt string, prov provider.Provider, out provider.Result)
}

// Call sends one judge request with the low effort and the Hermes output limit. It sends no
// temperature, because provider.Request has no temperature field. A failed call is not
// recorded.
func (j *Judge) Call(ctx context.Context, prov provider.Provider, system, user string) (provider.Result, error) {
	callCtx := ctx
	if j.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, j.Timeout)
		defer cancel()
	}
	ch, err := prov.Stream(callCtx, provider.Request{
		System:    system,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: user}},
		MaxTokens: judgeMaxTokens,
		Effort:    judgeEffort,
	})
	if err != nil {
		return provider.Result{}, err
	}
	res, err := provider.Collect(ch, nil)
	if err != nil {
		return provider.Result{}, err
	}
	if j.Record != nil {
		// The record uses the context of the caller, because the timeout covers only the call.
		j.Record(ctx, user, prov, res)
	}
	return res, nil
}

// Evaluate asks the judge whether the reply of the agent completes the goal. An API error or a
// timeout fails open to continue, with TransportFailed set.
func (j *Judge) Evaluate(ctx context.Context, prov provider.Provider, in JudgeInput) Verdict {
	if strings.TrimSpace(in.Response) == "" {
		return Verdict{Kind: VerdictContinue, Reason: "empty response (nothing to evaluate)"}
	}
	res, err := j.Call(ctx, prov, JudgeSystemPrompt, JudgeUserPrompt(in))
	if err != nil {
		return Verdict{Kind: VerdictContinue, Reason: "judge error: " + err.Error(), TransportFailed: true}
	}
	return ParseVerdict(res.Text)
}

// ApplyCounters updates the two failure counters of st from v, like the Hermes
// evaluate_after_turn. Each failure kind sets the other counter to 0. It returns the notice
// kind when a counter reaches its limit, else "". It does not change the status, because the
// after-turn step pauses the goal.
func ApplyCounters(st *State, v Verdict) (pauseKind string) {
	if v.ParseFailed {
		st.ConsecutiveParseFailures++
	} else {
		st.ConsecutiveParseFailures = 0
	}
	if v.TransportFailed {
		st.ConsecutiveTransportFailures++
	} else {
		st.ConsecutiveTransportFailures = 0
	}
	switch {
	case st.ConsecutiveTransportFailures >= maxTransportFailures:
		return NoticeTransportFail
	case st.ConsecutiveParseFailures >= maxParseFailures:
		return NoticeParseFail
	}
	return ""
}
