package gateway

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// modelAction is the form of a /model command.
type modelAction int

const (
	// modelShow shows the model in use and the providers: /model.
	modelShow modelAction = iota
	// modelProviders shows the same reply as modelShow in the old message: /model providers.
	modelProviders
	// modelClear removes the model override of the session: /model clear.
	modelClear
	// modelList shows a page of the model list of an entry: /model <entry> [<page>].
	modelList
	// modelRefresh gets the model list of an entry again: /model <entry> refresh.
	modelRefresh
	// modelConfig selects the model of the entry in config.yaml: /model <entry> config.
	modelConfig
	// modelUse selects a model by its position in a list: /model <entry> use <i> <stamp>.
	modelUse
	// modelSet selects a model by its ID: /model <entry>:<model>.
	modelSet
)

// modelArgs is the parsed argument text of a /model command.
type modelArgs struct {
	Action modelAction
	Entry  string
	Model  string
	Page   int
	Index  int
	Stamp  string
}

// parseModelArgs parses the argument text of /model. It returns false for an unknown form,
// so a model ID with a space is refused.
func parseModelArgs(args string) (modelArgs, bool) {
	f := strings.Fields(args)
	switch len(f) {
	case 0:
		return modelArgs{Action: modelShow}, true
	case 1:
		switch f[0] {
		case "clear":
			return modelArgs{Action: modelClear}, true
		case "providers":
			return modelArgs{Action: modelProviders}, true
		}
		if entry, model, ok := strings.Cut(f[0], ":"); ok {
			return modelArgs{Action: modelSet, Entry: entry, Model: model}, true
		}
		return modelArgs{Action: modelList, Entry: f[0], Page: 1}, true
	case 2:
		if strings.Contains(f[0], ":") {
			return modelArgs{}, false
		}
		switch f[1] {
		case "refresh":
			return modelArgs{Action: modelRefresh, Entry: f[0]}, true
		case "config":
			return modelArgs{Action: modelConfig, Entry: f[0]}, true
		}
		if n, ok := positiveInt(f[1]); ok {
			return modelArgs{Action: modelList, Entry: f[0], Page: n}, true
		}
	case 4:
		if strings.Contains(f[0], ":") || f[1] != "use" || !isStamp(f[3]) {
			return modelArgs{}, false
		}
		if n, ok := positiveInt(f[2]); ok {
			return modelArgs{Action: modelUse, Entry: f[0], Index: n, Stamp: f[3]}, true
		}
	}
	return modelArgs{}, false
}

// positiveInt returns the number in s when it is 1 or more.
func positiveInt(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1
}

// isStamp tells if s has the format of a list stamp: 4 characters of 0-9a-f.
func isStamp(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// cmdModel shows or changes the model of the current session. Only the forms config, use,
// <entry>:<model>, and clear change the stored override.
func cmdModel(ctx context.Context, c *CommandCtx, args string) error {
	s, err := c.current(ctx)
	if err != nil {
		return err
	}
	a, ok := parseModelArgs(args)
	if !ok {
		c.send(unknownModelForm(args))
		return nil
	}
	var pc config.Provider
	if a.Entry != "" || a.Action == modelSet {
		if _, ok := c.Deps.Providers[a.Entry]; !ok {
			c.send(Reply{Text: fmt.Sprintf("⚠️ **Unknown provider: %s**\nThe providers are: %s.",
				EscapeMarkdown(a.Entry), strings.Join(c.providerNames(), ", "))})
			return nil
		}
		if c.Deps.Cfg != nil {
			pc = c.Deps.Cfg.Providers[a.Entry]
		}
	}
	switch a.Action {
	case modelShow:
		c.send(c.modelReply(s))
	case modelProviders:
		r := c.modelReply(s)
		r.Edit = true
		c.send(r)
	case modelClear:
		if err := c.store().SetModel(ctx, s.ID, ""); err != nil {
			return err
		}
		s.ModelOverride = ""
		c.send(Reply{Text: "🧠 **Model: default**\nThis session uses the default model again."})
	case modelList:
		c.send(c.modelListReply(ctx, s, a.Entry, a.Page, ""))
	case modelRefresh:
		c.Deps.Models.Forget(a.Entry, pc)
		c.send(c.modelListReply(ctx, s, a.Entry, 1, ""))
	case modelConfig:
		if err := c.store().SetModel(ctx, s.ID, a.Entry); err != nil {
			return err
		}
		s.ModelOverride = a.Entry
		c.send(c.modelSetReply(a.Entry, pc.Model))
	case modelSet:
		if a.Model == "" {
			c.send(Reply{Text: "⚠️ **No model after the colon**\nSend /model <provider> or /model <provider>:<model>."})
			return nil
		}
		override := a.Entry + ":" + a.Model
		if err := c.store().SetModel(ctx, s.ID, override); err != nil {
			return err
		}
		s.ModelOverride = override
		c.send(c.modelSetReply(a.Entry, a.Model))
	case modelUse:
		return cmdModelUse(ctx, c, s, a)
	default:
		c.send(unknownModelForm(args))
	}
	return nil
}

// cmdModelUse selects the model at position a.Index of the list of a.Entry. It changes the
// model only when the list still has the stamp a.Stamp, so an old button never selects a
// wrong model.
func cmdModelUse(ctx context.Context, c *CommandCtx, s *sessions.Session, a modelArgs) error {
	var pc config.Provider
	if c.Deps.Cfg != nil {
		pc = c.Deps.Cfg.Providers[a.Entry]
	}
	list, err := c.Deps.Models.Get(ctx, a.Entry, pc)
	if err != nil {
		r := c.modelListReply(ctx, s, a.Entry, 1, "")
		r.Edit = true
		c.send(r)
		return nil
	}
	// The stamp check comes before the range check, so a changed list never selects a model.
	if list.Stamp() != a.Stamp {
		r := c.modelListReply(ctx, s, a.Entry, 1, "The list changed. Select the model again.")
		r.Edit = true
		c.send(r)
		return nil
	}
	ids := list.All()
	if a.Index < 1 || a.Index > len(ids) {
		c.send(Reply{Text: fmt.Sprintf("⚠️ **No model at position %d**\nSend /model %s to see the list.",
			a.Index, EscapeMarkdown(a.Entry)), Edit: true})
		return nil
	}
	model := ids[a.Index-1]
	override := a.Entry + ":" + model
	if err := c.store().SetModel(ctx, s.ID, override); err != nil {
		return err
	}
	s.ModelOverride = override
	r := c.modelSetReply(a.Entry, model)
	r.Edit = true
	c.send(r)
	return nil
}

// unknownModelForm returns the reply for /model arguments that have no known form.
func unknownModelForm(args string) Reply {
	return Reply{Text: fmt.Sprintf("⚠️ **Unknown form: %s**\nSend /model, /model <provider>, /model <provider>:<model>, or /model clear.",
		EscapeMarkdown(args))}
}
