package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/learn"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/telegram"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// heldLife is the time that a held memory write waits for the owner.
const heldLife = 24 * time.Hour

// noticeLife is the age at which an open change card is kept as it is.
const noticeLife = 30 * 24 * time.Hour

// LearnParts holds the stores of the memory history, the change notices, the held memory
// writes, and the skill log, with the skill manager and the curator that use them.
type LearnParts struct {
	History  *learn.MemoryHistory
	Notices  *learn.Notices
	Held     *learn.Held
	SkillLog *learn.SkillLog
	Owners   *learn.OwnerTexts
	Skills   *skills.Manager
	Curator  *skills.Curator

	curatorMu sync.Mutex
	curatorAt time.Time // the time of the last curator check
}

// newLearn builds the learning parts. It runs no query and reads no file.
func (a *App) newLearn(skillsDir string) *LearnParts {
	cfg := a.Cfg
	l := &LearnParts{
		History:  learn.NewMemoryHistory(a.DB),
		Notices:  learn.NewNotices(a.DB),
		Held:     learn.NewHeld(a.DB),
		SkillLog: learn.NewSkillLog(a.DB),
	}
	l.Owners = learn.NewOwnerTexts(a.DB, a.isOwnerUser, agent.OwnerWords)
	l.Skills = &skills.Manager{Dir: skillsDir, Guard: cfg.Skills.Guard,
		SkillMDMaxChars: cfg.Skills.SkillMDMaxChars, SupportFileMaxBytes: cfg.Skills.SupportFileMaxBytes,
		Copies: l.SkillLog, HistoryKeep: cfg.Skills.HistoryKeep}
	l.Curator = &skills.Curator{Dir: skillsDir, External: cfg.Skills.ExternalDirs, Store: l.SkillLog,
		CronUsers: func(ctx context.Context, names []string) (map[string][]string, error) {
			return cron.SkillUsers(ctx, a.Cron, names)
		},
		Cfg: cfg.Skills.Curator, Now: a.now}
	return l
}

// now returns the time of a.clock, or the wall time when a.clock is nil.
func (a *App) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}

// memoryThreatText returns the refusal text of the memory guard, or "" for a safe text.
func memoryThreatText(text string) string {
	ids := tools.MemoryThreatIDs(text)
	if len(ids) == 0 {
		return ""
	}
	return "the memory guard refused the text: " + strings.Join(ids, ", ") + "."
}

// isOwnerUser reports whether a Telegram user is an owner: a numeric entry of
// allowed_users or group_allowed_users, or an approved paired user. A "*" entry and
// allow_all_users do not make an owner.
func (a *App) isOwnerUser(userID string) bool {
	if userID == "" {
		return false
	}
	// The lists are the ones of telegram.IsOwner. group_allowed_users makes an owner in a
	// group, and a quote of an owner comes from such a group too. "*" is not a user ID.
	for _, list := range [][]string{a.Cfg.Telegram.AllowedUsers, a.Cfg.Telegram.GroupAllowedUsers} {
		for _, u := range list {
			if strings.TrimSpace(u) == userID {
				if _, err := strconv.ParseInt(userID, 10, 64); err == nil {
					return true
				}
			}
		}
	}
	return (&telegram.Pairing{DB: a.DB}).IsApproved(context.Background(), "telegram", userID)
}

// heldStore adds the memory writes that wait for the owner.
type heldStore struct {
	held *learn.Held
	now  func() time.Time
}

