package gateway

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// modelPageSize is the number of model buttons on one page of a model list.
const modelPageSize = 8

// modelEntryMaxBytes is the maximum length in bytes of an entry name that gets model
// buttons. Telegram callback data has a limit of 64 bytes, and the longest action is
// "/model <entry> use <i> <stamp>". A longer name gets a numbered text list.
const modelEntryMaxBytes = 20

// modelActionMaxBytes is the maximum length in bytes of a button action. The callback data
// also holds "cmd:", a Telegram user ID of up to 16 digits, and ":". A longer action is cut,
// so the reply gives no button for it.
const modelActionMaxBytes = 64 - len("cmd:") - 16 - len(":")

// modelToolsLine is the text that tells that the list has only models with tool support.
const modelToolsLine = "Only models with tool support."

// modelUseAction returns the command that selects the model at the position index (from 1)
// of the list of entry. The stamp identifies the list, so a tap on an old list does not
// select a wrong model.
func modelUseAction(entry string, index int, stamp string) string {
	return fmt.Sprintf("/model %s use %d %s", entry, index, stamp)
}

// modelGroupLine returns the line that gives the positions of the two groups of l, and
// tells when l has only models with tool support. It returns "" when there is nothing to
// tell.
func modelGroupLine(l provider.ModelList) string {
	var line string
	if len(l.Groups) == 2 {
		a := len(l.Groups[0].Models)
		b := a + len(l.Groups[1].Models)
		line = fmt.Sprintf("%s (1 to %d), then %s (%d to %d).", l.Groups[0].Title, a, lowerFirst(l.Groups[1].Title), a+1, b)
	}
	if l.ToolsOnly {
		if line != "" {
			line += " "
		}
		line += modelToolsLine
	}
	return line
}

// lowerFirst returns s with its first rune in lower case.
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}

// modelListReply returns the model list of the provider entry for the session s. A channel
// with buttons gets page of the list as buttons, when the entry name is short enough for
// the button data. The CLI and a long entry name get a numbered text list. The note, when
// it is not empty, goes below the header. A failed list call gives the failure reply.
func (c *CommandCtx) modelListReply(ctx context.Context, s *sessions.Session, entry string, page int, note string) Reply {
	var pc config.Provider
	if c.Deps.Cfg != nil {
		pc = c.Deps.Cfg.Providers[entry]
	}
	list, err := c.Deps.Models.Get(ctx, entry, pc)
	if err != nil {
		return c.modelListFailure(entry, pc, err)
	}
	ids := list.All()
	inUse := pc.Model
	if s != nil {
		if m, ok := strings.CutPrefix(s.ModelOverride, entry+":"); ok {
			inUse = m
		}
	}

	count := fmt.Sprintf("%d models", len(ids))
	if len(ids) == 1 {
		count = "1 model"
	}
	lines := []string{fmt.Sprintf("🧠 **%s · %s**", EscapeMarkdown(entryLabel(entry, pc)), count)}
	if g := modelGroupLine(list); g != "" {
		lines = append(lines, g)
	}
	if note != "" {
		lines = append(lines, note)
	}
	lastRow := []Button{{Text: "↩ Providers", Data: "/model providers"}}
	if refresh := "/model " + entry + " refresh"; len(refresh) <= modelActionMaxBytes {
		lastRow = append(lastRow, Button{Text: "↻ Refresh", Data: refresh})
	}

	if !c.interactive() || len(entry) > modelEntryMaxBytes {
		for i, id := range ids {
			row := fmt.Sprintf("%4d) %s", i+1, EscapeMarkdown(id))
			if id == inUse {
				row += "  ✓"
			}
			lines = append(lines, row)
		}
		lines = append(lines, "Send /model "+EscapeMarkdown(entry)+":<model> to use a model.")
		r := Reply{Text: strings.Join(lines, "\n"), Edit: true}
		if c.interactive() {
			r.Buttons = [][]Button{lastRow}
		}
		return r
	}

	pages := max((len(ids)+modelPageSize-1)/modelPageSize, 1)
	page = min(max(page, 1), pages)
	first := (page - 1) * modelPageSize
	stamp := list.Stamp()
	var rows [][]Button
	for i := first; i < min(first+modelPageSize, len(ids)); i++ {
		if (i-first)%modelButtonsPerRow == 0 {
			rows = append(rows, nil)
		}
		label := buttonTitle(modelShortName(ids[i]))
		if ids[i] == inUse {
			label = "✓ " + label
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], Button{Text: label, Data: modelUseAction(entry, i+1, stamp)})
	}
	if pages > 1 {
		prev, next := Button{Text: noAction, Data: noAction}, Button{Text: noAction, Data: noAction}
		if page > 1 {
			prev = Button{Text: "◀ Prev", Data: fmt.Sprintf("/model %s %d", entry, page-1)}
		}
		if page < pages {
			next = Button{Text: "Next ▶", Data: fmt.Sprintf("/model %s %d", entry, page+1)}
		}
		rows = append(rows, []Button{prev, {Text: fmt.Sprintf("%d / %d", page, pages), Data: noAction}, next})
	}
	rows = append(rows, lastRow)
	return Reply{Text: strings.Join(lines, "\n"), Buttons: rows, Edit: true}
}

