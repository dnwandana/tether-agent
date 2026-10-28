// Package skills loads SKILL.md skills, scans them with the skill guard, and
// installs the embedded bootstrap skills.
package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Trust is the trust level of a skill source. It selects the guard policy.
type Trust string

const (
	// TrustBuiltin marks the embedded bootstrap skills. The guard never scans them.
	TrustBuiltin Trust = "builtin"
	// TrustAgentCreated marks skills under the data dir. The agent writes them.
	TrustAgentCreated Trust = "agent-created"
	// TrustExternal marks skills from a read-only external folder (Hermes "trusted").
	TrustExternal Trust = "external"
	// TrustCommunity marks a skill that the owner installs from a source
	// outside TRUSTED_REPOS. The loader never gives this level. Only install
	// and update use it.
	TrustCommunity Trust = "community"
	// TrustTrusted marks a skill that the owner installs from a repo in
	// TRUSTED_REPOS. The loader never gives this level. Only install and
	// update use it.
	TrustTrusted Trust = "trusted"
)

// skillFile is the name of the file that defines a skill.
const skillFile = "SKILL.md"

// indexDescriptionCap is the maximum description length in the index, in
// runes. Hermes uses the same cap.
const indexDescriptionCap = 1024

// Skill is one parsed SKILL.md.
type Skill struct {
	Name        string
	Description string
	Body        string
	Dir         string
	Path        string
	External    bool
	RequiredEnv []string
	Trust       Trust
}

// Warning tells about a skill that the loader skipped or replaced.
type Warning struct {
	Path string
	Text string
}

// IndexLine is one line of the skills index in the system prompt.
type IndexLine struct{ Name, Description string }

// frontmatter holds the SKILL.md fields that Tether Agent reads.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	RequiredEnv any    `yaml:"required_environment_variables"`
}

// envNameRe matches a valid environment variable name.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Load scans dir and each external folder for SKILL.md files at every level.
// A skill in dir wins over an external skill with the same name. When guardOn
// is true, the guard scans each external skill with the TrustExternal policy
// and skips a blocked one. The guard does not scan the skills in dir. A
// skill that fails to parse or that the guard blocks becomes a Warning; it
// never stops the others. An external folder that cannot be read is also a
// Warning. A missing folder gives no skills and no error. An external path
// can start with "~/". The result is sorted by name.
func Load(dir string, external []string, guardOn bool) ([]Skill, []Warning, error) {
	var warns []Warning
	local, w, err := scanDir(dir, false)
	if err != nil {
		return nil, nil, err
	}
	warns = append(warns, w...)

	byName := map[string]Skill{}
	for _, s := range local {
		if prev, ok := byName[s.Name]; ok {
			warns = append(warns, Warning{Path: s.Path, Text: fmt.Sprintf("skill %q is also at %s; this copy is skipped", s.Name, prev.Path)})
			continue
		}
		byName[s.Name] = s
	}

	for _, ext := range external {
		found, w, err := scanDir(expandHome(ext), true)
		if err != nil {
			warns = append(warns, Warning{Path: ext, Text: "external skill folder is skipped: " + err.Error()})
			continue
		}
		warns = append(warns, w...)
		for _, s := range found {
			if prev, ok := byName[s.Name]; ok {
				warns = append(warns, Warning{Path: s.Path, Text: fmt.Sprintf("skill %q is also at %s; the external copy is skipped", s.Name, prev.Path)})
				continue
			}
			if guardOn {
				if w, ok := guardExternal(s); !ok {
					warns = append(warns, w)
					continue
				}
			}
			byName[s.Name] = s
		}
	}

	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warns, nil
}

// expandHome replaces a leading "~/" with the home folder of the user.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// guardExternal scans the folder of an external skill. It returns false and
// a Warning when the guard policy does not allow the skill.
func guardExternal(s Skill) (Warning, bool) {
	r, err := ScanDir(s.Dir)
	if err != nil {
		return Warning{Path: s.Path, Text: fmt.Sprintf("external skill %q is skipped: %v", s.Name, err)}, false
	}
	if Policy(s.Trust, r.Verdict) == ActionAllow {
		return Warning{}, true
	}
	var ids []string
	for _, f := range r.Findings {
		if f.Severity == SevCritical || f.Severity == SevHigh {
			ids = append(ids, fmt.Sprintf("%s (%s:%d)", f.PatternID, f.File, f.Line))
		}
	}
	return Warning{Path: s.Path, Text: fmt.Sprintf("external skill %q is skipped: the skill guard verdict is %s: %s",
		s.Name, r.Verdict, strings.Join(ids, ", "))}, false
}

