package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// maxSearchDepth is the number of folder levels below the search root that
// FindSkills reads.
const maxSearchDepth = 5

// searchDotDirs are the folders whose names start with "." that FindSkills
// reads. openai/skills keeps its skills in such folders.
var searchDotDirs = map[string]bool{
	".curated":      true,
	".experimental": true,
	".system":       true,
}

// FoundSkill is one skill that FindSkills found in a source tree.
type FoundSkill struct {
	Name        string
	Description string
	// RelDir is the slash path of the skill folder relative to the search
	// root. It is "." for a skill at the root.
	RelDir string
}

// FindSkills returns the skills under root, sorted by name and then by
// folder. It reads at most five folder levels below root. It skips ".git",
// "node_modules", and dot folders other than ".curated", ".experimental", and
// ".system". It does not go down into a folder that has a SKILL.md, and it
// does not follow symlinks. A SKILL.md that does not parse becomes a warning.
func FindSkills(root string) ([]FoundSkill, []Warning, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, nil, fmt.Errorf("search skills in %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("search skills in %s: not a folder", root)
	}
	var found []FoundSkill
	var warns []Warning
	var walk func(dir, rel string, depth int) error
	walk = func(dir, rel string, depth int) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if depth == 0 {
				return fmt.Errorf("search skills in %s: %w", root, err)
			}
			warns = append(warns, Warning{Path: dir, Text: "cannot read: " + err.Error()})
			return nil
		}
		for _, e := range entries {
			if e.Name() != skillFile {
				continue
			}
			p := filepath.Join(dir, skillFile)
			if !e.Type().IsRegular() {
				warns = append(warns, Warning{Path: p, Text: "skill file is not a regular file"})
				return nil
			}
			s, perr := ParseSkillFile(p)
			if perr != nil {
				warns = append(warns, Warning{Path: p, Text: perr.Error()})
				return nil
			}
			found = append(found, FoundSkill{Name: s.Name, Description: s.Description, RelDir: rel})
			return nil
		}
		if depth >= maxSearchDepth {
			return nil
		}
		for _, e := range entries {
			// A symlink to a folder is not a folder here, so the walk does
			// not follow it.
			if !e.IsDir() || skipSearchDir(e.Name()) {
				continue
			}
			if err := walk(filepath.Join(dir, e.Name()), path.Join(rel, e.Name()), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, ".", 0); err != nil {
		return nil, nil, err
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Name != found[j].Name {
			return found[i].Name < found[j].Name
		}
		return found[i].RelDir < found[j].RelDir
	})
	return found, warns, nil
}

// skipSearchDir tells if FindSkills must not go down into a folder.
func skipSearchDir(name string) bool {
	if name == ".git" || name == "node_modules" {
		return true
	}
	return strings.HasPrefix(name, ".") && !searchDotDirs[name]
}

// ManySkillsError tells that the search found more than one skill and that
// the caller must select one by name.
type ManySkillsError struct {
	Found []FoundSkill
	// Source is the source to give again with --skill. The installer sets
	// it; empty means that it is not known.
	Source string
}

func (e *ManySkillsError) Error() string {
	return fmt.Sprintf("the repository has %d skills. Select one with --skill", len(e.Found))
}

// SelectSkill returns the skill to install from found. With a name, it
// returns the one skill with that frontmatter name. Without a name, it
// returns the only skill, or a *ManySkillsError when there are more.
func SelectSkill(found []FoundSkill, name string) (FoundSkill, error) {
	if name == "" {
		switch len(found) {
		case 0:
			return FoundSkill{}, errors.New("no SKILL.md found")
		case 1:
			return found[0], nil
		default:
			return FoundSkill{}, &ManySkillsError{Found: found}
		}
	}
	var matches []FoundSkill
	for _, f := range found {
		if f.Name == name {
			matches = append(matches, f)
		}
	}
	switch len(matches) {
	case 0:
		names := make([]string, 0, len(found))
		for _, f := range found {
			names = append(names, f.Name)
		}
		return FoundSkill{}, fmt.Errorf("no skill named %q; the skills are: %s", name, strings.Join(names, ", "))
	case 1:
		return matches[0], nil
	default:
		dirs := make([]string, 0, len(matches))
		for _, m := range matches {
			dirs = append(dirs, m.RelDir)
		}
		return FoundSkill{}, fmt.Errorf("two folders have the skill %q: %s", name, strings.Join(dirs, ", "))
	}
}

// errNestedFound stops the walk of NestedSkill at the first match.
var errNestedFound = errors.New("nested skill found")

// NestedSkill returns the slash path, relative to dir, of the first SKILL.md
// below dir. It does not count dir/SKILL.md. The loader reads SKILL.md at
// every level, so a nested file would load as a second skill.
func NestedSkill(dir string) (rel string, ok bool, err error) {
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != skillFile || filepath.Dir(p) == filepath.Clean(dir) {
			return nil
		}
		r, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(r)
		return errNestedFound
	})
	if errors.Is(walkErr, errNestedFound) {
		return rel, true, nil
	}
	if walkErr != nil {
		return "", false, fmt.Errorf("check for nested skills in %s: %w", dir, walkErr)
	}
	return "", false, nil
}
