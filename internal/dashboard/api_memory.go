package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// MemoryEntry is one entry of memory.md or user_profile.md. Text is redacted. For a blocked
// entry, Text is the blocked line and Blocked is true.
type MemoryEntry struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Blocked bool   `json:"blocked"`
}

// TopicRow is one note of the topic list.
type TopicRow struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Modified time.Time `json:"modified"`
	Entries  int       `json:"entries"`
	// Held is true for a note of held writes. The page shows a label.
	Held bool `json:"held"`
}

// MemoryView is the Memory page. Used and Limit are runes, separators included. Result is the
// tool result after a POST, else "".
type MemoryView struct {
	Memory      []MemoryEntry `json:"memory"`
	User        []MemoryEntry `json:"user"`
	MemoryUsed  int           `json:"memory_used"`
	MemoryLimit int           `json:"memory_limit"`
	UserUsed    int           `json:"user_used"`
	UserLimit   int           `json:"user_limit"`
	Rule        []MemoryEntry `json:"rule"`
	RuleUsed    int           `json:"rule_used"`
	RuleLimit   int           `json:"rule_limit"`
	EntryLimit  int           `json:"entry_limit"`
	Topics      []TopicRow    `json:"topics"`
	Result      string        `json:"result"`
	// EditIDs holds the ids of the history rows of the last change. It is never null.
	EditIDs []int64 `json:"edit_ids"`
}

// TopicView is the text of one note.
type TopicView struct {
	Topic string `json:"topic"`
	Text  string `json:"text"`
}

// MemoryHit is one search hit. Entry and Match are redacted, or both are the blocked line.
type MemoryHit struct {
	File    string `json:"file"`
	Entry   string `json:"entry"`
	Match   string `json:"match"`
	Blocked bool   `json:"blocked"`
}

// MemorySearchView is the result of a memory search.
type MemorySearchView struct {
	Query string      `json:"query"`
	Hits  []MemoryHit `json:"hits"`
}

// MemoryEdit is the body of POST /api/memory. ID names an entry of Target; it wins over OldText.
type MemoryEdit struct {
	Action  string `json:"action"`
	Target  string `json:"target"`
	Content string `json:"content"`
	ID      string `json:"id"`
	OldText string `json:"old_text"`
	Topic   string `json:"topic"`
}

// MemoryUndo is the body of POST /api/memory/undo.
type MemoryUndo struct {
	EditID int64 `json:"edit_id"`
}

// MemoryAPI serves the Memory page.
type MemoryAPI struct {
	st     *memory.Store
	redact func(string) string
}

// NewMemoryAPI returns the memory routes over st. redact hides secrets in each text that
// goes to the browser; nil changes nothing.
func NewMemoryAPI(st *memory.Store, redact func(string) string) *MemoryAPI {
	return &MemoryAPI{st: st, redact: redact}
}

// Register adds the memory routes to mux.
func (a *MemoryAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory", a.view)
	mux.HandleFunc("GET /api/memory/topics/{topic}", a.topic)
	mux.HandleFunc("GET /api/memory/search", a.search)
	mux.HandleFunc("POST /api/memory", a.edit)
	mux.HandleFunc("POST /api/memory/undo", a.undo)
}

// hide returns text with the secrets removed.
func (a *MemoryAPI) hide(text string) string {
	if a.redact == nil {
		return text
	}
	return a.redact(text)
}

// memoryEntries returns the entries of target for the page. A blocked entry shows as its
// blocked line, and no other text of it leaves the server.
func (a *MemoryAPI) memoryEntries(raw []string) []MemoryEntry {
	limit := a.st.EntryLimit()
	out := make([]MemoryEntry, 0, len(raw))
	for _, e := range raw {
		row := MemoryEntry{ID: memory.EntryID(e)}
		if line, blocked := tools.MemoryBlockedLineWithin(e, limit); blocked {
			row.Text, row.Blocked = line, true
		} else {
			row.Text = a.hide(e)
		}
		out = append(out, row)
	}
	return out
}

