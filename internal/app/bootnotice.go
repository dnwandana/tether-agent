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

// BootNotice tells the chats that the gateway is back. The chat that sent /restart gets one
// message. The home chat gets one message when the version changed. A chat that is both
// gets only the first message. It does nothing when Telegram is off or has no channel.
// A send error is logged. The two kv keys are updated in every case.
func (a *App) BootNotice(ctx context.Context, version string) {
	if !a.Cfg.Telegram.Enabled {
		return
	}
	ch, ok := a.Gateway().Channels["telegram"]
	if !ok {
		return
	}
	last, _, _ := a.DB.KVGet(ctx, LastVersionKey)
	changed := last != "" && last != version

	var req gateway.Target
	haveReq := false
	if raw, ok, _ := a.DB.KVGet(ctx, RestartNotifyKey); ok {
		var n restartNotify
		if err := json.Unmarshal([]byte(raw), &n); err == nil && n.Source == "telegram" && n.ChatID != "" {
			req = gateway.Target{Source: n.Source, ChatID: n.ChatID, ThreadID: n.ThreadID}
			haveReq = time.Since(time.UnixMilli(n.At)) <= restartNotifyMaxAge
		}
	}
	home, haveHome := telegram.HomeTarget(ctx, a.DB, a.Cfg.Telegram, false)

	send := func(to gateway.Target, text string) {
		if err := ch.Send(ctx, to, gateway.Outbound{Text: text}); err != nil {
			a.Log.Warn("gateway: cannot send the boot notice", "chat", to.ChatID, "err", err.Error())
		}
	}
	if haveReq {
		text := "♻ The gateway restarted. Your session continues."
		if changed {
			text = "♻ The gateway restarted on tether " + version + " (before: " + last + "). Your session continues."
		}
		send(req, text)
	}
	if changed && haveHome && !(haveReq && sameChat(req, home)) {
		send(home, "♻ Tether updated from "+last+" to "+version+" and restarted.")
	}

	if _, err := a.DB.ExecContext(ctx, "DELETE FROM kv WHERE key=?", RestartNotifyKey); err != nil {
		a.Log.Warn("gateway: cannot clear the restart notice", "err", err.Error())
	}
	if err := a.DB.KVSet(ctx, LastVersionKey, version); err != nil {
		a.Log.Warn("gateway: cannot save the version", "err", err.Error())
	}
}

// sameChat reports whether two targets are the same chat and the same thread.
func sameChat(x, y gateway.Target) bool {
	return x.ChatID == y.ChatID && x.ThreadID == y.ThreadID
}
