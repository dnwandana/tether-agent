package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// The withheld reasons of the terminal tool.
const (
	reasonNoRun   = "this run cannot use bound secrets"
	reasonNoValue = "%s is bound but has no value. Ask the owner with request_secret"
	reasonNotYet  = "the program %s is not allowed yet. Run the command once in a Telegram chat turn, so that the owner can allow it"
	reasonDenied  = "the owner did not allow the program %s"
	// The texts of a handle that a command does not get.
	reasonOutside  = "this run read outside content"
	reasonHandleNo = "the owner did not allow it"
	unknownHandle  = "[%s is unknown. Handles end when the session ends or the gateway restarts.]"
	authTailBytes  = 4096
)

// secretPlan is the bound-secret decision of one terminal call before the gate.
type secretPlan struct {
	parsed   security.Parsed
	give     []security.Binding
	withheld []security.Withheld
	// handles holds the handle values that the command names, by handle name.
	handles map[string]string
	// unknown holds the handle names that the root did not make.
	unknown []string
	// noRun is true for a member turn or a script call.
	noRun bool
}

// planSecrets reads command and matches it against Bound. A member turn and a script call
// get no secret. A name with no value in the store is withheld.
func (t *TerminalTool) planSecrets(env *Env, command string) secretPlan {
	if len(t.Bound) == 0 && t.Handles == nil {
		return secretPlan{}
	}
	if t.Store != nil {
		t.Store.Refresh()
	}
	p := security.ReadCommand(command)
	give, withheld := security.BindSecrets(p, t.Bound)
	plan := secretPlan{parsed: p, withheld: withheld}
	var hnames []string
	if t.Handles != nil {
		root := ""
		if env != nil {
			root = env.RunRootSession()
		}
		var hvals []string
		hvals, hnames, plan.unknown = t.Handles.Env(root, p)
		if len(hvals) > 0 {
			plan.handles = map[string]string{}
			for i, kv := range hvals {
				plan.handles[hnames[i]] = strings.TrimPrefix(kv, hnames[i]+"=")
			}
		}
	}
	if env != nil && (env.Member || env.ScriptCall) {
		plan.noRun = true
		plan.withheld = nil
		for _, b := range give {
			plan.withheld = append(plan.withheld, security.Withheld{Name: b.Name, Reason: reasonNoRun})
		}
		for _, w := range withheld {
			plan.withheld = append(plan.withheld, security.Withheld{Name: w.Name, Reason: reasonNoRun})
		}
		for _, n := range hnames {
			plan.withheld = append(plan.withheld, security.Withheld{Name: n, Reason: reasonNoRun})
		}
		plan.handles = nil
		return plan
	}
	for _, b := range give {
		if v, ok := t.storeValue(b.Name); ok && v != "" {
			plan.give = append(plan.give, b)
			continue
		}
		plan.withheld = append(plan.withheld, security.Withheld{Name: b.Name, Reason: fmt.Sprintf(reasonNoValue, b.Name)})
	}
	return plan
}

// storeValue returns the .env value of name from the store. A nil store has no value.
func (t *TerminalTool) storeValue(name string) (string, bool) {
	if t.Store == nil {
		return "", false
	}
	return t.Store.Get(name)
}

// names returns the sorted names of p.give.
func (p secretPlan) names() []string {
	var out []string
	for _, b := range p.give {
		out = append(out, b.Name)
	}
	sort.Strings(out)
	return out
}

// giveSecrets checks the pin of each binding of plan after the gate allowed the command. It
// returns the values to give, the given names, and one line for each withheld name, sorted by name.
func (t *TerminalTool) giveSecrets(ctx context.Context, env *Env, plan secretPlan, command, dir, path string) (bound map[string]string, given []string, lines []string) {
	withheld := append([]security.Withheld(nil), plan.withheld...)
	for _, b := range plan.give {
		reason := t.pinReason(ctx, env, plan, b, command, dir, path)
		if reason == "" {
			// The value comes from the store only, never from the process environment.
			if v, ok := t.storeValue(b.Name); ok && v != "" {
				if bound == nil {
					bound = map[string]string{}
				}
				bound[b.Name] = v
				given = append(given, b.Name)
				continue
			}
			reason = fmt.Sprintf(reasonNoValue, b.Name)
		}
		withheld = append(withheld, security.Withheld{Name: b.Name, Reason: reason})
	}
	sort.Strings(given)
	hv, hlines := t.giveHandles(ctx, env, plan, command)
	if len(hv) > 0 && bound == nil {
		bound = map[string]string{}
	}
	maps.Copy(bound, hv)
	sort.SliceStable(withheld, func(i, j int) bool { return withheld[i].Name < withheld[j].Name })
	for _, w := range withheld {
		lines = append(lines, withheldLine(w))
	}
	return bound, given, append(lines, hlines...)
}

