package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/skills"
)

// passthroughKey is the Env.Values key of the env names that skill_view
// registered in the session.
const passthroughKey = "env_passthrough"

// maxLinkedFiles limits the support files that skill_view lists.
const maxLinkedFiles = 50

// SkillCatalog finds the skills on disk. It reads the folders on each call,
// so a skill that the session created is visible at once.
type SkillCatalog struct {
	// Dir is the skills folder in the data dir.
	Dir string
	// External holds the read-only external skill folders.
	External []string
	// Guard turns on the skill guard (skills.guard).
	Guard bool
}

// Load returns the skills of the catalog, sorted by name.
func (c *SkillCatalog) Load() ([]skills.Skill, []skills.Warning, error) {
	return skills.Load(c.Dir, c.External, c.Guard)
}

// find returns the skill with the given name, or a model error that lists
// the skill names.
func (c *SkillCatalog) find(name string) (skills.Skill, error) {
	if c == nil {
		return skills.Skill{}, toolErr("skills are not available in this session")
	}
	all, _, err := c.Load()
	if err != nil {
		return skills.Skill{}, toolErr("cannot load the skills: %v", err)
	}
	names := make([]string, 0, len(all))
	for _, s := range all {
		if s.Name == name {
			return s, nil
		}
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return skills.Skill{}, toolErr("no skill named %q; no skills are installed", name)
	}
	return skills.Skill{}, toolErr("no skill named %q; the skills are: %s", name, strings.Join(names, ", "))
}

// passthroughSet holds the env names of one session.
type passthroughSet struct {
	mu    sync.Mutex
	names []string
}

// addPassthrough registers names for the session of env.
func addPassthrough(env *Env, names []string) {
	if env == nil || len(names) == 0 {
		return
	}
	valuesMu.Lock()
	if env.Values == nil {
		env.Values = map[string]any{}
	}
	set, ok := env.Values[passthroughKey].(*passthroughSet)
	if !ok {
		set = &passthroughSet{}
		env.Values[passthroughKey] = set
	}
	valuesMu.Unlock()
	set.mu.Lock()
	defer set.mu.Unlock()
	for _, n := range names {
		if !slices.Contains(set.names, n) {
			set.names = append(set.names, n)
		}
	}
}

