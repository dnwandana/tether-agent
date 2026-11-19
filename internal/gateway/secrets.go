package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

const (
	secretsHelp       = "See and save the bound secrets: /secrets [set NAME | unpin NAME]"
	secretsGroupText  = "Send /secrets in a private chat with me."
	secretsNoneText   = "🔑 No bound secrets. Bind a secret in exec\\_secrets in config.yaml."
	secretsListHeader = "🔑 Bound secrets"
	secretsListTail   = "Send /secrets set NAME to save a new value."
)

// SecretsDeps holds the data of /secrets. app fills it, because gateway cannot import app or
// telegram.
type SecretsDeps struct {
	// Bound returns each bound name with its commands.
	Bound func() map[string][]string
	// HasValue reports whether .env holds a value for the name.
	HasValue func(name string) bool
	// Marks returns the time of the last auth failure for each marked name.
	Marks func(ctx context.Context) (map[string]time.Time, error)
	// Pins returns the allowed programs of a name.
	Pins func(ctx context.Context, name string) ([]security.PinTarget, error)
	// Unpin removes the allowed programs of a name and returns how many it removed.
	Unpin func(ctx context.Context, name string) (int64, error)
	// Start begins the save of a new value. In Telegram it sends the card and returns at once.
	Start func(ctx context.Context, c *CommandCtx, name string) error
}

// SecretsCommand returns the owner-only /secrets command. It lists the bound secrets, starts
// the save of a new value, and removes the allowed programs of a name.
func SecretsCommand(d SecretsDeps) Command {
	return Command{
		Name:      "secrets",
		Help:      secretsHelp,
		WhileBusy: true,
		Run: func(ctx context.Context, c *CommandCtx, args string) error {
			if secretsInGroup(c) {
				c.send(Reply{Text: secretsGroupText})
				return nil
			}
			f := strings.Fields(args)
			switch {
			case len(f) == 0:
				return secretsList(ctx, c, d)
			case len(f) == 2 && f[0] == "set":
				if !secretsIsBound(d, f[1]) {
					c.send(Reply{Text: secretsNotBound(f[1])})
					return nil
				}
				if d.Start == nil {
					return nil
				}
				if err := d.Start(ctx, c, f[1]); err != nil {
					c.send(Reply{Text: "⚠️ " + EscapeMarkdown(err.Error())})
				}
				return nil
			case len(f) == 2 && f[0] == "unpin":
				return secretsUnpin(ctx, c, d, f[1])
			}
			c.send(Reply{Text: secretsHelp})
			return nil
		},
	}
}

// secretsInGroup reports whether the caller is in a group chat.
func secretsInGroup(c *CommandCtx) bool {
	if strings.HasPrefix(c.Key, "telegram:group:") {
		return true
	}
	return c.Session != nil && strings.HasPrefix(c.Session.ChatID, "-")
}

func secretsIsBound(d SecretsDeps, name string) bool {
	if d.Bound == nil {
		return false
	}
	_, ok := d.Bound()[name]
	return ok
}

func secretsNotBound(name string) string {
	return "\"" + EscapeMarkdown(name) + "\" is not a bound secret. The owner binds secrets in exec\\_secrets in config.yaml."
}

func secretsList(ctx context.Context, c *CommandCtx, d SecretsDeps) error {
	var bound map[string][]string
	if d.Bound != nil {
		bound = d.Bound()
	}
	if len(bound) == 0 {
		c.send(Reply{Text: secretsNoneText})
		return nil
	}
	var marks map[string]time.Time
	if d.Marks != nil {
		m, err := d.Marks(ctx)
		if err != nil {
			return err
		}
		marks = m
	}
	names := make([]string, 0, len(bound))
	for n := range bound {
		names = append(names, n)
	}
	sort.Strings(names)
	lines := []string{secretsListHeader}
	for _, n := range names {
		state := "not set"
		if d.HasValue != nil && d.HasValue(n) {
			state = "set"
			if at, ok := marks[n]; ok {
				state = "⚠️ check (auth failure " + at.Local().Format("Jan 2 15:04") + ")"
			}
		}
		lines = append(lines, fmt.Sprintf("• %s: %s · %s", EscapeMarkdown(n), state, EscapeMarkdown(strings.Join(bound[n], ", "))))
		if d.Pins == nil {
			continue
		}
		pins, err := d.Pins(ctx, n)
		if err != nil {
			return err
		}
		if len(pins) == 0 {
			continue
		}
		paths := make([]string, len(pins))
		for i, p := range pins {
			paths[i] = EscapeMarkdown(p.Path)
		}
		lines = append(lines, "  Programs: "+strings.Join(paths, ", "))
	}
	lines = append(lines, secretsListTail)
	c.send(Reply{Text: strings.Join(lines, "\n")})
	return nil
}

func secretsUnpin(ctx context.Context, c *CommandCtx, d SecretsDeps, name string) error {
	if !secretsIsBound(d, name) {
		c.send(Reply{Text: secretsNotBound(name)})
		return nil
	}
	var n int64
	if d.Unpin != nil {
		var err error
		if n, err = d.Unpin(ctx, name); err != nil {
			return err
		}
	}
	e := EscapeMarkdown(name)
	switch n {
	case 0:
		c.send(Reply{Text: e + " has no allowed programs."})
	case 1:
		c.send(Reply{Text: "Removed 1 allowed program of " + e + ". The next use asks again."})
	default:
		c.send(Reply{Text: fmt.Sprintf("Removed %d allowed programs of %s. The next use asks again.", n, e)})
	}
	return nil
}
