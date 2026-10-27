package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/mcp"
	"github.com/dnwandana/tether-agent/internal/obs"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// CommandCtx is the context of one slash command. The commands act on Key only.
type CommandCtx struct {
	Deps *Deps
	// Key is the session key of the caller.
	Key string
	// Session is the active session of Key. It can be nil.
	Session *sessions.Session
	// Reply sends a text to the caller.
	Reply func(text string)
	// Confirm asks the caller a yes or no question. Nil means that the channel cannot ask.
	Confirm func(prompt string) bool
	Hooks   CommandHooks
}

// CommandHooks connect the commands to the run of the key. With a nil Stop or Steer hook,
// the command replies NoTaskReply.
type CommandHooks struct {
	// BeforeNew runs before /new replaces the session, for example to start the review of
	// the old session. Nil uses Deps.BeforeNew.
	BeforeNew func(ctx context.Context, old *sessions.Session)
	// Stop stops the run of the key.
	Stop func()
	// Steer adds guidance to the run of the key.
	Steer func(text string)
}

// Command is one slash command.
type Command struct {
	Name, Help string
	Run        func(ctx context.Context, c *CommandCtx, args string) error
	// WhileBusy lets the command run while the key has a turn or a queue. A command that
	// can change the session of the turn must not set it (Hermes). /new and /reset set it,
	// because they stop the run first.
	WhileBusy bool
}

// sessionsListMax is the number of sessions that /sessions shows.
const sessionsListMax = 20

var (
	commandsMu sync.RWMutex
	commands   = map[string]Command{}
)

func init() {
	for _, c := range []Command{
		{Name: "sessions", Help: "List the recent sessions of this chat", Run: cmdSessions, WhileBusy: true},
		{Name: "resume", Help: "Switch to another session: /resume <id>", Run: cmdResume},
		{Name: "new", Help: "Start a new session", Run: cmdNew, WhileBusy: true},
		{Name: "reset", Help: "Start a new session (the same as /new)", Run: cmdNew, WhileBusy: true},
		{Name: "rename", Help: "Rename the current session: /rename <title>", Run: cmdRename},
		{Name: "archive", Help: "Hide the current session from /sessions", Run: cmdArchive},
		{Name: "delete", Help: "Delete a session: /delete <id>", Run: cmdDelete},
		{Name: "model", Help: "Pin the model of this session: /model <provider>[:<model>] | clear", Run: cmdModel},
		{Name: "prompt", Help: "Set the base prompt of this session: /prompt set <text> | show | clear", Run: cmdPrompt},
		{Name: "fork", Help: "Copy the current session and switch to the copy: /fork [title]", Run: cmdFork},
		{Name: "compact", Help: "Replace the older messages of this session with a summary now", Run: cmdCompact},
		ReloadMCPCommand(nil),
		{Name: "usage", Help: "Show the token use and the cost of each model: /usage [today|7d|30d|session]", Run: cmdUsage, WhileBusy: true},
	} {
		Register(c)
	}
}

// Register adds cmd to the command table. A command with the same name replaces the old one.
func Register(cmd Command) {
	commandsMu.Lock()
	defer commandsMu.Unlock()
	commands[cmd.Name] = cmd
}

// Commands returns a copy of the command table.
func Commands() map[string]Command {
	commandsMu.RLock()
	defer commandsMu.RUnlock()
	out := make(map[string]Command, len(commands))
	for k, v := range commands {
		out[k] = v
	}
	return out
}

func lookupCommand(name string) (Command, bool) {
	commandsMu.RLock()
	defer commandsMu.RUnlock()
	c, ok := commands[name]
	return c, ok
}

// commandList returns the command names as "/a, /b, ...", in name order.
func commandList() string {
	all := Commands()
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, "/"+n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// IsCommand splits a slash command into its name and its arguments. It removes the bot
// name of "/cmd@bot". The name is in lower case. A text such as "/etc/hosts" is not a
// command, because a name has only letters, digits, "_", and "-".
func IsCommand(text string) (name, args string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	head, rest, _ := strings.Cut(text[1:], " ")
	if i := strings.IndexAny(head, "\n\t"); i >= 0 {
		rest = head[i+1:] + " " + rest
		head = head[:i]
	}
	head, _, _ = strings.Cut(head, "@")
	if head == "" {
		return "", "", false
	}
	for _, r := range head {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", "", false
		}
	}
	return strings.ToLower(head), strings.TrimSpace(rest), true
}

// store returns the session store of the command.
func (c *CommandCtx) store() *sessions.Store { return c.Deps.Store }