// PassthroughFor returns the env names that the skill_view calls of the
// session of env registered, sorted. TerminalTool.Passthrough adds them to
// terminal.env_passthrough, so a child process of this session keeps them.
func PassthroughFor(env *Env) []string {
	if env == nil {
		return nil
	}
	valuesMu.Lock()
	set, ok := env.Values[passthroughKey].(*passthroughSet)
	valuesMu.Unlock()
	if !ok {
		return nil
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	out := slices.Clone(set.names)
	sort.Strings(out)
	return out
}

// SkillViewTool is the skill_view tool.
type SkillViewTool struct {
	Catalog *SkillCatalog
}

type skillViewArgs struct {
	Name string `json:"name"`
	File string `json:"file"`
}

// Name returns "skill_view".
func (SkillViewTool) Name() string { return "skill_view" }

// Toolset returns the skills toolset.
func (SkillViewTool) Toolset() string { return SetSkills }

// ParallelSafe returns true, because a view changes no file.
func (SkillViewTool) ParallelSafe() bool { return true }

// Schema returns the tool schema for the model.
func (SkillViewTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "skill_view",
		Description: "Load a skill: its SKILL.md instructions and the list of its support files. " +
			"Load a skill before you do a task that it covers. To read a support file, pass its path " +
			"in the skill folder as file, for example references/api.md. When the skill needs environment " +
			"variables, the terminal tool gets them in this session after the view.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "The skill name from the skills index or skills_list."},
    "file": {"type": "string", "description": "A support file in the skill folder, for example references/api.md. Omit it to load SKILL.md."}
  },
  "required": ["name"]
}`),
	}
}

// Run returns the skill body or one support file.
func (t SkillViewTool) Run(_ context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a skillViewArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return "", toolErr("name is required")
	}
	s, err := t.Catalog.find(a.Name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(a.File) != "" {
		return readSkillFile(s, a.File)
	}
	addPassthrough(env, s.RequiredEnv)

	var b strings.Builder
	fmt.Fprintf(&b, "skill %s: %s\nfolder: %s\n\n%s", s.Name, s.Description, s.Dir, strings.TrimLeft(s.Body, "\n"))
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	if files := linkedFiles(s.Dir); len(files) > 0 {
		fmt.Fprintf(&b, "\nSupport files (read one with skill_view and file):\n- %s\n", strings.Join(files, "\n- "))
	}
	if len(s.RequiredEnv) > 0 {
		fmt.Fprintf(&b, "\nRequired environment variables (the terminal tool gets them in this session): %s\n",
			strings.Join(s.RequiredEnv, ", "))
	}
	return b.String(), nil
}

// readSkillFile returns the content of file in the folder of s. The file
// must stay in the folder after ".." and symlinks resolve.
func readSkillFile(s skills.Skill, file string) (string, error) {
	const outside = "file must be inside the skill folder"
	p := filepath.Join(s.Dir, file)
	if !inside(s.Dir, p) {
		return "", toolErr("%s: %s", outside, file)
	}
	realDir, err := filepath.EvalSymlinks(s.Dir)
	if err != nil {
		return "", toolErr("cannot read the skill folder: %v", err)
	}
	real, err := filepath.EvalSymlinks(p)
	if errors.Is(err, fs.ErrNotExist) {
		files := linkedFiles(s.Dir)
		if len(files) == 0 {
			return "", toolErr("skill %s has no file %s and no support files", s.Name, file)
		}
		return "", toolErr("skill %s has no file %s; the files are: %s", s.Name, file, strings.Join(files, ", "))
	}
	if err != nil {
		return "", toolErr("cannot read %s: %v", file, err)
	}
	if !inside(realDir, real) {
		return "", toolErr("%s: %s", outside, file)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", toolErr("cannot read %s: %v", file, err)
	}
	if info.IsDir() {
		return "", toolErr("%s is a folder; the files are: %s", file, strings.Join(linkedFiles(s.Dir), ", "))
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return "", toolErr("cannot read %s: %v", file, err)
	}
	if looksBinary(data[:min(len(data), binarySniffLen)]) {
		return "", toolErr("%s is a binary file", file)
	}
	return string(data), nil
}

// inside reports whether path is in the folder dir. Both are cleaned first.
func inside(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// linkedFiles returns the support files of a skill folder: every file except
// SKILL.md and hidden files, as slash paths, sorted, at most maxLinkedFiles.
func linkedFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || path == filepath.Join(dir, "SKILL.md") {
			return nil
		}
		if len(out) == maxLinkedFiles {
			return fs.SkipAll
		}
		if rel, err := filepath.Rel(dir, path); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// SkillsListTool is the skills_list tool.
type SkillsListTool struct {
	Catalog *SkillCatalog
}

// Name returns "skills_list".
func (SkillsListTool) Name() string { return "skills_list" }

// Toolset returns the skills toolset.
func (SkillsListTool) Toolset() string { return SetSkills }

// ParallelSafe returns true, because a list changes nothing.
func (SkillsListTool) ParallelSafe() bool { return true }

// Schema returns the tool schema for the model.
func (SkillsListTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name:        "skills_list",
		Description: "List the installed skills with their descriptions. Load one with skill_view.",
		Parameters:  json.RawMessage(`{"type": "object", "properties": {}}`),
	}
}

// Run lists the skills, one "- name: description" line each.
func (t SkillsListTool) Run(_ context.Context, _ json.RawMessage, _ *Env) (string, error) {
	if t.Catalog == nil {
		return "", toolErr("skills are not available in this session")
	}
	all, _, err := t.Catalog.Load()
	if err != nil {
		return "", toolErr("cannot load the skills: %v", err)
	}
	if len(all) == 0 {
		return "no skills are installed", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d skills (load one with skill_view):\n", len(all))
	for _, l := range skills.Index(all) {
		fmt.Fprintf(&b, "- %s: %s\n", l.Name, oneLine(l.Description))
	}
	return b.String(), nil
}

// SkillManageTool is the skill_manage tool. It writes only
// under the skills folder of the data dir.
type SkillManageTool struct {
	Mgr     *skills.Manager
	Catalog *SkillCatalog
}

type skillManageArgs struct {
	Action    string  `json:"action"`
	Name      string  `json:"name"`
	Content   *string `json:"content"`
	OldString *string `json:"old_string"`
	NewString *string `json:"new_string"`
	File      string  `json:"file"`
}

// Name returns "skill_manage".
func (SkillManageTool) Name() string { return "skill_manage" }

// Toolset returns the skills toolset.
func (SkillManageTool) Toolset() string { return SetSkills }

// ParallelSafe returns false, because the tool changes files.
func (SkillManageTool) ParallelSafe() bool { return false }

// Schema returns the tool schema for the model.
func (SkillManageTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "skill_manage",
		Description: "Create, change, or delete your own skills. A skill is a SKILL.md file with YAML frontmatter " +
			"(name, description) and a markdown body with the instructions. Save a skill when you find a way to do " +
			"a task that is useful again.\n\n" +
			"ACTIONS:\n" +
			"- create: name and content (the full SKILL.md). The frontmatter name must be the same as name.\n" +
			"- patch: name, old_string, new_string. Replaces one exact match in SKILL.md, or in file when you give it.\n" +
			"- write_file: name, file, content. Writes a support file under references/ or scripts/.\n" +
			"- delete: name.\n\n" +
			"The skill guard scans each write. It refuses a skill that sends secrets out, for example curl with " +
			"$API_KEY. Put an API call in a script under ~/.tether/scripts/ (write_file cannot write there: ask the user, or use the terminal tool), and make " +
			"the skill run that script with the terminal tool. Skills from external folders are read-only.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["create", "patch", "write_file", "delete"], "description": "The action to do."},
    "name": {"type": "string", "description": "The skill name: lowercase letters, digits, '.', '_', and '-'."},
    "content": {"type": "string", "description": "create: the full SKILL.md. write_file: the file content."},
    "old_string": {"type": "string", "description": "patch: the exact text to replace. It must match once."},
    "new_string": {"type": "string", "description": "patch: the new text."},
    "file": {"type": "string", "description": "write_file and patch: a path under references/ or scripts/, for example references/api.md."}
  },
  "required": ["action", "name"]
}`),
	}
}