// scanDir walks root and parses every SKILL.md. It skips folders whose names
// start with ".". The order of the result follows the walk order.
func scanDir(root string, external bool) ([]Skill, []Warning, error) {
	if root == "" {
		return nil, nil, nil
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	var skills []Skill
	var warns []Warning
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			warns = append(warns, Warning{Path: path, Text: "cannot read: " + err.Error()})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != skillFile {
			return nil
		}
		s, perr := ParseSkillFile(path)
		if perr != nil {
			warns = append(warns, Warning{Path: path, Text: perr.Error()})
			return nil
		}
		s.External = external
		s.Trust = trustFor(s.Name, external)
		skills = append(skills, s)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("scan skills folder %s: %w", root, err)
	}
	return skills, warns, nil
}

// trustFor gives the trust level of a skill from its name and source.
func trustFor(name string, external bool) Trust {
	if external {
		return TrustExternal
	}
	if isBootstrapName(name) {
		return TrustBuiltin
	}
	return TrustAgentCreated
}

// ParseSkillFile reads one SKILL.md. It parses the YAML frontmatter between
// the "---" lines and keeps the rest as the body. The name and the
// description must exist.
func ParseSkillFile(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("read skill: %w", err)
	}
	s, err := parseSkill(raw)
	if err != nil {
		return Skill{}, err
	}
	s.Path = path
	s.Dir = filepath.Dir(path)
	return s, nil
}

// parseSkill parses SKILL.md content. It does not set Path or Dir.
func parseSkill(raw []byte) (Skill, error) {
	fmText, body, ok := splitFrontmatter(raw)
	if !ok {
		return Skill{}, errors.New("skill has no frontmatter: the file must start with a \"---\" line and close it with a \"---\" line")
	}
	var fm frontmatter
	if err := yaml.Unmarshal(fmText, &fm); err != nil {
		return Skill{}, fmt.Errorf("skill frontmatter is not valid YAML: %w", err)
	}
	fm.Name = strings.TrimSpace(fm.Name)
	fm.Description = strings.TrimSpace(fm.Description)
	if fm.Name == "" {
		return Skill{}, errors.New("skill frontmatter has no name")
	}
	if fm.Description == "" {
		return Skill{}, fmt.Errorf("skill %q frontmatter has no description", fm.Name)
	}
	return Skill{
		Name:        fm.Name,
		Description: fm.Description,
		Body:        string(body),
		RequiredEnv: requiredEnv(fm.RequiredEnv),
	}, nil
}

// splitFrontmatter cuts the frontmatter from the body. It accepts LF and
// CRLF line ends.
func splitFrontmatter(raw []byte) (fm, body []byte, ok bool) {
	text := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	text = bytes.TrimPrefix(text, []byte("\xef\xbb\xbf"))
	if !bytes.HasPrefix(text, []byte("---\n")) {
		return nil, nil, false
	}
	rest := text[len("---\n"):]
	for off := 0; off <= len(rest); {
		end := bytes.IndexByte(rest[off:], '\n')
		var line []byte
		next := len(rest) + 1
		if end < 0 {
			line = rest[off:]
		} else {
			line = rest[off : off+end]
			next = off + end + 1
		}
		if strings.TrimRight(string(line), " \t") == "---" {
			if next > len(rest) {
				return rest[:off], nil, true
			}
			return rest[:off], rest[next:], true
		}
		off = next
	}
	return nil, nil, false
}

// requiredEnv reads required_environment_variables. Hermes accepts a list
// of names, a list of maps with a "name" or "env_var" key, or one map.
// Invalid and repeated names are dropped.
func requiredEnv(v any) []string {
	var items []any
	switch t := v.(type) {
	case []any:
		items = t
	case map[string]any:
		items = []any{t}
	case string:
		items = []any{t}
	default:
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		var name string
		switch e := it.(type) {
		case string:
			name = e
		case map[string]any:
			if n, ok := e["name"].(string); ok && n != "" {
				name = n
			} else if n, ok := e["env_var"].(string); ok {
				name = n
			}
		}
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || !envNameRe.MatchString(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// Index returns one line per skill for the system prompt. It keeps the full
// description up to 1,024 runes, because the "Use when" part tells the model
// when to load the skill.
func Index(skills []Skill) []IndexLine {
	out := make([]IndexLine, 0, len(skills))
	for _, s := range skills {
		desc := s.Description
		if r := []rune(desc); len(r) > indexDescriptionCap {
			desc = string(r[:indexDescriptionCap])
		}
		out = append(out, IndexLine{Name: s.Name, Description: desc})
	}
	return out
}