// modelShortName returns the part of the model ID after the last "/", for a button label.
// An ID that ends in "/" stays complete.
func modelShortName(id string) string {
	if short := id[strings.LastIndex(id, "/")+1:]; short != "" {
		return short
	}
	return id
}

// modelListFailure returns the reply for a failed model list call of the entry. It offers
// the model of the entry in config.yaml only when the entry has one.
func (c *CommandCtx) modelListFailure(entry string, pc config.Provider, err error) Reply {
	e := EscapeMarkdown(entry)
	use := "/model " + entry + " config"
	// A use action that does not fit in the button data gets the typed command, as in the CLI.
	tap := c.interactive() && len(use) <= modelActionMaxBytes
	end := "Tap the button to use the model in config.yaml. Send /model " + e + ":<model> to use another model."
	if !tap {
		end = "Send /model " + e + " config to use the model in config.yaml. Send /model " + e + ":<model> to use another model."
	}
	// An entry with no model in config.yaml has no model to offer.
	if pc.Model == "" {
		end = "Send /model " + e + ":<model> to use a model."
	}
	r := Reply{
		Text: fmt.Sprintf("⚠️ **No model list from %s**\n%s\n%s", e, EscapeMarkdown(provider.ModelListCause(err)), end),
		Edit: true,
	}
	if !c.interactive() {
		return r
	}
	if tap && pc.Model != "" {
		r.Buttons = append(r.Buttons, []Button{{Text: "Use " + pc.Model, Data: use}})
	}
	r.Buttons = append(r.Buttons, []Button{{Text: "↩ Providers", Data: "/model providers"}})
	return r
}

// unusableDetail returns the Unusable text of pc as one sentence for a reply: with no
// prefix "config: " and with one "." at the end. The text is not escaped.
func unusableDetail(pc config.Provider) string {
	d := strings.TrimPrefix(pc.Unusable, "config: ")
	return strings.TrimSuffix(d, ".") + "."
}

// unusableReply returns the reply for a /model form that names the entry, when pc is not
// usable. It tells the cause and the entry that still serves the session s.
func (c *CommandCtx) unusableReply(s *sessions.Session, entry string, pc config.Provider) Reply {
	r := Reply{
		Text: fmt.Sprintf("⚠️ **%s is not usable**\n%s\nThis session still uses %s.",
			EscapeMarkdown(entry), EscapeMarkdown(unusableDetail(pc)), EscapeMarkdown(c.servedEntry(s))),
		Edit: true,
	}
	if c.interactive() {
		r.Buttons = [][]Button{{{Text: "↩ Providers", Data: "/model providers"}}}
	}
	return r
}
