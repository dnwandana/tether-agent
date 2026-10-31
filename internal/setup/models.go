package setup

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// modelRowsMax is the maximum number of rows that the wizard prints for one list.
const modelRowsMax = 20

// resolvedEntry resolves the answers into a provider entry. The key of the answers is the
// resolved key, so the resolution gets no env lookup. It does not check Unusable, because
// the model list call does not need a model.
func (w *Wizard) resolvedEntry(name string, a providerAnswers) (config.Provider, error) {
	pc := config.Provider{Preset: a.preset, APIKey: a.key, Model: a.model}
	if a.preset == "" {
		pc.Format, pc.BaseURL = a.format, a.baseURL
	}
	return config.ResolvePreset(name, pc, nil)
}

// askModel gets the model list of the entry and asks for the model. When the list call
// fails, it prints the cause and asks for the model ID.
func (w *Wizard) askModel(ctx context.Context, name string, a providerAnswers) (string, error) {
	pc, err := w.resolvedEntry(name, a)
	if err != nil {
		return "", err
	}
	list, err := provider.ListModels(ctx, name, pc, w.hc())
	if err != nil {
		w.say("The model list is not available: %s.", provider.ModelListCause(err))
		return w.askRequired("Model", a.model)
	}
	return w.pickModel(ctx, name, pc, list, a.model)
}

// pickModel prints the list and reads the answer. def is the current model, or "".
func (w *Wizard) pickModel(ctx context.Context, name string, pc config.Provider, list provider.ModelList, def string) (string, error) {
	who := pc.BaseURL
	if pc.Preset != "" {
		pre, _ := config.PresetByID(pc.Preset)
		who = pre.Group
	}
	shown := w.printModels(who, list)
	all := list.All()
	for {
		v := w.ask("Model", def)
		if v == "" {
			if w.eof {
				return "", errInputEnded
			}
			w.say("A value is necessary.")
			continue
		}
		// The current model can be absent from the list.
		if def != "" && v == def {
			return def, nil
		}
		if n, err := strconv.Atoi(v); err == nil {
			if n >= 1 && n <= len(shown) {
				return shown[n-1], nil
			}
			if w.eof {
				return "", errInputEnded
			}
			w.say("Enter a number from 1 to %d.", len(shown))
			continue
		}
		// The full ID of a search result is not in the full list, but it is in the printed list.
		if slices.Contains(all, v) || slices.Contains(shown, v) {
			return v, nil
		}
		if matches := w.matchModels(ctx, name, pc, list, v); len(matches) > 0 {
			shown = w.printModels(who, provider.ModelList{Groups: []provider.ModelGroup{{Models: matches}}, Search: list.Search})
			continue
		}
		yes, err := w.confirm(fmt.Sprintf("No model matches \"%s\". Use \"%s\" as the model ID?", v, v))
		if err != nil {
			return "", err
		}
		if yes {
			return v, nil
		}
	}
}

// printModels prints the list with numbers and returns the printed IDs in number order.
// It prints at most modelRowsMax rows, so a number always points to a printed row.
func (w *Wizard) printModels(who string, list provider.ModelList) []string {
	var shown []string
	total := 0
	for i, g := range list.Groups {
		total += len(g.Models)
		rows := g.Models[:min(len(g.Models), modelRowsMax-len(shown))]
		if len(rows) == 0 {
			continue
		}
		switch {
		case i > 0:
			w.say("%s:", g.Title)
		case g.Title == "":
			w.say("Models of %s:", who)
		case list.ToolsOnly:
			w.say("Models of %s. %s, with tool support:", who, g.Title)
		default:
			w.say("Models of %s. %s:", who, g.Title)
		}
		for _, id := range rows {
			shown = append(shown, id)
			w.say("%4d) %s", len(shown), id)
		}
	}
	if total > len(shown) {
		w.say("… and %d more. Type a text to filter the list.", total-len(shown))
	}
	if list.Search {
		w.say("Type a number, a text to search, or a model ID.")
	} else {
		w.say("Type a number, a text to filter the list, or a model ID.")
	}
	return shown
}

// filterModels returns each ID of ids that contains text, without regard to case.
func filterModels(ids []string, text string) []string {
	text = strings.ToLower(text)
	var out []string
	for _, id := range ids {
		if strings.Contains(strings.ToLower(id), text) {
			out = append(out, id)
		}
	}
	return out
}

// matchModels returns the models that match text. An OpenRouter list asks the server,
// because the full catalog is on the server. Else it filters the full list.
func (w *Wizard) matchModels(ctx context.Context, name string, pc config.Provider, list provider.ModelList, text string) []string {
	if list.Search {
		// On ErrNoSearch or a failed call, the full list is the fallback.
		if ids, err := provider.SearchModels(ctx, name, pc, w.hc(), text); err == nil {
			return ids
		}
	}
	return filterModels(list.All(), text)
}