// Hold adds a held row for op and returns its id.
func (h heldStore) Hold(ctx context.Context, op memory.Op, matched, sessionKey string, sources, hosts []string) (int64, error) {
	now := h.now()
	row, err := h.held.Add(ctx, learn.HeldWrite{At: now, ExpiresAt: now.Add(heldLife), Op: op,
		MatchedEntry: matched, Source: op.Source, SessionID: op.SessionID, SessionKey: sessionKey,
		Sources: sources, Hosts: hosts, State: "held"})
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

// skillRead returns the function that records a skill read with the given source.
func (a *App) skillRead(source string) func(ctx context.Context, name, sessionID string) {
	return func(ctx context.Context, name, sessionID string) {
		if err := a.Learn.SkillLog.RecordRead(ctx, name, source, sessionID); err != nil {
			a.Log.Warn("cannot record the read of a skill", "skill", name, "err", err.Error())
		}
	}
}

var errNoTelegram = errors.New("the Telegram channel is not on in this role")

// learnDeps returns the learning parts for the gateway.
func (a *App) learnDeps() *gateway.LearnDeps {
	return &gateway.LearnDeps{
		Memory: a.Memory, Notices: a.Learn.Notices, Held: a.Learn.Held, Skills: a.Learn.Skills,
		Curator: a.Learn.Curator, Copies: a.Learn.SkillLog, Tidy: a.Agent.RunTidy,
		GroupHasMember: a.groupHasMember, SendPrivate: a.sendPrivate, SendOwner: a.sendOwner,
		RefreshMenu: a.refreshMenu,
	}
}

// groupHasMember reports whether the Telegram group of key has a user who is not an owner.
func (a *App) groupHasMember(ctx context.Context, key string) bool {
	if a.Telegram == nil {
		return false
	}
	sess, err := a.Store.Active(ctx, key)
	if err != nil || sess == nil || sess.Source != "telegram" {
		return false
	}
	chat, err := strconv.ParseInt(sess.ChatID, 10, 64)
	if err != nil {
		return false
	}
	return a.Telegram.ChatHasMember(ctx, chat)
}

// sendPrivate sends r to the private chat of the user of the session of key.
func (a *App) sendPrivate(ctx context.Context, key string, r gateway.Reply) error {
	if a.Telegram == nil {
		return errNoTelegram
	}
	sess, err := a.Store.Active(ctx, key)
	if err != nil {
		return err
	}
	if sess == nil {
		return errors.New("no active session for " + key)
	}
	return a.Telegram.SendPrivateReply(ctx, sess.UserID, r)
}

// sendOwner sends r to the owner with no turn.
func (a *App) sendOwner(ctx context.Context, r gateway.Reply) error {
	if a.Telegram == nil {
		return errNoTelegram
	}
	return a.Telegram.SendOwnerReply(ctx, r)
}

// refreshMenu makes Telegram read the skill list again.
func (a *App) refreshMenu(ctx context.Context) {
	if a.Telegram == nil {
		return
	}
	if err := a.Telegram.RefreshCommands(ctx); err != nil {
		a.Log.Warn("cannot refresh the Telegram command menu", "err", err.Error())
	}
}

// sweepLearn moves the due held memory writes to a note, keeps the old cards, and runs the
// curator check. A failed step does not stop the next step.
func (a *App) sweepLearn(ctx context.Context) {
	now := a.now()
	rows, err := a.Learn.Held.Due(ctx, now)
	if err != nil {
		a.Log.Warn("cannot list the due held memory writes", "err", err.Error())
	}
	for _, h := range rows {
		state := "dropped"
		if h.Op.Action == "add" || h.Op.Action == "replace" {
			if err := a.Memory.AppendHeldNote(ctx, heldNoteText(h)); err != nil {
				a.Log.Warn("cannot write the held memory note", "id", h.ID, "err", err.Error())
				continue
			}
			state = "swept"
		}
		if err := a.Learn.Held.SetState(ctx, h.ID, state); err != nil {
			a.Log.Warn("cannot end a held memory write", "id", h.ID, "err", err.Error())
		}
	}
	if _, err := a.Learn.Notices.ExpireOpen(ctx, now.Add(-noticeLife)); err != nil {
		a.Log.Warn("cannot keep the old change cards", "err", err.Error())
	}
	a.curatorTick(ctx, now)
}

// heldNoteText returns the note text of a held write that nobody answered.
func heldNoteText(h learn.HeldWrite) string {
	return "[held " + h.At.Local().Format("2006-01-02") + ", from " + strings.Join(h.Sources, ", ") + "]\n" + h.Op.Content
}

// curatorTick runs the skill curator at most once an hour. It sends a card to the owner when
// a run archived a skill.
func (a *App) curatorTick(ctx context.Context, now time.Time) {
	if !a.Cfg.Skills.Curator.Enabled {
		return
	}
	l := a.Learn
	l.curatorMu.Lock()
	if !l.curatorAt.IsZero() && now.Sub(l.curatorAt) < time.Hour {
		l.curatorMu.Unlock()
		return
	}
	l.curatorAt = now
	l.curatorMu.Unlock()
	idle, _, err := newestChatActive(ctx, a.DB)
	if err != nil {
		a.Log.Warn("curator: cannot read the last chat time", "err", err.Error())
		return
	}
	rep, ran, err := l.Curator.Run(ctx, false, idle)
	if err != nil {
		a.Log.Warn("curator: the run failed", "err", err.Error())
		return
	}
	if !ran || len(rep.Archived) == 0 {
		return
	}
	a.refreshMenu(ctx)
	if a.Telegram == nil {
		return
	}
	items := make([]learn.NoticeItem, 0, len(rep.Archived))
	for _, f := range rep.Archived {
		items = append(items, learn.NoticeItem{Kind: "archive", ID: f.ID})
	}
	n, err := l.Notices.Create(ctx, "", gateway.CuratorCardText(rep), items)
	if err != nil {
		a.Log.Warn("curator: cannot save the card", "err", err.Error())
		return
	}
	if err := a.Telegram.SendOwnerReply(ctx, gateway.CardReply(n, true)); err != nil {
		a.Log.Warn("curator: cannot send the card", "err", err.Error())
	}
}

// newestChatActive returns the newest last_active time of the chat sessions.
func newestChatActive(ctx context.Context, d *db.DB) (time.Time, bool, error) {
	var ms *int64
	if err := d.QueryRowContext(ctx, `SELECT MAX(last_active) FROM sessions WHERE kind = 'chat'`).Scan(&ms); err != nil {
		return time.Time{}, false, err
	}
	if ms == nil {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(*ms), true, nil
}
