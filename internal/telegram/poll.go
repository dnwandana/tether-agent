package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
)

// offsetKey is the kv key that holds the next getUpdates offset.
const offsetKey = "telegram.update_offset"

const (
	// defaultPollTimeoutS is the getUpdates long-poll timeout in seconds.
	defaultPollTimeoutS = 50
	// maxPollBackoff is the longest wait after a failed getUpdates call.
	maxPollBackoff = 30 * time.Second
	// singleModeMax is the number of updates after which the single mode ends. One
	// getUpdates reply holds 100 updates at most, so the batch that failed is then read.
	singleModeMax = 100
)

// Poller reads updates with the getUpdates long poll. It stores the offset in
// state.db, so a restart does not replay updates.
type Poller struct {
	C        *Client
	DB       *db.DB
	Log      *slog.Logger
	TimeoutS int
	// HandleCtx is the context of the handle calls. Nil uses the context of Run. A
	// separate context lets the turns go on after Run stops to poll.
	HandleCtx context.Context
	// Inflight counts each message update from its push to the queue of its chat until
	// its handle call returns. It can be nil.
	Inflight *atomic.Int64

	// sleep waits between failed calls. Tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
}

// Run polls until ctx ends, and then returns the context error. It stores the
// offset of each update before it calls handle, so delivery is at most once.
// The messages of one chat go to handle one at a time, in the order of the
// updates, because the gateway, its debouncer, and the steer queue need that
// order. The messages of other chats run in parallel. A callback query runs in
// its own goroutine, because a button press must not wait for a message. So
// handle must return soon: it must not wait for a turn. For an edited
// message, handle gets the edit in both Message and EditedMessage. After a reply that
// it cannot read, Run reads one update for each call. It skips a single update that does
// not decode. Run returns only after each handle call that it started has returned.
func (p *Poller) Run(ctx context.Context, handle func(ctx context.Context, u Update)) error {
	hctx := p.HandleCtx
	if hctx == nil {
		hctx = ctx
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	chats := &chatQueues{handle: handle, log: p.logger(), wg: &wg, inflight: p.Inflight}
	timeout := p.TimeoutS
	if timeout <= 0 {
		timeout = defaultPollTimeoutS
	}
	offset, err := p.loadOffset(ctx)
	if err != nil {
		p.logger().Warn("telegram offset read failed, start from the server offset", "err", err.Error())
	}
	var backoff time.Duration
	// single is true while the poller reads one update for each call. seen counts the
	// updates of the single mode, also the updates that the poller skips.
	single, seen := false, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		limit := 0
		if single {
			limit = 1
		}
		ups, err := p.C.GetUpdatesN(ctx, offset, timeout, limit)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var ue *UnreadableError
			unreadable := errors.As(err, &ue)
			switch {
			case unreadable && !single:
				// A new call with the same offset gives the same reply. Read the updates
				// one at a time, to find the update that the poller cannot read.
				single, seen = true, 0
				p.logger().Warn("telegram: cannot read the update batch, read one update at a time")
				continue
			case unreadable && ue.HasID && ue.UpdateID >= offset:
				p.logger().Warn("telegram: update cannot be read, skip it", "update", ue.UpdateID)
				offset = ue.UpdateID + 1
				if err := p.DB.KVSet(ctx, offsetKey, strconv.FormatInt(offset, 10)); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					p.logger().Warn("telegram offset write failed", "err", err.Error())
				}
				backoff = 0
				if seen++; seen >= singleModeMax {
					single = false
				}
				continue
			}
			backoff = nextBackoff(backoff)
			if unreadable {
				// The poller cannot go past this update, because it has no id to skip.
				p.logger().Warn("telegram: the update at the offset cannot be read", "offset", offset)
			} else {
				p.logger().Warn("telegram getUpdates failed, wait and retry", "err", err.Error(), "wait_s", backoff.Seconds())
			}
			if err := p.wait(ctx, backoff); err != nil {
				return err
			}
			continue
		}
		backoff = 0
		if single {
			seen += len(ups)
			if len(ups) == 0 || seen >= singleModeMax {
				single = false
			}
		}
		for _, u := range ups {
			if u.UpdateID < offset {
				continue
			}
			offset = u.UpdateID + 1
			if err := p.DB.KVSet(ctx, offsetKey, strconv.FormatInt(offset, 10)); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				p.logger().Warn("telegram offset write failed", "err", err.Error())
			}
			if u.Message == nil && u.EditedMessage != nil {
				// Hermes handles an edit like a new message, because
				// python-telegram-bot gives the edit as effective_message.
				u.Message = u.EditedMessage
			}
			if u.Message != nil {
				chats.push(hctx, u.Message.Chat.ID, u)
			} else {
				wg.Add(1)
				go func() {
					defer wg.Done()
					chats.run(hctx, u)
				}()
			}
		}
	}
}

