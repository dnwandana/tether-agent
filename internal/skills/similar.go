package skills

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dnwandana/tether-agent/internal/textmatch"
)

// SimilarSkills lists the skills that are like a new skill. A skill is similar
// when it shares 2 or more name parts with name, or when the words of the
// two skills are similar. It checks the skills of dir (existing) and the
// skills in dir/.archive (archived). It skips the skill that has the name
// name. Each list has the skill with the most shared words first, then
// follows the name order.
func SimilarSkills(dir, name, description string) (existing, archived []string, err error) {
	parts := nameParts(name)
	words := skillWords(name, description)
	live, _, err := scanDir(dir, false)
	if err != nil {
		return nil, nil, err
	}
	existing = similarNames(live, name, parts, words)
	entries, err := os.ReadDir(filepath.Join(dir, ".archive"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	var old []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, perr := ParseSkillFile(filepath.Join(dir, ".archive", e.Name(), skillFile))
		if perr != nil {
			continue
		}
		old = append(old, s)
	}
	archived = similarNames(old, name, parts, words)
	return existing, archived, nil
}

// similarNames returns the names of the skills in all that are similar to the
// new skill, sorted by the count of shared words and then by name.
func similarNames(all []Skill, name string, parts, words map[string]bool) []string {
	type hit struct {
		name   string
		shared int
	}
	var hits []hit
	seen := map[string]bool{}
	for _, s := range all {
		if s.Name == name || seen[s.Name] {
			continue
		}
		shared, ok := textmatch.Similar(words, skillWords(s.Name, s.Description))
		if !ok {
			common := 0
			for p := range nameParts(s.Name) {
				if parts[p] {
					common++
				}
			}
			if common < 2 {
				continue
			}
		}
		seen[s.Name] = true
		hits = append(hits, hit{s.Name, shared})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].shared != hits[j].shared {
			return hits[i].shared > hits[j].shared
		}
		return hits[i].name < hits[j].name
	})
	var out []string
	for _, h := range hits {
		out = append(out, h.name)
	}
	return out
}

// nameParts returns the parts of a skill name: lower case, split on "-" and
// "_", with no empty part.
func nameParts(name string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '-' || r == '_' }) {
		out[p] = true
	}
	return out
}

// skillWords returns the words of a skill from its name and its description.
func skillWords(name, description string) map[string]bool {
	spaced := strings.NewReplacer("-", " ", "_", " ").Replace(name)
	return textmatch.Words(spaced + " " + description)
}