func (c *CommandCtx) reply(format string, args ...any) {
	if c.Reply != nil {
		c.Reply(fmt.Sprintf(format, args...))
	}
}

// current returns the session of the key. It creates one when the key has none.
func (c *CommandCtx) current(ctx context.Context) (*sessions.Session, error) {
	if c.Session != nil {
		return c.Session, nil
	}
	s, err := c.store().ActiveOrCreate(ctx, c.Key, sessionTemplate(c.Key, nil))
	if err != nil {
		return nil, err
	}
	c.Session = s
	return s, nil
}

// owned returns the session with the id when it belongs to the key of the caller.
func (c *CommandCtx) owned(ctx context.Context, id string) (*sessions.Session, error) {
	s, err := c.store().Get(ctx, id)
	if errors.Is(err, sessions.ErrNotFound) || err == nil && s.Key != c.Key {
		return nil, fmt.Errorf("no session %s in this chat. Send /sessions to see the sessions", id)
	}
	return s, err
}

// sessionTemplate returns the fields of a new session for key, copied from old when it
// is set. The source is the part of the key before the first ":".
func sessionTemplate(key string, old *sessions.Session) sessions.Session {
	if old != nil {
		return sessions.Session{Source: old.Source, ChatID: old.ChatID, ThreadID: old.ThreadID, UserID: old.UserID, Kind: "chat"}
	}
	src, _, _ := strings.Cut(key, ":")
	return sessions.Session{Source: src, Kind: "chat"}
}

func cmdSessions(ctx context.Context, c *CommandCtx, _ string) error {
	list, err := c.store().List(ctx, c.Key, sessionsListMax)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.reply("No sessions in this chat.")
		return nil
	}
	var b strings.Builder
	b.WriteString("Sessions (newest first):")
	for _, s := range list {
		mark := " "
		if c.Session != nil && s.ID == c.Session.ID {
			mark = "*"
		}
		title := s.Title
		if title == "" {
			title = "(no title)"
		}
		fmt.Fprintf(&b, "\n%s %s  %s  %s  %d messages", mark, s.ID, title,
			s.LastActive.Local().Format(time.DateTime), s.MessageCount)
	}
	c.reply("%s", b.String())
	return nil
}

func cmdResume(ctx context.Context, c *CommandCtx, args string) error {
	if args == "" {
		c.reply("Usage: /resume <id>. Send /sessions to see the ids.")
		return nil
	}
	s, err := c.owned(ctx, args)
	if err != nil {
		c.reply("%v", err)
		return nil
	}
	if err := c.store().SetActive(ctx, c.Key, s.ID); err != nil {
		return err
	}
	c.Session = s
	c.reply("Resumed session %s%s.", s.ID, titleSuffix(s.Title))
	return nil
}

func cmdNew(ctx context.Context, c *CommandCtx, _ string) error {
	old := c.Session
	if old != nil {
		switch {
		case c.Hooks.BeforeNew != nil:
			c.Hooks.BeforeNew(ctx, old)
		case c.Deps.BeforeNew != nil:
			c.Deps.BeforeNew(ctx, old)
		case c.Deps.Agent != nil:
			// The review of the old session runs in the background, so the reply does
			// not wait for it.
			c.Deps.Agent.ReviewBeforeNew(ctx, old)
		}
	}
	tmpl := sessionTemplate(c.Key, old)
	tmpl.Key = c.Key
	s, err := c.store().Create(ctx, tmpl)
	if err != nil {
		return err
	}
	if err := c.store().SetActive(ctx, c.Key, s.ID); err != nil {
		return err
	}
	c.Session = s
	c.reply("New session %s.", s.ID)
	return nil
}

func cmdRename(ctx context.Context, c *CommandCtx, args string) error {
	title := strings.Join(strings.Fields(args), " ")
	if title == "" {
		c.reply("Usage: /rename <title>")
		return nil
	}
	s, err := c.current(ctx)
	if err != nil {
		return err
	}
	if err := c.store().Rename(ctx, s.ID, title); err != nil {
		return err
	}
	s.Title = title
	c.reply("Renamed the session to %q.", title)
	return nil
}

func cmdArchive(ctx context.Context, c *CommandCtx, _ string) error {
	s, err := c.current(ctx)
	if err != nil {
		return err
	}
	if err := c.store().Archive(ctx, s.ID); err != nil {
		return err
	}
	s.Archived = true
	c.reply("Archived session %s. /sessions does not show it now. Send /new to start a new session.", s.ID)
	return nil
}

// confirmWord is the last argument of /delete on a channel that has no Confirm.
const confirmWord = "confirm"

