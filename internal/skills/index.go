package skills

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// indexJSON is the curated index. The binary does not download an index, so
// a new list comes only with a new release.
//
//go:embed index/available.json
var indexJSON []byte

// indexVersion is the only index format that LoadIndex reads.
const indexVersion = 1

// IndexEntry is one skill in the curated index. The entry has no trust
// field, because the trust level comes from the repo of the source.
type IndexEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Path        string `json:"path"`
	Ref         string `json:"ref"`
}

// LoadIndex parses the embedded curated index and returns its entries
// sorted by name.
func LoadIndex() ([]IndexEntry, error) {
	var file struct {
		Version int          `json:"version"`
		Skills  []IndexEntry `json:"skills"`
	}
	if err := json.Unmarshal(indexJSON, &file); err != nil {
		return nil, fmt.Errorf("read the skill index: %w", err)
	}
	if file.Version != indexVersion {
		return nil, fmt.Errorf("read the skill index: version %d is not supported (want %d)", file.Version, indexVersion)
	}
	entries := file.Skills
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// LookupIndex returns the index entry with the given name. The bool is
// false when the index has no such entry.
func LookupIndex(name string) (IndexEntry, bool, error) {
	entries, err := LoadIndex()
	if err != nil {
		return IndexEntry{}, false, err
	}
	for _, e := range entries {
		if e.Name == name {
			return e, true, nil
		}
	}
	return IndexEntry{}, false, nil
}

// FilterIndex returns the entries whose name or description contains word,
// with no regard to case. An empty word returns all entries.
func FilterIndex(entries []IndexEntry, word string) []IndexEntry {
	word = strings.ToLower(word)
	var out []IndexEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), word) ||
			strings.Contains(strings.ToLower(e.Description), word) {
			out = append(out, e)
		}
	}
	return out
}