// buildView reads the store and returns the Memory page. It returns the status and the
// text of the error when a read fails.
func (a *MemoryAPI) buildView(result string, editIDs []int64) (MemoryView, int, string) {
	mem, err := a.st.ReadEntries(memory.TargetMemory)
	if err != nil {
		return MemoryView{}, http.StatusInternalServerError, a.hide(err.Error())
	}
	user, err := a.st.ReadEntries(memory.TargetUser)
	if err != nil {
		return MemoryView{}, http.StatusInternalServerError, a.hide(err.Error())
	}
	rule, err := a.st.ReadEntries(memory.TargetRule)
	if err != nil {
		return MemoryView{}, http.StatusInternalServerError, a.hide(err.Error())
	}
	infos, err := a.st.TopicInfos()
	if err != nil {
		return MemoryView{}, http.StatusInternalServerError, a.hide(err.Error())
	}
	if editIDs == nil {
		editIDs = []int64{}
	}
	v := MemoryView{
		Memory:      a.memoryEntries(mem),
		User:        a.memoryEntries(user),
		MemoryUsed:  utf8.RuneCountInString(strings.Join(mem, memory.Separator)),
		MemoryLimit: a.st.Limit(memory.TargetMemory),
		UserUsed:    utf8.RuneCountInString(strings.Join(user, memory.Separator)),
		UserLimit:   a.st.Limit(memory.TargetUser),
		Rule:        a.memoryEntries(rule),
		RuleUsed:    utf8.RuneCountInString(strings.Join(rule, memory.Separator)),
		RuleLimit:   a.st.Limit(memory.TargetRule),
		EntryLimit:  a.st.EntryLimit(),
		EditIDs:     editIDs,
		Topics:      make([]TopicRow, 0, len(infos)),
		Result:      result,
	}
	for _, t := range infos {
		v.Topics = append(v.Topics, TopicRow{Name: t.Name, Type: t.Type, Modified: t.Modified, Entries: t.Entries, Held: t.Held})
	}
	return v, 0, ""
}

func (a *MemoryAPI) view(w http.ResponseWriter, _ *http.Request) {
	v, status, msg := a.buildView("", nil)
	if status != 0 {
		memoryErr(w, status, msg)
		return
	}
	memoryJSON(w, http.StatusOK, v)
}

func (a *MemoryAPI) topic(w http.ResponseWriter, r *http.Request) {
	topic := r.PathValue("topic")
	text, err := a.st.Read(topic, 1, 0)
	if err != nil {
		memoryErr(w, http.StatusNotFound, a.hide(err.Error()))
		return
	}
	memoryJSON(w, http.StatusOK, TopicView{Topic: topic, Text: a.hide(text)})
}

// memoryMaxHits is the largest number of hits of one search.
const memoryMaxHits = 20

func (a *MemoryAPI) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	hits, err := a.st.SearchHits(q, memoryMaxHits)
	if err != nil {
		memoryErr(w, http.StatusBadRequest, a.hide(err.Error()))
		return
	}
	limit := a.st.EntryLimit()
	out := MemorySearchView{Query: q, Hits: make([]MemoryHit, 0, len(hits))}
	for _, h := range hits {
		row := MemoryHit{File: h.File}
		if line, blocked := tools.MemoryBlockedLineWithin(h.Entry, limit); blocked {
			row.Entry, row.Match, row.Blocked = line, line, true
		} else if red := a.hide(h.Entry); red != h.Entry {
			// The brackets of the snippet can split a secret, so the match is the whole
			// redacted entry.
			row.Entry, row.Match = red, red
		} else {
			row.Entry, row.Match = h.Entry, a.hide(h.Match)
		}
		out.Hits = append(out.Hits, row)
	}
	memoryJSON(w, http.StatusOK, out)
}