// giveHandles returns the values of the handles that plan names, and the lines for each handle
// that the command does not get. After outside content in the turn, it asks the owner one time.
func (t *TerminalTool) giveHandles(ctx context.Context, env *Env, plan secretPlan, command string) (values map[string]string, lines []string) {
	for _, n := range plan.unknown {
		lines = append(lines, fmt.Sprintf(unknownHandle, n))
	}
	if len(plan.handles) == 0 {
		return nil, lines
	}
	names := make([]string, 0, len(plan.handles))
	for n := range plan.handles {
		names = append(names, n)
	}
	sort.Strings(names)
	refuse := func(reason string) (map[string]string, []string) {
		for _, n := range names {
			lines = append(lines, withheldLine(security.Withheld{Name: n, Reason: reason}))
		}
		return nil, lines
	}
	if sources := Outside(env).Sources; len(sources) > 0 {
		var pinner SecretPinner
		if env != nil && env.Gate != nil {
			pinner, _ = env.Gate.(SecretPinner)
		}
		if pinner == nil {
			return refuse(reasonOutside)
		}
		allow, err := pinner.AskPin(ctx, env, PinRequest{Command: command, Handles: names, Sources: sources})
		if errors.Is(err, security.ErrNoPrompter) {
			return refuse(reasonOutside)
		}
		if err != nil || !allow {
			return refuse(reasonHandleNo)
		}
	}
	return plan.handles, lines
}

// pinReason checks the pin rows of one binding. It returns "" when the secret may go to the
// command, else the reason to withhold it.
func (t *TerminalTool) pinReason(ctx context.Context, env *Env, plan secretPlan, b security.Binding, command, dir, path string) string {
	targets, err := security.ResolvePinTargets(plan.parsed, b.Parts, dir, path)
	if err != nil {
		return err.Error()
	}
	var missing []security.PinTarget
	for _, target := range targets {
		ok := false
		if t.Pins != nil {
			// A failed query counts as no row.
			if allowed, err := t.Pins.Allowed(ctx, b.Name, target); err == nil {
				ok = allowed
			}
		}
		if !ok {
			missing = append(missing, target)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	first := missing[0]
	var pinner SecretPinner
	if env != nil && env.Gate != nil {
		pinner, _ = env.Gate.(SecretPinner)
	}
	if t.Pins == nil || pinner == nil {
		return fmt.Sprintf(reasonNotYet, first.Path)
	}
	allow, err := pinner.AskPin(ctx, env, PinRequest{Name: b.Name, Path: first.Path, SHA256: first.SHA256, Command: command})
	if errors.Is(err, security.ErrNoPrompter) {
		return fmt.Sprintf(reasonNotYet, first.Path)
	}
	if err != nil || !allow {
		return fmt.Sprintf(reasonDenied, first.Path)
	}
	for _, target := range missing {
		if err := t.Pins.Allow(ctx, b.Name, target); err != nil {
			logger := slog.Default()
			if env != nil && env.Log != nil {
				logger = env.Log
			}
			logger.Warn("terminal: cannot save the pin of a bound secret", "name", b.Name, "path", target.Path, "err", err)
		}
	}
	return ""
}

// markNote is the note for a bound secret after an auth failure.
const markNote = "[%s is marked for a check: the command got an auth failure. " +
	"Do not run it again with this value. Ask the owner for a new value with request_secret.]"

// afterRun clears the marks of the given names after a success, and marks them after an auth
// failure. It returns the note for the result, or "".
func (t *TerminalTool) afterRun(ctx context.Context, given []string, exitCode int, output string) string {
	if t.Marks == nil || len(given) == 0 {
		return ""
	}
	if exitCode == 0 {
		for _, name := range given {
			if err := t.Marks.Clear(ctx, name); err != nil {
				slog.Warn("terminal: cannot clear the mark of a bound secret", "name", name, "err", err)
			}
		}
		return ""
	}
	if security.ClassifyFailure(exitCode, output) != security.FailureInvalid {
		return ""
	}
	now := time.Now()
	lines := make([]string, 0, len(given))
	for _, name := range given {
		if err := t.Marks.Mark(ctx, name, now); err != nil {
			slog.Warn("terminal: cannot mark a bound secret", "name", name, "err", err)
		}
		lines = append(lines, fmt.Sprintf(markNote, name))
	}
	return strings.Join(lines, "\n")
}

// withheldLine returns "[NAME was not given: reason]".
func withheldLine(w security.Withheld) string {
	return "[" + w.Name + " was not given: " + w.Reason + "]"
}

// withLines adds each line to text on its own line.
func withLines(text string, lines []string) string {
	if len(lines) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	for _, l := range lines {
		appendNote(&b, l)
	}
	return b.String()
}

// tailFile returns the last n bytes of the file at path, or "".
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(info.Size()-int64(n), 0)
	buf := make([]byte, info.Size()-off)
	m, _ := f.ReadAt(buf, off)
	return string(buf[:m])
}
