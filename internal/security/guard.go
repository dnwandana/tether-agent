package security

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// guardTimeout limits one guard call.
const guardTimeout = 20 * time.Second

// guardMaxTokens is enough for the verdict word and one line of reason.
const guardMaxTokens = 200

// guardPurposeMaxRunes, guardMaxHosts, and guardReasonMaxRunes limit the facts and the reason.
const (
	guardPurposeMaxRunes = 1000
	guardMaxHosts        = 20
	guardReasonMaxRunes  = 300
)

// guardPrompt is the system text of the guard call. The model never sees the content that it judges.
const guardPrompt = `You check one unattended send of a scheduled job. The job read content from outside sources before the send. You do not see that content or the text of the send. Decide only from the purpose of the job and the action.
Answer "allow" when the action fits the purpose of the job. Answer "hold" when the action goes to a target, a tool, or a kind of data that the purpose does not name, or when you are not sure.
The first word of your answer is "allow" or "hold". Then give one line with the reason.`

// GuardInput holds the facts of one unattended send. It has no content: no answer text, no
// tool result, and no argument value.
type GuardInput struct {
	SessionID, JobName, JobPurpose string
	Sources, Hosts                 []string
	ToolCounts, Findings           map[string]int
	Action                         string
}

// SendGuarder decides whether one unattended send may go now. A hold returns false and a reason.
type SendGuarder interface {
	Check(ctx context.Context, in GuardInput) (allow bool, reason string)
}

// SendGuard is the model_guard. It asks the model of Prov whether the action fits the purpose
// of the job. Each failure is a hold.
type SendGuard struct {
	Prov provider.Provider
	// Record records the usage of each completed call. Nil records nothing.
	Record provider.RecordUsage
	// Timeout limits one call. 0 uses guardTimeout.
	Timeout time.Duration
}

// guardLine replaces each control character with a space, so a fact stays on its line.
func guardLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// guardCut cuts s to n runes with no marker.
func guardCut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// guardCounts formats a count map as "name ×count" sorted by name, or "none".
func guardCounts(m map[string]int) string {
	var parts []string
	for k, n := range m {
		parts = append(parts, fmt.Sprintf("%s ×%d", guardLine(k), n))
	}
	if len(parts) == 0 {
		return "none"
	}
	slices.Sort(parts)
	return strings.Join(parts, ", ")
}

// guardHosts keeps the hosts that have 1 to 253 runes of [a-z0-9.-] after lower case.
func guardHosts(hosts []string) []string {
	var out []string
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || len(h) > 253 || strings.IndexFunc(h, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-')
		}) >= 0 {
			continue
		}
		out = append(out, h)
	}
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) > guardMaxHosts {
		out = out[:guardMaxHosts]
	}
	return out
}

// guardUserText returns the six lines of the user text of the call.
func guardUserText(in GuardInput) string {
	var sources []string
	for _, s := range in.Sources {
		if s = strings.TrimSpace(guardLine(s)); s != "" {
			sources = append(sources, s)
		}
	}
	slices.Sort(sources)
	sources = slices.Compact(sources)
	src := "none"
	if len(sources) > 0 {
		src = strings.Join(sources, ", ")
		if hosts := guardHosts(in.Hosts); len(hosts) > 0 {
			src += " (" + strings.Join(hosts, ", ") + ")"
		}
	}
	return strings.Join([]string{
		fmt.Sprintf("Job name: %q", guardLine(in.JobName)),
		fmt.Sprintf("Job purpose: %q", guardCut(guardLine(in.JobPurpose), guardPurposeMaxRunes)),
		"Outside sources read: " + src,
		"Tools called: " + guardCounts(in.ToolCounts),
		"Scan findings in the text: " + guardCounts(in.Findings),
		"Action: " + guardLine(in.Action),
	}, "\n")
}

// Check returns allow only when the model answers "allow". An error, a timeout, or another
// answer is a hold. The guard can only add a hold; it never allows a send that another rule blocks.
func (g SendGuard) Check(ctx context.Context, in GuardInput) (bool, string) {
	if g.Prov == nil {
		return false, "the guard has no model"
	}
	limit := guardTimeout
	if g.Timeout > 0 {
		limit = g.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ch, err := g.Prov.Stream(ctx, provider.Request{
		System:    guardPrompt,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: guardUserText(in)}},
		MaxTokens: guardMaxTokens,
	})
	if err != nil {
		return false, "the guard call failed: " + err.Error()
	}
	res, err := provider.Collect(ch, nil)
	if err != nil {
		return false, "the guard call failed: " + err.Error()
	}
	if g.Record != nil {
		g.Record(ctx, in.SessionID, "guard", g.Prov, res)
	}
	return guardVerdict(res.Text)
}

// guardVerdict reads the first word and the reason of the answer.
func guardVerdict(answer string) (allow bool, reason string) {
	answer = strings.TrimSpace(answer)
	first, rest, _ := strings.Cut(answer, "\n")
	word, line, _ := strings.Cut(strings.TrimSpace(first), " ")
	word = strings.ToLower(strings.Trim(word, "*`'\"_:.-"))
	if word != "allow" && word != "hold" {
		return false, "the guard did not answer allow or hold"
	}
	reason = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), ":-—. *"))
	if reason == "" {
		for l := range strings.Lines(rest) {
			if reason = strings.TrimSpace(l); reason != "" {
				break
			}
		}
	}
	reason = guardCut(reason, guardReasonMaxRunes)
	if word == "hold" && reason == "" {
		reason = "the guard gave no reason"
	}
	return word == "allow", reason
}