func (a *MemoryAPI) edit(w http.ResponseWriter, r *http.Request) {
	var e MemoryEdit
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, memoryBodyMax)).Decode(&e); err != nil {
		memoryErr(w, http.StatusBadRequest, "the request body is not valid JSON")
		return
	}
	switch e.Action {
	case "add", "replace", "remove":
	default:
		memoryErr(w, http.StatusBadRequest, a.hide(fmt.Sprintf("unknown action %q; use add, replace, or remove", e.Action)))
		return
	}
	if (e.Action == "add" || e.Action == "replace") && a.st.Threat != nil {
		if msg := a.st.Threat(e.Content); msg != "" {
			memoryErr(w, http.StatusBadRequest, a.hide(msg))
			return
		}
	}
	old, exact := e.OldText, false
	if e.ID != "" && e.Action != "add" {
		entries, err := a.st.ReadEntries(e.Target)
		if err != nil {
			memoryErr(w, http.StatusBadRequest, a.hide(err.Error()))
			return
		}
		raw, found := "", false
		for _, en := range entries {
			if memory.EntryID(en) == e.ID {
				raw, found = en, true
				break
			}
		}
		if !found {
			memoryErr(w, http.StatusNotFound, a.hide(fmt.Sprintf("no entry in %s has the ID %s", e.Target, e.ID)))
			return
		}
		old = raw
		exact = e.Action == "remove"
	}
	res, err := a.st.ApplyContext(r.Context(), memory.Op{Action: e.Action, Target: e.Target, Content: e.Content,
		OldText: old, Topic: e.Topic, Exact: exact, Source: memory.SourceOwner})
	if err != nil {
		memoryErr(w, http.StatusBadRequest, a.hide(err.Error()))
		return
	}
	v, status, msg := a.buildView(a.hide(res.Text), res.EditIDs)
	if status != 0 {
		memoryErr(w, status, msg)
		return
	}
	memoryJSON(w, http.StatusOK, v)
}

// undo reverses one history row of the owner page.
func (a *MemoryAPI) undo(w http.ResponseWriter, r *http.Request) {
	var b MemoryUndo
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, memoryBodyMax)).Decode(&b); err != nil {
		memoryErr(w, http.StatusBadRequest, "the request body is not valid JSON")
		return
	}
	if b.EditID <= 0 {
		memoryErr(w, http.StatusBadRequest, "edit_id is required")
		return
	}
	if a.st.History == nil {
		memoryErr(w, http.StatusNotFound, "The memory history is not available.")
		return
	}
	e, err := a.st.History.Get(r.Context(), b.EditID)
	if err != nil {
		memoryErr(w, http.StatusNotFound, fmt.Sprintf("No memory edit has the ID %d.", b.EditID))
		return
	}
	res, err := a.st.Undo(r.Context(), b.EditID, e.Stamp)
	switch {
	case errors.Is(err, memory.ErrChanged):
		memoryErr(w, http.StatusConflict, "The entry changed after this edit. Nothing was undone.")
		return
	case errors.Is(err, memory.ErrUndone):
		memoryErr(w, http.StatusConflict, "This edit is undone already.")
		return
	case errors.Is(err, memory.ErrThreat):
		memoryErr(w, http.StatusBadRequest, "The text matched the memory threat scan. Nothing was restored.")
		return
	case err != nil:
		memoryErr(w, http.StatusInternalServerError, a.hide(err.Error()))
		return
	}
	v, status, msg := a.buildView(a.hide(res.Text), res.EditIDs)
	if status != 0 {
		memoryErr(w, status, msg)
		return
	}
	memoryJSON(w, http.StatusOK, v)
}

// memoryBodyMax is the largest body of a memory edit in bytes.
const memoryBodyMax = 64 << 10

func memoryJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func memoryErr(w http.ResponseWriter, status int, msg string) {
	memoryJSON(w, status, map[string]string{"error": msg})
}

func init() {
	registerAPI(MemoryView{}, MemoryEntry{}, TopicRow{}, TopicView{}, MemoryHit{}, MemorySearchView{}, MemoryEdit{}, MemoryUndo{})
}
