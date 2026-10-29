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
	"github.com/dnwandana/tether-agent/internal/provider"
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
	// Send sends a Reply with buttons to the caller. It is nil on a text-only channel.
	Send func(r Reply)
	// Confirm asks the caller a yes or no question. Nil means that the channel cannot ask.
	Confirm func(prompt string) bool
	Hooks   CommandHooks
}

// Reply is one command reply with optional buttons.
type Reply struct {
	// Text is common Markdown. It is the only part that a text-only channel shows.
	Text string
	// Buttons holds rows of buttons. Button.Text is the label, and Button.Data is the action.
	Buttons [][]Button
	// Rich is optional rich-message Markdown. Text is the fallback.
	Rich string
	// Edit replaces the message of the pressed button, when there is one.
	Edit bool
}

// CommandHooks connect the commands to the run of the key. With a nil Stop, StopKeep, or
// Steer hook, the command replies NoTaskReply.
type CommandHooks struct {
	// BeforeNew runs before /new replaces the session, for example to start the review of
	// the old session. Nil uses Deps.BeforeNew.
	BeforeNew func(ctx context.Context, old *sessions.Session)
	// Stop stops the run of the key.
	Stop func()
	// StopKeep stops the running task of the key and keeps its queue. It returns false when
	// no task runs, and the number of messages that wait.
	StopKeep func() (stopped bool, waiting int)
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
		{Name: "delete", Help: "Delete a session", Run: cmdDelete},
		{Name: "model", Help: "Pin the model of this session: /model <provider>[:<model>] | clear", Run: cmdModel},
		{Name: "prompt", Help: "Set the base prompt of this session: /prompt set <text> | show | clear", Run: cmdPrompt},
		{Name: "fork", Help: "Copy the current session and switch to the copy: /fork [title]", Run: cmdFork},
		{Name: "compact", Help: "Replace the older messages of this session with a summary now", Run: cmdCompact},
		ReloadMCPCommand(nil),
		{Name: "usage", Help: "Show the token use and the cost of each model: /usage [today|7d|30d|session]", Run: cmdUsage, WhileBusy: true},
		{Name: "help", Help: "Show all commands", Run: cmdHelp, WhileBusy: true},
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

// interactive reports whether the channel of the caller shows buttons.
func (c *CommandCtx) interactive() bool { return c.Send != nil }

// send sends r with its buttons. A text-only channel gets r.Text only.
func (c *CommandCtx) send(r Reply) {
	switch {
	case c.Send != nil:
		c.Send(r)
	case c.Reply != nil:
		c.Reply(r.Text)
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

// owned returns the session with the id when it belongs to the key of the caller. For a
// session that does not exist or that belongs to another key, the error is a
// notFoundError, and its text is the reply to send.
func (c *CommandCtx) owned(ctx context.Context, id string) (*sessions.Session, error) {
	s, err := c.store().Get(ctx, id)
	if errors.Is(err, sessions.ErrNotFound) || err == nil && s.Key != c.Key {
		return nil, notFoundError{id: id}
	}
	return s, err
}

// notFoundError tells that this chat has no session with the id.
type notFoundError struct{ id string }

func (e notFoundError) Error() string { return notFoundText(e.id) }

// notFoundText returns the reply for a session ID that this chat does not have. The reply
// shows the ID, because the owner typed it.
func notFoundText(id string) string {
	return fmt.Sprintf("⚠️ **Session not found**\nThis chat has no session `%s`. Send /sessions to see your sessions.", id)
}

// ownedOrReply returns the session with the id, like owned. When this chat has no such
// session, it sends the not-found reply and returns nil and no error.
func (c *CommandCtx) ownedOrReply(ctx context.Context, id string) (*sessions.Session, error) {
	s, err := c.owned(ctx, id)
	var nf notFoundError
	if errors.As(err, &nf) {
		c.send(Reply{Text: nf.Error()})
		return nil, nil
	}
	return s, err
}

// messageCount returns the number of messages of the session with the id.
func (c *CommandCtx) messageCount(ctx context.Context, id string) (int, error) {
	return c.store().CountMessages(ctx, id)
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

func cmdSessions(ctx context.Context, c *CommandCtx, args string) error {
	a := parseListArgs("sessions", args)
	mode := listSessions
	if a.Archived {
		mode = listArchived
	}
	r, err := c.listReply(ctx, mode, a, nowFunc())
	if err != nil {
		return err
	}
	c.send(r)
	return nil
}

func cmdResume(ctx context.Context, c *CommandCtx, args string) error {
	a := parseListArgs("resume", args)
	if a.ID == "" {
		r, err := c.listReply(ctx, listResume, a, nowFunc())
		if err != nil {
			return err
		}
		c.send(r)
		return nil
	}
	s, err := c.ownedOrReply(ctx, a.ID)
	if s == nil || err != nil {
		return err
	}
	// The resume of an archived session takes it out of the archive. The Undo button of
	// /archive uses this.
	if s.Archived {
		if err := c.store().Unarchive(ctx, s.ID); err != nil {
			return err
		}
		s.Archived = false
	}
	n, err := c.messageCount(ctx, s.ID)
	if err != nil {
		return err
	}
	if err := c.store().SetActive(ctx, c.Key, s.ID); err != nil {
		return err
	}
	c.Session = s
	c.send(backReply(s, n, nowFunc()))
	return nil
}

// backReply returns the reply after a switch to the session s with msgs messages.
func backReply(s *sessions.Session, msgs int, now time.Time) Reply {
	text := fmt.Sprintf("↩️ **Back to \"%s\"**\n", sessionTitle(s.Title))
	last := formatDay(s.LastActive, now) + " at " + s.LastActive.In(time.Local).Format("15:04")
	switch msgs {
	case 0:
		text += "No messages yet."
	case 1:
		text += "1 message. Last message " + last + "."
	default:
		text += fmt.Sprintf("%d messages. Last message %s.", msgs, last)
	}
	return Reply{Text: text}
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
	c.send(Reply{Text: newReply(c.Deps)})
	return nil
}

// newReply returns the reply to /new and /reset. With no agent, the reply has no model
// and no context lines. When the primary failed, the reply names the fallback model and
// adds the warning.
func newReply(d *Deps) string {
	text := "✨ Starting new session.\nSend a message to begin.\n\n"
	if d.Agent != nil {
		ms := d.Agent.CurrentModel()
		mark := ""
		if ms.Fallback.Active {
			mark = " (fallback)"
		}
		text += fmt.Sprintf("Model: %s%s\nContext: %s\n", EscapeMarkdown(ms.Model), mark, formatContext(ms.ContextWindow))
		if w := fallbackWarning(ms.Fallback, nowFunc()); w != "" {
			text += w + "\n"
		}
		text += "\n"
	}
	return text + "💡 Tip: " + pickTip()
}

// fallbackWarning returns the warning line for a primary that failed, or "" when the
// primary answers.
func fallbackWarning(st provider.FallbackStatus, now time.Time) string {
	if !st.Active {
		return ""
	}
	return fmt.Sprintf("⚠️ The primary model %s failed %s (%s). Calls go to the fallback until the primary answers again.",
		EscapeMarkdown(st.PrimaryModel), obs.Ago(now.Sub(st.FailedAt)), EscapeMarkdown(st.Cause))
}

func cmdRename(ctx context.Context, c *CommandCtx, args string) error {
	title := strings.Join(strings.Fields(args), " ")
	if title == "" {
		// Only show the title. A command that shows help must not create a session.
		shown := "none"
		if c.Session != nil && c.Session.Title != "" {
			shown = EscapeMarkdown(c.Session.Title)
		}
		c.send(Reply{Text: "✏️ **Title: " + shown + "**\nTo change it, send `/rename <new title>`."})
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
	c.send(Reply{Text: fmt.Sprintf("✏️ **Renamed to \"%s\"**", sessionTitle(title))})
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
	text := fmt.Sprintf("🗄 **Archived \"%s\"**\nThe session list does not show it now.", sessionTitle(s.Title))
	if !c.interactive() {
		text += fmt.Sprintf("\nSend /resume %s to undo.", s.ID)
	}
	// Undo runs /resume, and /resume takes the session out of the archive.
	c.send(Reply{Text: text, Buttons: [][]Button{{
		{Text: "↩️ Undo", Data: "/resume " + s.ID},
		{Text: "🆕 New session", Data: "/new"},
	}}})
	return nil
}

// confirmWord is the last argument of /delete that deletes with no question.
const confirmWord = "confirm"

func cmdDelete(ctx context.Context, c *CommandCtx, args string) error {
	fields := strings.Fields(args)
	confirmed := len(fields) == 2 && fields[1] == confirmWord && isSessionID(fields[0])
	id := ""
	if confirmed {
		id = fields[0]
	} else {
		a := parseListArgs("delete", args)
		if a.ID == "" {
			r, err := c.listReply(ctx, listDelete, a, nowFunc())
			if err != nil {
				return err
			}
			c.send(r)
			return nil
		}
		id = a.ID
	}
	s, err := c.ownedOrReply(ctx, id)
	if s == nil || err != nil {
		return err
	}
	if !confirmed {
		n, err := c.messageCount(ctx, s.ID)
		if err != nil {
			return err
		}
		messages := fmt.Sprintf("This deletes %d messages. You cannot undo this.", n)
		if n == 1 {
			messages = "This deletes 1 message. You cannot undo this."
		}
		question := fmt.Sprintf("🗑 **Delete \"%s\"?**\n%s", sessionTitle(s.Title), messages)
		switch {
		case c.interactive():
			c.send(Reply{Text: question, Buttons: [][]Button{{
				{Text: "🗑 Delete", Data: "/delete " + s.ID + " " + confirmWord},
				{Text: "Cancel", Data: "cancel"},
			}}})
			return nil
		case c.Confirm == nil:
			c.send(Reply{Text: fmt.Sprintf("%s\nTo delete it, send: /delete %s %s", question, s.ID, confirmWord)})
			return nil
		case !c.Confirm(fmt.Sprintf("Delete %q? %s", plainTitle(s.Title), messages)):
			c.send(Reply{Text: "Not deleted."})
			return nil
		}
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
	c.send(Reply{Text: fmt.Sprintf("🗑 **Deleted \"%s\"**", sessionTitle(s.Title)), Edit: true})
	return nil
}

func cmdModel(ctx context.Context, c *CommandCtx, args string) error {
	s, err := c.current(ctx)
	if err != nil {
		return err
	}
	if args == "" {
		c.send(c.modelReply(s))
		return nil
	}
	if args == "clear" {
		if err := c.store().SetModel(ctx, s.ID, ""); err != nil {
			return err
		}
		s.ModelOverride = ""
		c.send(Reply{Text: "🧠 **Model: default**\nThis session uses the default model again."})
		return nil
	}
	name, model, _ := strings.Cut(args, ":")
	if _, ok := c.Deps.Providers[name]; !ok || strings.ContainsAny(args, " \t\n") {
		c.send(Reply{Text: fmt.Sprintf("⚠️ **Unknown provider: %s**\nThe providers are: %s.",
			EscapeMarkdown(name), strings.Join(c.providerNames(), ", "))})
		return nil
	}
	if strings.Contains(args, ":") && model == "" {
		c.send(Reply{Text: "⚠️ **No model after the colon**\nSend /model <provider> or /model <provider>:<model>."})
		return nil
	}
	if err := c.store().SetModel(ctx, s.ID, args); err != nil {
		return err
	}
	s.ModelOverride = args
	c.send(Reply{
		Text:    fmt.Sprintf("🧠 **Model: %s**\nThis session uses it from the next message.", EscapeMarkdown(args)),
		Buttons: [][]Button{{{Text: "↺ Use the default", Data: "/model clear"}}},
	})
	return nil
}

// modelButtonsPerRow is the number of provider buttons in one row of the /model reply.
const modelButtonsPerRow = 3

// modelReply returns the reply to /model with no argument. It names the provider and the
// model that the session s uses now. On a channel with buttons, it has one button for each
// provider. A text-only channel gets the command to type.
func (c *CommandCtx) modelReply(s *sessions.Session) Reply {
	head := "default"
	if s.ModelOverride != "" {
		head = EscapeMarkdown(s.ModelOverride)
	}
	name, model := c.modelInUse(s)
	// The buttons mark the entry that the session chose. The text names the entry that
	// answers now, which is the fallback when the primary failed.
	shown, warning := name, ""
	if name == "primary" {
		if st, _ := provider.StatusOf(c.Deps.Providers["primary"]); st.Active {
			shown, model, warning = st.FallbackName, st.FallbackModel, fallbackWarning(st, nowFunc())
		}
	}
	text := fmt.Sprintf("🧠 **Model: %s**\n`%s`", head, shown)
	if model != "" {
		text += " · " + EscapeMarkdown(model)
	}
	if warning != "" {
		text += "\n" + warning
	}
	names := c.providerNames()
	if !c.interactive() {
		text += fmt.Sprintf("\nSend /model <provider> to change it. The providers are: %s.", strings.Join(names, ", "))
		return Reply{Text: text}
	}
	var rows [][]Button
	for i, n := range names {
		if i%modelButtonsPerRow == 0 {
			rows = append(rows, nil)
		}
		label := n
		if n == name {
			label = "✓ " + n
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], Button{Text: label, Data: "/model " + n})
	}
	return Reply{Text: text + "\nSelect a provider for this session:", Buttons: rows}
}

// modelInUse returns the provider name and the model that the session s uses now: the
// model override when it names a known provider, else the default provider "primary".
func (c *CommandCtx) modelInUse(s *sessions.Session) (name, model string) {
	name = "primary"
	if s.ModelOverride != "" {
		n, m, _ := strings.Cut(s.ModelOverride, ":")
		if _, ok := c.Deps.Providers[n]; ok {
			name, model = n, m
		}
	}
	if model == "" {
		if p := c.Deps.Providers[name]; p != nil {
			model = p.Model()
		}
	}
	if model == "" && c.Deps.Cfg != nil {
		model = c.Deps.Cfg.Providers[name].Model
	}
	return name, model
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

// useBasePrompt is the button that removes the custom prompt.
var useBasePrompt = [][]Button{{{Text: "🧹 Use the base prompt", Data: "/prompt clear"}}}

// promptHelp is the reply to /prompt with an argument that is not known.
const promptHelp = "📝 **Prompt commands**\n`/prompt` shows the prompt.\n" +
	"`/prompt set <text>` replaces the base prompt in this session.\n`/prompt clear` goes back to the base prompt."

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
		c.send(Reply{Text: "📝 **Custom prompt set**\nIt replaces the base prompt in this session.", Buttons: useBasePrompt})
	case (sub == "" || sub == "show") && text == "":
		if s.PromptOverride == "" {
			c.send(Reply{Text: "📝 **Prompt: base prompt**\nTo replace it in this session, send `/prompt set <text>`."})
		} else {
			c.send(Reply{Text: "📝 **Prompt: custom**\n" + quoteMarkdown(s.PromptOverride), Buttons: useBasePrompt})
		}
	case sub == "clear" && text == "":
		if err := c.store().SetPrompt(ctx, s.ID, ""); err != nil {
			return err
		}
		s.PromptOverride = ""
		c.send(Reply{Text: "📝 **Base prompt again**\nThe custom prompt is removed."})
	default:
		c.send(Reply{Text: promptHelp})
	}
	return nil
}

// quoteMarkdown returns text as a Markdown quote. Each line gets the "> " prefix, and the
// Markdown markers in the text show literally.
func quoteMarkdown(text string) string {
	lines := strings.Split(EscapeMarkdown(text), "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
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
	text := fmt.Sprintf("🍴 **Copied to \"%s\"**\nYou are in the copy now. \"%s\" did not change.",
		sessionTitle(f.Title), sessionTitle(s.Title))
	if !c.interactive() {
		text += fmt.Sprintf("\nSend /resume %s to go back.", s.ID)
	}
	c.send(Reply{Text: text, Buttons: [][]Button{{
		{Text: "↩️ Back to \"" + buttonTitle(s.Title) + "\"", Data: "/resume " + s.ID},
	}}})
	return nil
}

func cmdCompact(ctx context.Context, c *CommandCtx, _ string) error {
	if c.Deps.Agent == nil || c.Deps.Agent.Compactor == nil {
		c.send(Reply{Text: "🗜 **Compaction is not available**"})
		return nil
	}
	s, err := c.current(ctx)
	if err != nil {
		return err
	}
	n, err := c.Deps.Agent.CompactNow(ctx, s)
	c.send(compactReply(n, err))
	return nil
}

// compactReply returns the reply to /compact after n messages became one summary, or
// after the error err.
func compactReply(n int, err error) Reply {
	switch {
	case err != nil:
		return Reply{Text: fmt.Sprintf("⚠️ **Compaction failed**\n%s. Try again later.",
			EscapeMarkdown(strings.TrimSuffix(err.Error(), ".")))}
	case n == 0:
		return Reply{Text: "🗜 **Nothing to compact**\nThis session is still short."}
	default:
		return Reply{Text: fmt.Sprintf("🗜 **Compacted**\n%d messages → 1 summary. The session keeps the context.", n)}
	}
}

// helpTelegram is the reply to /help on a channel with buttons. Telegram shows the
// command /reload-mcp as /reload_mcp in its menu.
const helpTelegram = "📖 **Commands**\n\n" +
	"**Sessions**\n/new · start a new session\n/sessions · see and resume sessions\n" +
	"/rename · change the title\n/fork · copy this session\n/archive · hide this session\n" +
	"/delete · delete a session\n\n" +
	"**This session**\n/model · change the model\n/prompt · change the prompt\n" +
	"/compact · make the context shorter\n/usage · see tokens and cost\n\n" +
	"**The current task**\n/stop · stop it\n/steer · send it a note\n" +
	"/approve · /deny · answer an approval\n\n" +
	"**Setup**\n/sethome · send cron results to this chat\n/reload_mcp · reload the MCP servers"

// helpCLI is the reply to /help on a text-only channel. It does not have the Telegram
// commands, and it has /exit.
const helpCLI = "📖 **Commands**\n\n" +
	"**Sessions**\n/new · start a new session\n/sessions · see and resume sessions\n" +
	"/rename · change the title\n/fork · copy this session\n/archive · hide this session\n" +
	"/delete · delete a session\n\n" +
	"**This session**\n/model · change the model\n/prompt · change the prompt\n" +
	"/compact · make the context shorter\n/usage · see tokens and cost\n\n" +
	"**The current task**\n/stop · stop it\n/steer · send it a note\n\n" +
	"**Setup**\n/reload-mcp · reload the MCP servers\n/exit · leave the chat"

func cmdHelp(_ context.Context, c *CommandCtx, _ string) error {
	text := helpCLI
	if c.interactive() {
		text = helpTelegram
	}
	c.send(Reply{Text: text})
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
				c.send(Reply{Text: "🔌 **No MCP servers**\nAdd a server under `mcp.servers` in config.yaml."})
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
			c.send(Reply{Text: reloadMCPReply(res, err)})
			return nil
		},
	}
}

// reloadMCPReply returns the reply to /reload-mcp. The parts of the report follow Hermes
// gateway.reload_mcp.
func reloadMCPReply(res mcp.ReloadResult, err error) string {
	if errors.Is(err, mcp.ErrServerList) {
		return fmt.Sprintf("⚠️ **MCP reload failed**\n%s\nThe MCP tools did not change.", EscapeMarkdown(err.Error()))
	}
	lines := []string{"🔌 **MCP servers reloaded**"}
	for _, l := range []struct {
		label string
		names []string
	}{{"Reloaded", res.Reloaded}, {"Added", res.Added}, {"Removed", res.Removed}} {
		if len(l.names) > 0 {
			lines = append(lines, "• "+l.label+": "+escapeNames(l.names))
		}
	}
	if res.Servers == 0 {
		lines = append(lines, "No MCP servers are enabled.")
	} else {
		lines = append(lines, fmt.Sprintf("%s from %s.", plural(res.Tools, "tool"), plural(res.Servers, "server")))
	}
	if len(res.Skipped) > 0 {
		lines = append(lines, "• Not added, because another tool has the same name: "+escapeNames(res.Skipped))
	}
	if err != nil {
		lines = append(lines, "⚠️ Some servers did not reload. They keep their last tool lists: "+EscapeMarkdown(err.Error()))
	}
	lines = append(lines, "_The changes apply from your next message. That message does not use the prompt cache._")
	return strings.Join(lines, "\n")
}

// escapeNames returns the names as a comma list. The Markdown markers in a name show
// literally.
func escapeNames(names []string) string {
	return EscapeMarkdown(strings.Join(names, ", "))
}

// plural returns "1 {word}" or "{n} {word}s".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// usageRange is one range of /usage: the argument and the button label.
type usageRange struct{ arg, label string }

// usageRanges are the ranges of /usage in button order. The header names a range with the
// label in lower case.
var usageRanges = []usageRange{{"today", "Today"}, {"7d", "7 days"}, {"30d", "30 days"}, {"session", "Session"}}

// usageButtons returns the range buttons of /usage with a check mark on the current range.
func usageButtons(current string) [][]Button {
	row := make([]Button, 0, len(usageRanges))
	for _, u := range usageRanges {
		label := u.label
		if u.arg == current {
			label = "✓ " + label
		}
		row = append(row, Button{Text: label, Data: "/usage " + u.arg})
	}
	return [][]Button{row}
}

func cmdUsage(ctx context.Context, c *CommandCtx, args string) error {
	if c.Deps.Agent == nil || c.Deps.Agent.DB == nil {
		c.send(Reply{Text: "📊 **Usage is not available**"})
		return nil
	}
	scope := strings.ToLower(strings.TrimSpace(args))
	if scope == "" {
		scope = "today"
	}
	var since time.Time
	var head string
	var session *sessions.Session
	if scope == "session" {
		s, err := c.current(ctx)
		if err != nil {
			return err
		}
		session = s
		head = fmt.Sprintf("📊 **Usage: \"%s\"**", sessionTitle(s.Title))
	} else {
		var ok bool
		if since, ok = obs.UsageSince(scope, time.Now()); !ok {
			names := make([]string, len(usageRanges))
			for i, u := range usageRanges {
				names[i] = u.arg
			}
			c.send(Reply{Text: fmt.Sprintf("⚠️ **Unknown range: %s**\nThe ranges are: %s.",
				EscapeMarkdown(scope), strings.Join(names, ", "))})
			return nil
		}
		for _, u := range usageRanges {
			if u.arg == scope {
				head = "📊 **Usage: " + strings.ToLower(u.label) + "**"
			}
		}
	}
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	rows, compactions, err := obs.UsageReport(ctx, c.Deps.Agent.DB, since, sessionID, FlatProviders(c.Deps.Cfg))
	if err != nil {
		return err
	}
	r := Reply{Edit: args != ""}
	if c.interactive() {
		r.Buttons = usageButtons(scope)
	}
	// tail is the last line of a session report.
	tail := ""
	if session != nil {
		tail = fmt.Sprintf("Compactions: %d", compactions)
	}
	switch {
	case len(rows) == 0 && session != nil:
		unit := "compactions"
		if compactions == 1 {
			unit = "compaction"
		}
		r.Text = fmt.Sprintf("%s\nNo model calls yet. %d %s.", head, compactions, unit)
	case len(rows) == 0:
		r.Text = head + "\nNo model calls in this range."
	case !c.interactive():
		// The CLI keeps the wide table with the tok/s column.
		r.Text = head + "\n" + obs.FormatUsage(rows)
		if tail != "" {
			r.Text += "\n" + tail
		}
	default:
		// Telegram shows the narrow table in a fixed-width font only in a code block. A
		// line right after a Markdown table becomes a table row, so a blank line comes
		// before the tail of the rich message.
		r.Rich = head + "\n\n" + obs.FormatUsageMarkdown(rows)
		r.Text = head + "\n```\n" + obs.FormatUsageNarrow(rows) + "\n```"
		if tail != "" {
			r.Rich += "\n\n" + tail
			r.Text += "\n" + tail
		}
	}
	c.send(r)
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
