package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/telegram"
)

// LastVersionKey is the kv key of the version that the last gateway run reported.
const LastVersionKey = "gateway.last_version"

// restartNotifyMaxAge is the age after which a saved /restart chat gets no message.
const restartNotifyMaxAge = 10 * time.Minute

// BootNotice tells the chats that the gateway is back. The chat that sent /restart can be a
// Telegram chat or the dashboard, and it gets one message. The home chat is Telegram only and
// gets one message when the version changed. A chat that is both gets only the first message.
// The function sends nothing to a channel that is off. A send error is logged. The two kv keys
// are updated in every case.
func (a *App) BootNotice(ctx context.Context, version string) {
	// channelOf returns the channel of source. Telegram counts only when telegram.enabled is
	// true, because a test or an old run can leave a channel in the map.
	channelOf := func(source string) (gateway.Channel, bool) {
		switch source {
		case "telegram":
			if !a.Cfg.Telegram.Enabled {
				return nil, false
			}
		case "dashboard":
		default:
			return nil, false
		}
		ch, ok := a.Gateway().Channels[source]
		return ch, ok
	}
	last, _, _ := a.DB.KVGet(ctx, LastVersionKey)
	changed := last != "" && last != version

	var req gateway.Target
	var reqCh gateway.Channel
	haveReq := false
	if raw, ok, _ := a.DB.KVGet(ctx, RestartNotifyKey); ok {
		var n restartNotify
		if err := json.Unmarshal([]byte(raw), &n); err == nil && n.ChatID != "" {
			if c, ok := channelOf(n.Source); ok {
				reqCh = c
				req = gateway.Target{Source: n.Source, ChatID: n.ChatID, ThreadID: n.ThreadID}
				haveReq = time.Since(time.UnixMilli(n.At)) <= restartNotifyMaxAge
			}
		}
	}
	var home gateway.Target
	haveHome := false
	tgCh, tgOn := channelOf("telegram")
	if tgOn {
		home, haveHome = telegram.HomeTarget(ctx, a.DB, a.Cfg.Telegram, false)
	}

	send := func(ch gateway.Channel, to gateway.Target, text string) {
		if err := ch.Send(ctx, to, gateway.Outbound{Text: text}); err != nil {
			a.Log.Warn("gateway: cannot send the boot notice", "source", to.Source, "chat", to.ChatID, "err", err.Error())
		}
	}
	if haveReq {
		text := "♻ The gateway restarted. Your session continues."
		if changed {
			text = "♻ The gateway restarted on tether " + version + " (before: " + last + "). Your session continues."
		}
		send(reqCh, req, text)
	}
	if changed && haveHome && !(haveReq && sameChat(req, home)) {
		send(tgCh, home, "♻ Tether updated from "+last+" to "+version+" and restarted.")
	}

	if _, err := a.DB.ExecContext(ctx, "DELETE FROM kv WHERE key=?", RestartNotifyKey); err != nil {
		a.Log.Warn("gateway: cannot clear the restart notice", "err", err.Error())
	}
	if err := a.DB.KVSet(ctx, LastVersionKey, version); err != nil {
		a.Log.Warn("gateway: cannot save the version", "err", err.Error())
	}
}

// sameChat reports whether two targets are the same channel, chat, and thread.
func sameChat(x, y gateway.Target) bool {
	return x.Source == y.Source && x.ChatID == y.ChatID && x.ThreadID == y.ThreadID
}