// Run does the action of the call.
func (t SkillManageTool) Run(_ context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a skillManageArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if t.Mgr == nil {
		return "", toolErr("skill_manage is not available in this session")
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return "", toolErr("name is required")
	}
	if err := t.refuseExternal(a.Name); err != nil {
		return "", err
	}
	var (
		res  skills.Result
		err  error
		verb string
	)
	switch a.Action {
	case "create":
		if a.Content == nil {
			return "", toolErr("content is required for create")
		}
		res, err = t.Mgr.Create(a.Name, *a.Content)
		verb = "created skill " + a.Name
	case "patch":
		if a.OldString == nil || a.NewString == nil {
			return "", toolErr("old_string and new_string are required for patch")
		}
		file := a.File
		if strings.TrimSpace(file) == "" {
			file = "SKILL.md"
		}
		res, err = t.Mgr.PatchFile(a.Name, file, *a.OldString, *a.NewString)
		verb = "patched skill " + a.Name
	case "write_file":
		if a.Content == nil {
			return "", toolErr("content is required for write_file")
		}
		res, err = t.Mgr.WriteFile(a.Name, a.File, *a.Content)
		verb = "wrote a file of skill " + a.Name
	case "delete":
		if err := t.Mgr.Delete(a.Name); err != nil {
			return "", toolErr("%v", err)
		}
		return "deleted skill " + a.Name, nil
	default:
		return "", toolErr("unknown action %q; use create, patch, write_file, or delete", a.Action)
	}
	if err != nil {
		return "", toolErr("%v", err)
	}
	out := fmt.Sprintf("%s: %s", verb, res.Path)
	if len(res.Findings) > 0 {
		report := skills.FormatFindings(skills.ScanResult{Verdict: res.Verdict, Findings: res.Findings})
		if env != nil && env.Log != nil && res.Verdict != skills.VerdictSafe {
			env.Log.Warn("skill guard findings in a saved skill", "skill", a.Name, "verdict", string(res.Verdict), "findings", len(res.Findings))
		}
		out += "\nThe skill is saved, but the skill guard found these items. Check them:\n" + report
	}
	return out, nil
}

// refuseExternal returns an error when name is a skill of an external
// folder and no skill of the skills folder has that name.
func (t SkillManageTool) refuseExternal(name string) error {
	if t.Catalog == nil {
		return nil
	}
	all, _, err := t.Catalog.Load()
	if err != nil {
		return toolErr("cannot load the skills: %v", err)
	}
	for _, s := range all {
		if s.Name == name && s.External {
			return toolErr("skill %s is in the external folder %s; external skills are read-only. "+
				"To change it, create a skill with another name", name, s.Dir)
		}
	}
	return nil
}