// chatQueues runs the updates of each chat in order, one at a time. Each chat
// with updates has one worker goroutine. The worker stops when the queue of
// its chat is empty.
type chatQueues struct {
	handle func(ctx context.Context, u Update)
	log    *slog.Logger
	// wg counts the workers.
	wg *sync.WaitGroup
	// inflight counts the queued and running updates. It can be nil.
	inflight *atomic.Int64

	mu sync.Mutex
	// queues holds the waiting updates of each chat. A chat is in the map
	// while its worker runs.
	queues map[int64][]Update
}

// push adds u to the queue of chat and starts the worker of chat when no
// worker runs.
func (c *chatQueues) push(ctx context.Context, chat int64, u Update) {
	if c.inflight != nil {
		c.inflight.Add(1)
	}
	c.mu.Lock()
	if c.queues == nil {
		c.queues = map[int64][]Update{}
	}
	q, running := c.queues[chat]
	c.queues[chat] = append(q, u)
	c.mu.Unlock()
	if !running {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.work(ctx, chat)
		}()
	}
}

// work handles the updates of chat in order until the queue is empty.
func (c *chatQueues) work(ctx context.Context, chat int64) {
	for {
		c.mu.Lock()
		q := c.queues[chat]
		if len(q) == 0 {
			delete(c.queues, chat)
			c.mu.Unlock()
			return
		}
		u := q[0]
		c.queues[chat] = q[1:]
		c.mu.Unlock()
		c.run(ctx, u)
		if c.inflight != nil {
			c.inflight.Add(-1)
		}
	}
}

// run calls handle for u. A panic does not stop the worker of the chat.
func (c *chatQueues) run(ctx context.Context, u Update) {
	defer func() {
		if v := recover(); v != nil {
			c.log.Error("telegram: the update handler panicked", "update", u.UpdateID, "panic", fmt.Sprint(v))
		}
	}()
	c.handle(ctx, u)
}

// RegisterCommands sets the bot menu with the built-in commands and the skill commands
// cmds. When Telegram refuses the menu with the skills, it sets the built-in commands only,
// so one bad skill entry does not remove the menu.
func (p *Poller) RegisterCommands(ctx context.Context, cmds []gateway.SkillCommand) error {
	menu, hidden := botMenu(cmds)
	if hidden > 0 {
		p.logger().Info(fmt.Sprintf("telegram: the bot menu hides %d skill commands; send /help skills to see all", hidden))
	}
	err := p.C.SetMyCommands(ctx, menu)
	builtins := builtinCommands()
	// A canceled ctx is a stop of the gateway, not a refusal of the menu.
	if err == nil || len(menu) == len(builtins) || ctx.Err() != nil {
		return err
	}
	p.logger().Warn("telegram: cannot set the skill commands in the bot menu; the menu gets the built-in commands only",
		"err", err.Error())
	return p.C.SetMyCommands(ctx, builtins)
}

// menuMax is the maximum number of entries in the bot menu (the Hermes default).
const menuMax = 60

// menuNameMax is the Telegram limit for the length of a command name.
const menuNameMax = 32

// menuDashes changes the Unicode dashes to "-", because BotFather rejects them (Hermes).
var menuDashes = strings.NewReplacer("\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-",
	"\u2014", "-", "\u2015", "-", "\u2212", "-")

// BotCommands returns the bot menu: the built-in commands, and then the skill commands of
// cmds in their order. cmds is the skill command table, in slug order.
func BotCommands(cmds []gateway.SkillCommand) []BotCommand {
	menu, _ := botMenu(cmds)
	return menu
}