func cmdDelete(ctx context.Context, c *CommandCtx, args string) error {
	fields := strings.Fields(args)
	if len(fields) == 0 || len(fields) > 2 || len(fields) == 2 && fields[1] != confirmWord {
		c.reply("Usage: /delete <id>")
		return nil
	}
	s, err := c.owned(ctx, fields[0])
	if err != nil {
		c.reply("%v", err)
		return nil
	}
	question := fmt.Sprintf("Delete session %s%s and all its messages? You cannot undo this.", s.ID, titleSuffix(s.Title))
	switch {
	case len(fields) == 2:
	case c.Confirm == nil:
		c.reply("%s\nTo delete it, send: /delete %s %s", question, s.ID, confirmWord)
		return nil
	case !c.Confirm(question):
		c.reply("Not deleted.")
		return nil
	}
	if err := c.store().Delete(ctx, s.ID); err != nil {
		return err
	}
	// The frozen prompt of a deleted session must not stay in memory.
	if ag := c.Deps.Agent; ag != nil && ag.Prompts != nil {
		ag.Prompts.Forget(s.ID)
	}
	if c.Deps.AfterDelete != nil {
		c.Deps.AfterDelete(s.ID)
	}
	if c.Session != nil && c.Session.ID == s.ID {
		c.Session = nil
	}
	c.reply("Deleted session %s.", s.ID)
	return nil
}

func cmdModel(ctx context.Context, c *CommandCtx, args string) error {
	s, err := c.current(ctx)
	if err != nil {
		return err
	}
	if args == "" {
		cur := s.ModelOverride
		if cur == "" {
			cur = "the default (primary)"
		}
		c.reply("Model: %s. The providers are: %s.", cur, strings.Join(c.providerNames(), ", "))
		return nil
	}
	if args == "clear" {
		if err := c.store().SetModel(ctx, s.ID, ""); err != nil {
			return err
		}
		s.ModelOverride = ""
		c.reply("This session uses the default model again.")
		return nil
	}
	name, model, _ := strings.Cut(args, ":")
	if _, ok := c.Deps.Providers[name]; !ok || strings.ContainsAny(args, " \t\n") {
		c.reply("unknown provider: %s. The providers are: %s.", name, strings.Join(c.providerNames(), ", "))
		return nil
	}
	if strings.Contains(args, ":") && model == "" {
		c.reply("Usage: /model <provider>[:<model>]")
		return nil
	}
	if err := c.store().SetModel(ctx, s.ID, args); err != nil {
		return err
	}
	s.ModelOverride = args
	c.reply("This session now uses %s.", args)
	return nil
}

