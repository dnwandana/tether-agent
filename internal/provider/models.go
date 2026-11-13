package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// listTimeout is the time limit of one model list call.
const listTimeout = 10 * time.Second

// nonChatTexts are the parts of a model ID that mark a model that cannot
// chat. The list follows the noise patterns of Hermes Agent and the OpenAI
// model list. The user can still type the ID of a removed model.
var nonChatTexts = []string{
	"embed", "tts", "speech", "whisper", "transcribe", "dall-e", "image",
	"moderation", "realtime", "audio", "rerank",
}

// ErrNoChatModels marks a model list that has no chat model after the filter.
var ErrNoChatModels = errors.New("no chat models")

// isChatModel tells if the model id can chat. The match does not look at the
// case of the letters.
func isChatModel(id string) bool {
	id = strings.ToLower(id)
	for _, t := range nonChatTexts {
		if strings.Contains(id, t) {
			return false
		}
	}
	return true
}

// ModelListCause returns the short cause of a failed model list call. The
// cause goes into a reply, so it never carries a response body or a URL.
func ModelListCause(err error) string {
	if errors.Is(err, ErrNoChatModels) {
		return "no chat models"
	}
	return ShortCause(err)
}

// ModelGroup is a titled part of a model list. The generic list has one
// group with no title.
type ModelGroup struct {
	Title  string
	Models []string
}

// ModelList is the model list of a provider entry.
type ModelList struct {
	Groups []ModelGroup
	// At is the time of the list call.
	At time.Time
	// ToolsOnly tells that the server keeps only models with tool support.
	ToolsOnly bool
	// Search tells that SearchModels works for the entry.
	Search bool
}

// All returns the models of all groups in group order. The returned slice
// shares no memory with the Models slices of the groups.
func (l ModelList) All() []string {
	var all []string
	for _, g := range l.Groups {
		all = append(all, g.Models...)
	}
	return all
}

// Stamp returns 4 hex characters of the FNV-1a hash of the model IDs. A
// button that selects a model by position carries the stamp, so a changed
// list does not select a wrong model.
func (l ModelList) Stamp() string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.Join(l.All(), "\n")))
	return fmt.Sprintf("%08x", h.Sum32())[:4]
}

// modelItem is one item of a model list response.
type modelItem struct {
	ID        string `json:"id"`
	Created   int64  `json:"created"`
	CreatedAt string `json:"created_at"`
}

// unixTime returns the creation time of the item in Unix seconds, or 0 when
// the item has no valid time.
func (m modelItem) unixTime() int64 {
	if m.Created > 0 {
		return m.Created
	}
	if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
		return t.Unix()
	}
	return 0
}

// ListModels returns the model list of the entry p. It sends a GET to the
// models URL with the key and the quirks of the entry. It does not read
// p.Unusable, because the setup wizard calls it before the entry has a
// model. The newest model is first when the items have a time. The list
// has no model that cannot chat. When no model stays, the error is
// ErrNoChatModels. An OpenRouter entry on the preset URL gets two groups
// from the server sort. It uses a time limit of 10 seconds.
func ListModels(ctx context.Context, name string, p config.Provider, hc *http.Client) (ModelList, error) {
	return ListModelsWithin(ctx, name, p, hc, 0)
}

// ListModelsWithin works like ListModels, with the time limit timeout for the
// list call. A timeout of 0 or less means 10 seconds.
func ListModelsWithin(ctx context.Context, name string, p config.Provider, hc *http.Client, timeout time.Duration) (ModelList, error) {
	if timeout <= 0 {
		timeout = listTimeout
	}
	if isOpenRouter(p) {
		return listOpenRouter(ctx, name, p, hc, timeout)
	}
	e := newEndpoint(name, p, hc)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var raw json.RawMessage
	if err := getJSON(ctx, e.hc, name, listURL(e), listHeader(e), e.apiKey, &raw); err != nil {
		return ModelList{}, err
	}
	items, err := decodeModelItems(raw)
	if err != nil {
		return ModelList{}, fmt.Errorf("provider %s: %w", name, err)
	}
	slices.SortStableFunc(items, func(a, b modelItem) int {
		ta, tb := a.unixTime(), b.unixTime()
		switch {
		case ta > tb:
			return -1
		case ta < tb:
			return 1
		}
		return 0
	})
	ids := slices.DeleteFunc(uniqueIDs(items), func(id string) bool { return !isChatModel(id) })
	if len(ids) == 0 {
		return ModelList{}, fmt.Errorf("provider %s: %w", name, ErrNoChatModels)
	}
	return ModelList{Groups: []ModelGroup{{Models: ids}}, At: time.Now()}, nil
}

// listHeader returns the auth headers and the app headers of a list call.
// They are the same headers that the chat adapters send. No key sends no
// auth header.
func listHeader(e endpoint) http.Header {
	h := http.Header{}
	switch e.format {
	case "anthropic":
		h.Set("anthropic-version", anthropicVersion)
		switch {
		case e.apiKey == "":
		case e.has("bearer_auth"):
			h.Set("Authorization", "Bearer "+e.apiKey)
		default:
			h.Set("x-api-key", e.apiKey)
		}
	default:
		if e.apiKey != "" {
			h.Set("Authorization", "Bearer "+e.apiKey)
		}
	}
	if e.has("openrouter_headers") {
		h.Set("HTTP-Referer", openRouterReferer)
		h.Set("X-Title", openRouterTitle)
	}
	return h
}

// listURL returns the models URL of the endpoint. The anthropic format
// adds "/v1" when the base URL does not end in "/v1", as the chat adapter
// does.
func listURL(e endpoint) string {
	if e.format != "anthropic" {
		return joinURL(e.baseURL, "/models")
	}
	base := strings.TrimRight(e.baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/models?limit=1000"
	}
	return base + "/v1/models?limit=1000"
}

// decodeModelItems decodes a model list body. The body is an object with a
// "data" list, or a bare list.
func decodeModelItems(raw json.RawMessage) ([]modelItem, error) {
	var items []modelItem
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		return items, nil
	}
	var w struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	if len(w.Data) == 0 || string(w.Data) == "null" {
		return nil, errors.New("the response has no model list")
	}
	if err := json.Unmarshal(w.Data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// uniqueIDs returns the IDs of items in order. It skips an empty ID and a
// second copy of an ID.
func uniqueIDs(items []modelItem) []string {
	seen := make(map[string]bool, len(items))
	ids := make([]string, 0, len(items))
	for _, m := range items {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		ids = append(ids, m.ID)
	}
	return ids
}
