package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/security"
)

// The limits of skill_manage writes (Hermes tools/skill_manager_tool.py).
const (
	maxNameLen        = 64
	maxSkillMDChars   = 100_000
	maxSupportFileLen = 1 << 20
)

// writableSubdirs holds the skill subfolders that WriteFile can write.
var writableSubdirs = []string{"references", "scripts"}

// validNameRe is the Hermes VALID_NAME_RE for a skill name.
var validNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Manager creates, changes, and deletes the agent-written skills under Dir.
// When Guard is true, each write scans the full new file set of the skill
// first and applies the agent-created policy. A refused write leaves the disk
// unchanged.
type Manager struct {
	// Dir is the skills folder in the data dir.
	Dir string
	// Guard turns on the skill guard (skills.guard).
	Guard bool

	mu sync.Mutex
}

// Result is the result of a write.
type Result struct {
	// Path is the file that the write changed.
	Path string
	// Verdict is the guard verdict. It is empty when the guard is off.
	Verdict Verdict
	// Findings holds the guard findings of a saved skill, for example the
	// findings of a caution verdict.
	Findings []Finding
}

// Create writes a new skill Dir/<name>/SKILL.md. The frontmatter name must
// be name.
func (m *Manager) Create(name, content string) (Result, error) {
	if err := validName(name); err != nil {
		return Result{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok, err := m.find(name); err != nil {
		return Result{}, err
	} else if ok {
		return Result{}, fmt.Errorf("a skill named %q already exists at %s; use patch to change it", name, s.Path)
	}
	dir := filepath.Join(m.Dir, name)
	if _, err := os.Lstat(dir); err == nil {
		return Result{}, fmt.Errorf("the folder %s already exists; choose another name", dir)
	}
	if err := validSkillMD(name, content); err != nil {
		return Result{}, err
	}
	res, err := m.guard(map[string]string{skillFile: content})
	if err != nil {
		return Result{}, err
	}
	target := filepath.Join(dir, skillFile)
	if err := m.checkTarget(target); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("cannot create the skills folder: %w", err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("cannot create the skill folder: %w", err)
	}
	if err := writeFileAtomic(target, []byte(content)); err != nil {
		os.RemoveAll(dir)
		return Result{}, fmt.Errorf("cannot write %s: %w", target, err)
	}
	res.Path = target
	return res, nil
}

// Patch replaces old with new in the SKILL.md of the skill name. old must
// match exactly once.
func (m *Manager) Patch(name, old, new string) (Result, error) {
	return m.PatchFile(name, skillFile, old, new)
}

// PatchFile replaces old with new in the file rel of the skill name. rel is
// SKILL.md or a file under references/ or scripts/. old must match exactly
// once.
func (m *Manager) PatchFile(name, rel, old, new string) (Result, error) {
	if old == "" {
		return Result{}, errors.New("old_string is required for patch")
	}
	rel, err := cleanRel(rel, true)
	if err != nil {
		return Result{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.mustFind(name)
	if err != nil {
		return Result{}, err
	}
	target := filepath.Join(s.Dir, filepath.FromSlash(rel))
	if err := m.checkTarget(target); err != nil {
		return Result{}, err
	}
	raw, err := os.ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, fmt.Errorf("skill %s has no file %s", name, rel)
	}
	if err != nil {
		return Result{}, fmt.Errorf("cannot read %s: %w", target, err)
	}
	content := string(raw)
	switch n := strings.Count(content, old); n {
	case 0:
		return Result{}, fmt.Errorf("old_string not found in %s of skill %s; the match is exact, so copy the text from skill_view", rel, name)
	case 1:
	default:
		return Result{}, fmt.Errorf("old_string matches %d times in %s of skill %s; add more context so that it matches once", n, rel, name)
	}
	content = strings.Replace(content, old, new, 1)
	if rel == skillFile {
		if err := validSkillMD(name, content); err != nil {
			return Result{}, fmt.Errorf("the patch breaks SKILL.md: %w", err)
		}
	} else if len(content) > maxSupportFileLen {
		return Result{}, fmt.Errorf("%s would be %d bytes; the limit is %d", rel, len(content), maxSupportFileLen)
	}
	return m.save(s, rel, target, content)
}

// WriteFile writes the file rel of the skill name. rel must be under
// references/ or scripts/.
func (m *Manager) WriteFile(name, rel, content string) (Result, error) {
	rel, err := cleanRel(rel, false)
	if err != nil {
		return Result{}, err
	}
	if len(content) > maxSupportFileLen {
		return Result{}, fmt.Errorf("the file is %d bytes; the limit is %d", len(content), maxSupportFileLen)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.mustFind(name)
	if err != nil {
		return Result{}, err
	}
	target := filepath.Join(s.Dir, filepath.FromSlash(rel))
	if err := m.checkTarget(target); err != nil {
		return Result{}, err
	}
	return m.save(s, rel, target, content)
}

// Delete removes the folder of the skill name. It refuses a folder that
// holds other skills.
func (m *Manager) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.mustFind(name)
	if err != nil {
		return err
	}
	if err := m.checkTarget(filepath.Join(s.Dir, skillFile)); err != nil {
		return err
	}
	if real := realOrSelf(s.Dir); real == m.realDir() || !within(m.realDir(), real) {
		return fmt.Errorf("refused: the folder of skill %s is not in the skills folder", name)
	}
	nested, _, err := scanDir(s.Dir, false)
	if err != nil {
		return err
	}
	var others []string
	for _, n := range nested {
		if n.Dir != s.Dir {
			others = append(others, n.Name)
		}
	}
	if len(others) > 0 {
		return fmt.Errorf("the folder of skill %s also holds the skills %s; delete those first", name, strings.Join(others, ", "))
	}
	if err := os.RemoveAll(s.Dir); err != nil {
		return fmt.Errorf("cannot delete skill %s: %w", name, err)
	}
	return nil
}

// save scans the files of skill s with rel set to content, and writes the
// file when the guard allows it.
func (m *Manager) save(s Skill, rel, target, content string) (Result, error) {
	var res Result
	if m.Guard {
		files, err := skillFiles(s.Dir)
		if err != nil {
			return Result{}, err
		}
		files[rel] = content
		if res, err = m.guard(files); err != nil {
			return Result{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Result{}, fmt.Errorf("cannot create the folder of %s: %w", rel, err)
	}
	if err := writeFileAtomic(target, []byte(content)); err != nil {
		return Result{}, fmt.Errorf("cannot write %s: %w", target, err)
	}
	res.Path = target
	return res, nil
}

// guard scans files when the guard is on. It returns an error with the
// findings when the agent-created policy refuses the skill.
func (m *Manager) guard(files map[string]string) (Result, error) {
	if !m.Guard {
		return Result{}, nil
	}
	r := Scan(files)
	if Policy(TrustAgentCreated, r.Verdict) != ActionAllow {
		return Result{}, fmt.Errorf("the skill guard refused the write. Remove the flagged content and try again.\n%s", FormatFindings(r))
	}
	return Result{Verdict: r.Verdict, Findings: r.Findings}, nil
}

// find returns the skill name in Dir.
func (m *Manager) find(name string) (Skill, bool, error) {
	all, _, err := scanDir(m.Dir, false)
	if err != nil {
		return Skill{}, false, err
	}
	for _, s := range all {
		if s.Name == name {
			return s, true, nil
		}
	}
	return Skill{}, false, nil
}

// mustFind returns the skill name in Dir, or an error when it does not exist.
func (m *Manager) mustFind(name string) (Skill, error) {
	if err := validName(name); err != nil {
		return Skill{}, err
	}
	s, ok, err := m.find(name)
	if err != nil {
		return Skill{}, err
	}
	if !ok {
		return Skill{}, fmt.Errorf("no skill named %q in %s; use create first", name, m.Dir)
	}
	return s, nil
}

// checkTarget refuses a write to target when target is not in Dir, when a
// part of its path in Dir is a symlink, or when it is a protected path.
func (m *Manager) checkTarget(target string) error {
	rel, err := filepath.Rel(m.Dir, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("refused: %s is not in the skills folder %s", target, m.Dir)
	}
	cur := m.Dir
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return fmt.Errorf("cannot check %s: %w", cur, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refused: %s is a symlink; skill_manage does not write through symlinks", cur)
		}
	}
	home, _ := os.UserHomeDir()
	return security.CheckWritable(target, security.ProtectedPaths(filepath.Dir(m.Dir), home), true)
}

// realDir returns Dir with its symlinks resolved.
func (m *Manager) realDir() string { return realOrSelf(m.Dir) }

// realOrSelf returns p with its symlinks resolved, or p on an error.
func realOrSelf(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// skillFiles reads the text files of the skill folder dir that the guard
// scans, keyed by slash path. It does not follow symlinks.
func skillFiles(dir string) (map[string]string, error) {
	files := map[string]string{}
	total := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !isScannable(rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		total += len(b)
		if total > maxScanBytes {
			return errTooLargeToScan
		}
		files[rel] = string(b)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cannot read the skill files for the guard: %w", err)
	}
	return files, nil
}

// validName checks a skill name: lowercase letters, digits, ".", "_", and
// "-", with a letter or digit first, at most 64 characters.
func validName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("the skill name has more than %d characters", maxNameLen)
	}
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("invalid skill name %q: use lowercase letters, digits, '.', '_', and '-', and start with a letter or a digit", name)
	}
	return nil
}

// validSkillMD checks SKILL.md content: valid frontmatter, a name equal to
// the folder name, a description, a body, and the size limit.
func validSkillMD(name, content string) error {
	if n := utf8.RuneCountInString(content); n > maxSkillMDChars {
		return fmt.Errorf("SKILL.md has %d characters; the limit is %d. Move details into references/ files", n, maxSkillMDChars)
	}
	s, err := parseSkill([]byte(content))
	if err != nil {
		return err
	}
	if s.Name != name {
		return fmt.Errorf("the frontmatter name must match the folder name: the name is %q, the folder is %q", s.Name, name)
	}
	if utf8.RuneCountInString(s.Description) > indexDescriptionCap {
		return fmt.Errorf("the description has more than %d characters", indexDescriptionCap)
	}
	if strings.TrimSpace(s.Body) == "" {
		return errors.New("SKILL.md must have a body with the instructions after the frontmatter")
	}
	return nil
}

// cleanRel checks the path of a file in a skill folder and returns it as a
// clean slash path. It must be under references/ or scripts/. When
// allowSkillMD is true, SKILL.md is also accepted.
func cleanRel(rel string, allowSkillMD bool) (string, error) {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if rel == "" {
		return "", errors.New("file is required")
	}
	if strings.HasPrefix(rel, "/") || strings.Contains("/"+rel+"/", "/../") {
		return "", fmt.Errorf("file %q must be a relative path in the skill folder without \"..\"", rel)
	}
	rel = path.Clean(rel)
	if allowSkillMD && rel == skillFile {
		return rel, nil
	}
	for _, sub := range writableSubdirs {
		if strings.HasPrefix(rel, sub+"/") {
			return rel, nil
		}
	}
	return "", fmt.Errorf("file %q must be under references/ or scripts/", rel)
}