// providerNames returns the provider names in order.
func (c *CommandCtx) providerNames() []string {
	names := make([]string, 0, len(c.Deps.Providers))
	for n := range c.Deps.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func cmdPrompt(ctx context.Context, c *CommandCtx, args string) error {
	sub, text, _ := strings.Cut(args, " ")
	text = strings.TrimSpace(text)
	s, err := c.current(ctx)
	if err != nil {
		return err
	}
	switch {
	case sub == "set" && text != "":
		if err := c.store().SetPrompt(ctx, s.ID, text); err != nil {
			return err
		}
		s.PromptOverride = text
		c.reply("The custom prompt of this session is set. It replaces the base prompt.")
	case sub == "show" && text == "":
		if s.PromptOverride == "" {
			c.reply("This session uses the base prompt.")
		} else {
			c.reply("Custom prompt:\n%s", s.PromptOverride)
		}
	case sub == "clear" && text == "":
		if err := c.store().SetPrompt(ctx, s.ID, ""); err != nil {
			return err
		}
		s.PromptOverride = ""
		c.reply("The custom prompt is removed. This session uses the base prompt.")
	default:
		c.reply("Usage: /prompt set <text> | /prompt show | /prompt clear")
	}
	return nil
}

func cmdFork(ctx context.Context, c *CommandCtx, args string) error {
	s, err := c.current(ctx)
	if err != nil {
		return err
	}
	f, err := c.store().Fork(ctx, s.ID, strings.Join(strings.Fields(args), " "))
	if err != nil {
		return err
	}
	if err := c.store().SetActive(ctx, c.Key, f.ID); err != nil {
		return err
	}
	c.Session = f
	c.reply("Forked into %s. The original session is unchanged.", f.ID)
	return nil
}

// titleSuffix returns ` ("title")`, or "" for an empty title.
func titleSuffix(title string) string {
	if title == "" {
		return ""
	}
	return fmt.Sprintf(" (%q)", title)
}

func cmdCompact(ctx context.Context, c *CommandCtx, _ string) error {
	if c.Deps.Agent == nil || c.Deps.Agent.Compactor == nil {
		c.reply("Compaction is not available.")
		return nil
	}
	s, err := c.current(ctx)
	if err != nil {
		return err
	}
	n, err := c.Deps.Agent.CompactNow(ctx, s)
	switch {
	case err != nil:
		c.reply("Compaction failed: %v", err)
	case n == 0:
		c.reply("Nothing to compact.")
	default:
		c.reply("Compacted: %d messages → 1 summary.", n)
	}
	return nil
}

// ReloadMCPCommand returns the /reload-mcp command for the MCP manager m. The app
// registers it with Register after it makes the manager. A nil m replies that no MCP
// server is configured.
func ReloadMCPCommand(m *mcp.Manager) Command {
	return Command{
		Name: "reload-mcp",
		Help: "Reload the MCP servers and their tool lists. The next request has no prompt cache",
		Run: func(ctx context.Context, c *CommandCtx, _ string) error {
			if m == nil {
				c.reply("No MCP servers are configured.")
				return nil
			}
			var reg *tools.Registry
			if c.Deps.Agent != nil {
				reg = c.Deps.Agent.Tools
			}
			if reg == nil {
				reg = tools.NewRegistry()
			}
			res, err := m.ReloadReport(ctx, reg)
			c.reply("%s", reloadMCPReply(res, err))
			return nil
		},
	}
}

// reloadMCPReply returns the reply to /reload-mcp (Hermes gateway.reload_mcp text).
func reloadMCPReply(res mcp.ReloadResult, err error) string {
	if errors.Is(err, mcp.ErrServerList) {
		return fmt.Sprintf("MCP reload failed: %v\nThe MCP tools did not change.", err)
	}
	lines := []string{"MCP servers reloaded."}
	for _, l := range []struct {
		label string
		names []string
	}{{"Reloaded", res.Reloaded}, {"Added", res.Added}, {"Removed", res.Removed}} {
		if len(l.names) > 0 {
			lines = append(lines, l.label+": "+strings.Join(l.names, ", "))
		}
	}
	if res.Servers == 0 {
		lines = append(lines, "No MCP servers are enabled.")
	} else {
		lines = append(lines, fmt.Sprintf("%d tools available from %d servers.", res.Tools, res.Servers))
	}
	if len(res.Skipped) > 0 {
		lines = append(lines, "Not added, because a different tool has the same name: "+strings.Join(res.Skipped, ", "))
	}
	if err != nil {
		lines = append(lines, fmt.Sprintf("Some servers did not reload. They keep their last tool lists: %v", err))
	}
	lines = append(lines, "The changes apply from the next message. That request has no prompt cache.")
	return strings.Join(lines, "\n")
}

// usageHelp is the reply to /usage with a range that is not known.
const usageHelp = "Usage: /usage [today|7d|30d|session]"

func cmdUsage(ctx context.Context, c *CommandCtx, args string) error {
	if c.Deps.Agent == nil || c.Deps.Agent.DB == nil {
		c.reply("The usage data is not available.")
		return nil
	}
	scope := strings.ToLower(strings.TrimSpace(args))
	if scope == "" {
		scope = "today"
	}
	var since time.Time
	var sessionID string
	if scope == "session" {
		s, err := c.current(ctx)
		if err != nil {
			return err
		}
		sessionID = s.ID
	} else {
		var ok bool
		if since, ok = obs.UsageSince(scope, time.Now()); !ok {
			c.reply(usageHelp)
			return nil
		}
	}
	rows, compactions, err := obs.UsageReport(ctx, c.Deps.Agent.DB, since, sessionID, FlatProviders(c.Deps.Cfg))
	if err != nil {
		return err
	}
	table := obs.FormatUsage(rows)
	// Telegram shows the table in a fixed-width font only in a code block.
	if strings.HasPrefix(c.Key, "telegram:") {
		table = "```\n" + table + "\n```"
	}
	head := "Usage " + scope + ":"
	if sessionID != "" {
		head = fmt.Sprintf("Usage of session %s:", sessionID)
	}
	text := head + "\n" + table
	if sessionID != "" {
		text += fmt.Sprintf("\nCompactions: %d", compactions)
	}
	c.reply("%s", text)
	return nil
}

// FlatProviders returns the names of the providers with a flat-rate plan.
func FlatProviders(cfg *config.Config) map[string]bool {
	flat := map[string]bool{}
	if cfg == nil {
		return flat
	}
	for name, p := range cfg.Providers {
		if p.FlatRate {
			flat[name] = true
		}
	}
	return flat
}
