package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// ErrNoSearch is the error of SearchModels for an entry that is not OpenRouter.
// The caller then filters the full list.
var ErrNoSearch = errors.New("provider: this entry has no model search")

const (
	openRouterTopPath     = "/models?sort=top-weekly&supported_parameters=tools&limit=30"
	openRouterAgenticPath = "/models?sort=agentic-high-to-low&supported_parameters=tools&limit=30"
	openRouterSearchQuery = "&sort=top-weekly&supported_parameters=tools&limit=30" // after "/models?q=<text>"
	openRouterTopKeep     = 10
	openRouterAgenticKeep = 5
)

// isOpenRouter reports whether p is the openrouter preset on the preset URL.
// An entry that moves the preset to another host uses the generic list.
func isOpenRouter(p config.Provider) bool {
	if p.Preset != "openrouter" {
		return false
	}
	pre, ok := config.PresetByID("openrouter")
	return ok && strings.TrimRight(p.BaseURL, "/") == strings.TrimRight(pre.BaseURL, "/")
}

// listOpenRouter gets the two OpenRouter groups. The groups keep the server
// order. When no group has models, the error is the error of the first
// group, else the error of the second group, else ErrNoChatModels.
func listOpenRouter(ctx context.Context, name string, p config.Provider, hc *http.Client) (ModelList, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	e := newEndpoint(name, p, hc)
	var top, agentic []string
	var topErr, agenticErr error
	// The two calls run at the same time, because a Telegram chat waits for the list.
	var wg sync.WaitGroup
	wg.Go(func() { top, topErr = openRouterIDs(ctx, e, openRouterTopPath) })
	wg.Go(func() { agentic, agenticErr = openRouterIDs(ctx, e, openRouterAgenticPath) })
	wg.Wait()

	top = top[:min(len(top), openRouterTopKeep)]
	var best []string
	for _, id := range agentic {
		if len(best) == openRouterAgenticKeep {
			break
		}
		if !slices.Contains(top, id) {
			best = append(best, id)
		}
	}
	list := ModelList{At: time.Now(), ToolsOnly: true, Search: true}
	if len(top) > 0 {
		list.Groups = append(list.Groups, ModelGroup{Title: "Most used this week", Models: top})
	}
	if len(best) > 0 {
		list.Groups = append(list.Groups, ModelGroup{Title: "Best for agents", Models: best})
	}
	if len(list.Groups) > 0 {
		return list, nil
	}
	switch {
	case topErr != nil:
		return ModelList{}, topErr
	case agenticErr != nil:
		return ModelList{}, agenticErr
	}
	return ModelList{}, fmt.Errorf("provider %s: %w", name, ErrNoChatModels)
}

// openRouterIDs sends one list call to path and returns the chat IDs in server order.
func openRouterIDs(ctx context.Context, e endpoint, path string) ([]string, error) {
	var raw json.RawMessage
	if err := getJSON(ctx, e.hc, e.name, joinURL(e.baseURL, path), listHeader(e), e.apiKey, &raw); err != nil {
		return nil, err
	}
	items, err := decodeModelItems(raw)
	if err != nil {
		return nil, fmt.Errorf("provider %s: %w", e.name, err)
	}
	return slices.DeleteFunc(uniqueIDs(items), func(id string) bool {
		return strings.HasSuffix(id, ":batch") || !isChatModel(id)
	}), nil
}

// SearchModels runs a server search on an OpenRouter entry. Other entries return
// ErrNoSearch, and the caller filters the full list.
func SearchModels(ctx context.Context, name string, p config.Provider, hc *http.Client, text string) ([]string, error) {
	if !isOpenRouter(p) {
		return nil, ErrNoSearch
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	return openRouterIDs(ctx, newEndpoint(name, p, hc), "/models?q="+url.QueryEscape(text)+openRouterSearchQuery)
}