// botMenu returns the bot menu and the number of skill commands that the cap leaves out. A
// skill with a menu name over 32 characters, or with a name already in the menu, is left
// out. Its typed command still works.
func botMenu(cmds []gateway.SkillCommand) ([]BotCommand, int) {
	menu := builtinCommands()
	seen := make(map[string]bool, len(menu)+len(cmds))
	for _, b := range menu {
		seen[b.Command] = true
	}
	hidden := 0
	for _, c := range cmds {
		if len(c.MenuName) > menuNameMax || seen[c.MenuName] {
			continue
		}
		if len(menu) >= menuMax {
			hidden++
			continue
		}
		seen[c.MenuName] = true
		menu = append(menu, BotCommand{Command: c.MenuName, Description: menuDescription(c)})
	}
	return menu, hidden
}

// menuDescription returns the menu text of a skill command: the description on one line,
// with ASCII dashes, cut to 37 runes plus "..." (Hermes).
func menuDescription(c gateway.SkillCommand) string {
	d := menuDashes.Replace(oneLine(c.Skill.Description))
	if r := []rune(d); len(r) > 40 {
		d = string(r[:37]) + "..."
	}
	if d == "" {
		d = "Run the " + c.Slug + " skill"
	}
	return d
}

// builtinCommands returns the bot command menu. The most-used commands come first. Telegram does
// not allow "-" in a command name, so "/reload-mcp" is "reload_mcp" here. "/start",
// "/reset", "/resume", and "/subgoal" still work, but they are not in the menu.
func builtinCommands() []BotCommand {
	return []BotCommand{
		{Command: "new", Description: "Start a new session"},
		{Command: "sessions", Description: "See and resume sessions"},
		{Command: "stop", Description: "Stop the current task"},
		{Command: "steer", Description: "Send a note to the current task"},
		{Command: "now", Description: "Do my waiting message now"},
		{Command: "goal", Description: "Work toward a goal"},
		{Command: "todo", Description: "Show the task list"},
		{Command: "model", Description: "Change the model"},
		{Command: "effort", Description: "Change the reasoning effort"},
		{Command: "usage", Description: "See tokens and cost"},
		{Command: "help", Description: "See all commands"},
		{Command: "rename", Description: "Change the session title"},
		{Command: "fork", Description: "Copy this session"},
		{Command: "compact", Description: "Make the context shorter"},
		{Command: "prompt", Description: "Change the prompt"},
		{Command: "archive", Description: "Hide this session"},
		{Command: "delete", Description: "Delete a session"},
		{Command: "approve", Description: "Allow the waiting command"},
		{Command: "deny", Description: "Deny the waiting command"},
		{Command: "sethome", Description: "Send cron results here"},
		{Command: "reload_mcp", Description: "Reload the MCP servers"},
		{Command: "restart", Description: "Restart the gateway"},
	}
}

// commandAliases maps a Telegram command name to the name of the shared
// gateway command.
var commandAliases = map[string]string{"/reload_mcp": "/reload-mcp"}

// CanonicalCommand changes a Telegram command at the start of text to the
// shared command name, for example "/reload_mcp" to "/reload-mcp". It keeps
// an "@bot" suffix and the arguments. Other text does not change.
func CanonicalCommand(text string) string {
	if !strings.HasPrefix(text, "/") {
		return text
	}
	end := strings.IndexAny(text, " \n\t@")
	if end < 0 {
		end = len(text)
	}
	if alias, ok := commandAliases[text[:end]]; ok {
		return alias + text[end:]
	}
	return text
}

// nextBackoff returns the wait after one more failed call: 1 s, 2 s, 4 s, and
// so on, up to 30 s.
func nextBackoff(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	return min(2*d, maxPollBackoff)
}

func (p *Poller) loadOffset(ctx context.Context) (int64, error) {
	v, ok, err := p.DB.KVGet(ctx, offsetKey)
	if err != nil || !ok {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, errors.New("telegram: stored update offset is not a number: " + v)
	}
	return n, nil
}

func (p *Poller) wait(ctx context.Context, d time.Duration) error {
	if p.sleep != nil {
		return p.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (p *Poller) logger() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}
