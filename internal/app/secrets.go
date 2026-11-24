package app

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/dashboard"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// secretMarkPrefix is the kv key prefix of the auth-failure mark of a bound secret. The
// value is the Unix time in milliseconds. The doctor reads the same keys.
const secretMarkPrefix = "secret_check:"

// SaveSecret saves value as name in .env, updates the value store and the redactor, and clears
// the auth-failure mark of name. The value never goes to a log line or an error text.
func (a *App) SaveSecret(name, value string) error {
	if err := security.CheckSecretValue(value); err != nil {
		return err
	}
	if err := security.SaveDotEnv(filepath.Join(a.DataDir, ".env"), name, value); err != nil {
		return err
	}
	// The redactor learns the value before the next tool result.
	a.Redactor.Add(name, value)
	if a.DotEnv != nil {
		a.DotEnv.Set(name, value)
	}
	if err := a.DB.KVDelete(context.Background(), secretMarkPrefix+name); err != nil {
		// The value is saved. A mark that stays only shows an old warning.
		a.Log.Warn("secrets: cannot clear the auth-failure mark", "name", name, "err", err.Error())
	}
	return nil
}

// kvMarks keeps the auth-failure marks of the bound secrets in kv.
type kvMarks struct{ DB *db.DB }

// Mark records the time of an auth failure of name.
func (m kvMarks) Mark(ctx context.Context, name string, at time.Time) error {
	return m.DB.KVSet(ctx, secretMarkPrefix+name, strconv.FormatInt(at.UnixMilli(), 10))
}

// Clear removes the mark of name.
func (m kvMarks) Clear(ctx context.Context, name string) error {
	return m.DB.KVDelete(ctx, secretMarkPrefix+name)
}

var _ tools.SecretMarks = kvMarks{}

// secretsDeps builds the functions that the /secrets command uses.
func (a *App) secretsDeps() gateway.SecretsDeps {
	return gateway.SecretsDeps{
		Bound: func() map[string][]string { return boundCommands(a.Cfg) },
		HasValue: func(name string) bool {
			if a.DotEnv == nil {
				return false
			}
			// A value that tether setup saved after the start shows at once.
			a.DotEnv.Refresh()
			v, ok := a.DotEnv.Get(name)
			return ok && v != ""
		},
		Marks: func(ctx context.Context) (map[string]time.Time, error) {
			out := map[string]time.Time{}
			for name := range a.Cfg.ExecSecrets {
				v, ok, err := a.DB.KVGet(ctx, secretMarkPrefix+name)
				if err != nil {
					return nil, err
				}
				if !ok {
					continue
				}
				ms, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				if err != nil {
					continue
				}
				out[name] = time.UnixMilli(ms)
			}
			return out, nil
		},
		// The terminal tool does not exist yet when the command registers, so these read it late.
		Pins: func(ctx context.Context, name string) ([]security.PinTarget, error) {
			return a.terminal.Pins.List(ctx, name)
		},
		Unpin: func(ctx context.Context, name string) (int64, error) {
			return a.terminal.Pins.Unpin(ctx, name)
		},
		UnpinPath: func(ctx context.Context, name, path string) (int64, error) {
			return a.terminal.Pins.UnpinPath(ctx, name, path)
		},
		Start: a.startSecret,
	}
}

// startSecret begins the save of a new value for name. The CLI reads the value in the command.
// Telegram and the dashboard send a card and return at once.
func (a *App) startSecret(ctx context.Context, c *gateway.CommandCtx, name string) error {
	q := tools.SecretQuestion{Name: name, Commands: a.Cfg.ExecSecrets[name].Commands}
	if strings.HasPrefix(c.Key, "cli:") && a.CLIAsk != nil {
		_, err := a.CLIAsk.AskSecret(ctx, q)
		return err
	}
	if strings.HasPrefix(c.Key, dashboard.ChannelName+":") {
		if a.Dashboard == nil {
			return errors.New("no channel can ask for the value")
		}
		// The card waits longer than the command. The work context of the channel lives
		// until the gateway stops, and an HTTP request never owns the wait.
		cards, cardCtx := a.Dashboard.Cards(), a.Dashboard.WorkCtx()
		go func() {
			if _, err := cards.AskSecret(cardCtx, q); err != nil {
				a.Log.Warn("secrets: the dashboard card failed", "name", name, "err", err.Error())
			}
		}()
		return nil
	}
	if a.Telegram != nil && a.Telegram.Secrets != nil && c.Session != nil {
		chatID, err := strconv.ParseInt(c.Session.ChatID, 10, 64)
		if err != nil {
			return errors.New("no channel can ask for the value")
		}
		// /secrets runs only in a private chat, so the chat id is the user id. The card waits
		// longer than the command, so it gets a context that the command end does not cancel.
		cardCtx := context.WithoutCancel(ctx)
		ownerID := c.Session.ChatID
		go func() {
			if _, err := a.Telegram.Secrets.Ask(cardCtx, chatID, ownerID, q); err != nil {
				a.Log.Warn("secrets: the card failed", "name", name, "err", err.Error())
			}
		}()
		return nil
	}
	return errors.New("no channel can ask for the value")
}
