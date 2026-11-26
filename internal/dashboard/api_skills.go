package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// SkillRow is one installed skill of the Skills page.
type SkillRow struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Trust       string     `json:"trust"`
	Source      string     `json:"source"` // the lock identifier; "" for a skill outside the lockfile
	Commit      string     `json:"commit"`
	Pinned      bool       `json:"pinned"`
	Verdict     string     `json:"verdict"`
	RequiredEnv []string   `json:"required_env"`
	Check       string     `json:"check"` // the status of the last check; "" before the first check
	Update      bool       `json:"update"`
	Actions     []string   `json:"actions"`
	LastRead    *time.Time `json:"last_read"` // the last time the owner or the agent used the skill; nil when unknown
	Kept        bool       `json:"kept"`      // true when the curator never archives the skill
}

// ArchivedSkillRow is one skill that the curator archived.
type ArchivedSkillRow struct {
	Name       string    `json:"name"`
	RelDir     string    `json:"rel_dir"`
	ArchivedAt time.Time `json:"archived_at"`
}

// SkillInfo gives the read time, the kept flag, and the archive of the skills.
type SkillInfo interface {
	LastRead(ctx context.Context, name string) (time.Time, bool)
	Kept(ctx context.Context, name string) bool
	Archived(ctx context.Context) ([]skills.Flag, error)
	SetKept(ctx context.Context, name string, kept bool) error
	Restore(ctx context.Context, name string) error
}

// SkillFinding is one guard finding of a skill.
type SkillFinding struct {
	Severity    string `json:"severity"`
	Pattern     string `json:"pattern"`
	Category    string `json:"category"`
	Description string `json:"description"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Match       string `json:"match"`
}

// SkillDetailView is the detail of one skill.
type SkillDetailView struct {
	Skill       SkillRow       `json:"skill"`
	Body        string         `json:"body"`     // the SKILL.md text after the frontmatter, redacted and cut at 256 KiB
	BodyCut     bool           `json:"body_cut"` // true when the body was cut at 256 KiB
	Files       []string       `json:"files"`
	Ref         string         `json:"ref"`
	InstalledAt string         `json:"installed_at"`
	UpdatedAt   string         `json:"updated_at"`
	ContentHash string         `json:"content_hash"`
	Override    bool           `json:"override"`
	Findings    []SkillFinding `json:"findings"`
	Unscanned   []string       `json:"unscanned"`
	CronJobs    *[]string      `json:"cron_jobs"` // the cron jobs that use the skill, as "<name>" (<id>); nil when unknown
}

// CatalogRow is one entry of the curated index.
type CatalogRow struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Installed   bool   `json:"installed"`
	Verdict     string `json:"verdict"`      // the guard verdict that the lockfile holds for an installed skill; "" if unknown
	SourceTrust string `json:"source_trust"` // the trust level of the source repo
}

// SkillFileView is the redacted text of one support file. Cut is true when the text is longer than 256 KiB.
type SkillFileView struct {
	Text string `json:"text"`
	Cut  bool   `json:"cut"`
}

func init() {
	registerAPI(SkillRow{}, SkillFinding{}, SkillDetailView{}, CatalogRow{}, SkillFileView{}, ArchivedSkillRow{})
}

const (
	skillFileReadMax = 4 << 20
	skillFileCutMax  = 256 << 10
)

// SkillsAPI serves the Skills page.
type SkillsAPI struct {
	dataDir   string
	cfg       *config.Config
	ask       tools.Asker
	tasks     *Tasks
	cronUsers func(ctx context.Context) (map[string][]string, error)
	findGit   func() (skills.Git, error) // the git finder with skills.git_timeout_s; a test replaces it
	info      SkillInfo                  // the history of the skills; nil when not available
	redact    func(string) string        // masks secrets in a file text; nil keeps the text
	source    func(name string) string   // the install source of a name; a test replaces it
	mu        sync.Mutex
	checks    map[string]skills.CheckRow // the rows of the last check
}

// skillsKind is the task kind of each skill action.
const skillsKind = "skills"

// NewSkillsAPI returns the API for the skills of dataDir. ask opens the confirm cards.
func NewSkillsAPI(dataDir string, cfg *config.Config, ask tools.Asker, tasks *Tasks, cronUsers func(ctx context.Context) (map[string][]string, error), redact func(string) string, info SkillInfo) *SkillsAPI {
	var findGit func() (skills.Git, error)
	if in, err := skills.NewInstaller(dataDir, cfg, nil, io.Discard, nil); err == nil {
		findGit = in.FindGit
	}
	return &SkillsAPI{
		dataDir: dataDir, cfg: cfg, ask: ask, tasks: tasks, cronUsers: cronUsers, findGit: findGit, redact: redact, info: info,
		source: func(name string) string { return name },
		checks: map[string]skills.CheckRow{},
	}
}

// Register adds the routes of the Skills page.
func (a *SkillsAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/skills", a.list)
	mux.HandleFunc("GET /api/skills/catalog", a.catalog)
	mux.HandleFunc("GET /api/skills/archived", a.archived)
	mux.HandleFunc("GET /api/skills/{name}", a.detail)
	mux.HandleFunc("GET /api/skills/{name}/file", a.file)
	mux.HandleFunc("POST /api/skills/check", a.check)
	mux.HandleFunc("POST /api/skills/{name}/{action}", a.action)
}

// CronSkillUsers returns the cron users function of the gateway. It uses the one rule of
// cron.SkillUsers, so a prompt word is a user of a skill too.
func CronSkillUsers(st *cron.Store) func(ctx context.Context) (map[string][]string, error) {
	return func(ctx context.Context) (map[string][]string, error) {
		return cron.SkillUsers(ctx, st, nil)
	}
}

func skillsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func skillsErr(w http.ResponseWriter, status int, msg string) {
	skillsJSON(w, status, map[string]string{"error": msg})
}

func (a *SkillsAPI) skillsDir() string { return filepath.Join(a.dataDir, "skills") }

func (a *SkillsAPI) external() []string {
	if a.cfg == nil {
		return nil
	}
	return a.cfg.Skills.ExternalDirs
}

// row builds the row of one skill from the loader and the lockfile.
func (a *SkillsAPI) row(ctx context.Context, s skills.Skill, lock *skills.LockFile) SkillRow {
	r := SkillRow{
		Name: s.Name, Description: s.Description, Trust: string(s.Trust),
		RequiredEnv: s.RequiredEnv, Actions: []string{},
	}
	if r.RequiredEnv == nil {
		r.RequiredEnv = []string{}
	}
	e, locked := lock.Installed[s.Name]
	if locked {
		if e.TrustLevel != "" {
			r.Trust = string(e.TrustLevel)
		}
		r.Source, r.Commit, r.Verdict = e.Identifier, e.Commit, string(e.ScanVerdict)
		r.Pinned = skills.IsCommitSHA(e.Ref)
		if !r.Pinned {
			r.Actions = append(r.Actions, "update")
		}
		r.Actions = append(r.Actions, "uninstall")
	}
	if slices.Contains(skills.BootstrapNames(), s.Name) {
		r.Actions = append(r.Actions, "reset")
	}
	a.mu.Lock()
	if c, ok := a.checks[s.Name]; ok {
		r.Check, r.Update = c.Status, c.Update
	}
	a.mu.Unlock()
	if a.info != nil {
		if t, ok := a.info.LastRead(ctx, s.Name); ok {
			r.LastRead = &t
		}
		r.Kept = a.info.Kept(ctx, s.Name)
	}
	return r
}

func (a *SkillsAPI) archived(w http.ResponseWriter, r *http.Request) {
	rows := []ArchivedSkillRow{}
	if a.info != nil {
		flags, err := a.info.Archived(r.Context())
		if err != nil {
			skillsErr(w, http.StatusInternalServerError, skills.Sanitize(err.Error()))
			return
		}
		for _, f := range flags {
			rows = append(rows, ArchivedSkillRow{Name: skills.Sanitize(f.Name), RelDir: skills.Sanitize(f.RelDir), ArchivedAt: f.ArchivedAt})
		}
	}
	skillsJSON(w, http.StatusOK, rows)
}

func (a *SkillsAPI) list(w http.ResponseWriter, r *http.Request) {
	loaded, _, err := skills.Load(a.skillsDir(), a.external(), false)
	if err != nil {
		skillsErr(w, http.StatusInternalServerError, skills.Sanitize(err.Error()))
		return
	}
	lock, err := skills.ReadLock(a.skillsDir())
	if err != nil {
		skillsErr(w, http.StatusInternalServerError, skills.Sanitize(err.Error()))
		return
	}
	rows := []SkillRow{}
	for _, s := range loaded {
		rows = append(rows, a.row(r.Context(), s, lock))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	skillsJSON(w, http.StatusOK, rows)
}

func (a *SkillsAPI) catalog(w http.ResponseWriter, r *http.Request) {
	index, err := skills.LoadIndex()
	if err != nil {
		skillsErr(w, http.StatusInternalServerError, skills.Sanitize(err.Error()))
		return
	}
	loaded, _, _ := skills.Load(a.skillsDir(), a.external(), false)
	lock, _ := skills.ReadLock(a.skillsDir()) // a bad lockfile only leaves the verdict empty
	have := map[string]bool{}
	for _, s := range loaded {
		have[s.Name] = true
	}
	rows := []CatalogRow{}
	for _, e := range skills.FilterIndex(index, r.URL.Query().Get("q")) {
		row := CatalogRow{Name: e.Name, Description: e.Description, URL: e.URL, Installed: have[e.Name],
			SourceTrust: string(skills.TrustForURL(e.URL))}
		if lock != nil {
			row.Verdict = string(lock.Installed[e.Name].ScanVerdict)
		}
		rows = append(rows, row)
	}
	skillsJSON(w, http.StatusOK, rows)
}

func (a *SkillsAPI) detail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	loaded, _, err := skills.Load(a.skillsDir(), a.external(), false)
	if err != nil {
		skillsErr(w, http.StatusInternalServerError, skills.Sanitize(err.Error()))
		return
	}
	lock, err := skills.ReadLock(a.skillsDir())
	if err != nil {
		skillsErr(w, http.StatusInternalServerError, skills.Sanitize(err.Error()))
		return
	}
	i := slices.IndexFunc(loaded, func(s skills.Skill) bool { return s.Name == name })
	if i < 0 {
		skillsErr(w, http.StatusNotFound, `No skill named "`+name+`".`)
		return
	}
	s := loaded[i]
	v := SkillDetailView{Skill: a.row(r.Context(), s, lock), Files: []string{}, Findings: []SkillFinding{}, Unscanned: []string{}}
	body := skills.Sanitize(s.Body)
	if a.redact != nil {
		body = a.redact(body)
	}
	v.Body, v.BodyCut = cutText(body, skillFileCutMax)
	// cron.Store.List closes its own rows, and the handler holds no open rows here.
	if a.cronUsers != nil {
		if users, err := a.cronUsers(r.Context()); err == nil {
			jobs := append([]string{}, users[name]...)
			v.CronJobs = &jobs
		}
	}
	v.Files = skillFiles(s.Dir)
	if e, ok := lock.Installed[name]; ok {
		v.Ref, v.InstalledAt, v.UpdatedAt = e.Ref, e.InstalledAt, e.UpdatedAt
		v.ContentHash, v.Override = e.ContentHash, e.OwnerOverride
	}
	if s.Trust != skills.TrustBuiltin {
		if res, err := skills.ScanDir(s.Dir); err == nil {
			for _, f := range res.Findings {
				v.Findings = append(v.Findings, SkillFinding{
					Severity: string(f.Severity), Pattern: f.PatternID, Category: f.Category,
					Description: skills.Sanitize(f.Description), File: f.File,
					Line: f.Line, Match: skills.Sanitize(f.Match),
				})
			}
			if res.Unscanned != nil {
				v.Unscanned = res.Unscanned
			}
		}
	}
	skillsJSON(w, http.StatusOK, v)
}

// skillFiles returns the sorted slash paths of the regular files of the skill folder dir.
func skillFiles(dir string) []string {
	files := []string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if rel, err := filepath.Rel(dir, p); err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// file serves the redacted text of one support file of a skill. Each refusal comes before the read.
func (a *SkillsAPI) file(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	loaded, _, err := skills.Load(a.skillsDir(), a.external(), false)
	if err != nil {
		skillsErr(w, http.StatusInternalServerError, skills.Sanitize(err.Error()))
		return
	}
	i := slices.IndexFunc(loaded, func(s skills.Skill) bool { return s.Name == name })
	if i < 0 {
		skillsErr(w, http.StatusNotFound, `No skill named "`+name+`".`)
		return
	}
	dir := loaded[i].Dir
	const missing = "No such file in this skill."
	rel := r.URL.Query().Get("path")
	if !slices.Contains(skillFiles(dir), rel) {
		skillsErr(w, http.StatusNotFound, missing)
		return
	}
	target := filepath.Join(dir, filepath.FromSlash(rel))
	if fi, err := os.Lstat(target); err != nil || !fi.Mode().IsRegular() {
		skillsErr(w, http.StatusNotFound, missing)
		return
	}
	realDir, err1 := filepath.EvalSymlinks(dir)
	realTarget, err2 := filepath.EvalSymlinks(target)
	if err1 != nil || err2 != nil {
		skillsErr(w, http.StatusNotFound, missing)
		return
	}
	if up, err := filepath.Rel(realDir, realTarget); err != nil || up == ".." || strings.HasPrefix(up, ".."+string(filepath.Separator)) {
		skillsErr(w, http.StatusNotFound, missing)
		return
	}
	home, _ := os.UserHomeDir()
	base := filepath.Base(target)
	if security.CheckWritable(target, security.ProtectedPaths(a.dataDir, home), true) != nil ||
		base == ".env" || strings.HasPrefix(base, ".env.") {
		skillsErr(w, http.StatusForbidden, "This file is protected.")
		return
	}
	// O_NONBLOCK keeps the open from a wait when the path became a FIFO after the checks.
	f, err := os.OpenFile(realTarget, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		skillsErr(w, http.StatusNotFound, missing)
		return
	}
	defer f.Close()
	// The file can change between the checks and the open, so check the open file again.
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		skillsErr(w, http.StatusNotFound, missing)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(f, skillFileReadMax+1))
	if err != nil {
		skillsErr(w, http.StatusInternalServerError, "Could not read the file.")
		return
	}
	over := len(raw) > skillFileReadMax
	if over {
		raw = raw[:skillFileReadMax]
		// A rune cut at the read limit is not an error.
		for n := 0; n < utf8.UTFMax && len(raw) > 0 && !utf8.Valid(raw); n++ {
			raw = raw[:len(raw)-1]
		}
	}
	if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
		skillsErr(w, http.StatusUnsupportedMediaType, "This file is not text.")
		return
	}
	text := string(raw)
	if a.redact != nil {
		text = a.redact(text)
	}
	text, cut := cutText(text, skillFileCutMax)
	skillsJSON(w, http.StatusOK, SkillFileView{Text: text, Cut: cut || over})
}

// startTask starts a task of the kind skills and answers 202 or 409.
func (a *SkillsAPI) startTask(w http.ResponseWriter, subject string, run func(ctx context.Context, h TaskHandle, out *bytes.Buffer) error) {
	id, err := a.tasks.StartWith(TaskOpts{Kind: skillsKind, Subject: subject}, func(ctx context.Context, h TaskHandle) (string, error) {
		var out bytes.Buffer
		err := run(ctx, h, &out)
		text := skills.Sanitize(out.String())
		if err != nil {
			return "", errors.New(text + skills.Sanitize(err.Error()))
		}
		return text, nil
	})
	switch {
	case errors.Is(err, ErrTaskBusy):
		skillsErr(w, http.StatusConflict, err.Error())
	case err != nil:
		skillsErr(w, http.StatusServiceUnavailable, err.Error())
	default:
		skillsJSON(w, http.StatusAccepted, map[string]string{"task_id": id})
	}
}

func (a *SkillsAPI) check(w http.ResponseWriter, r *http.Request) {
	a.startTask(w, "", func(ctx context.Context, _ TaskHandle, out *bytes.Buffer) error {
		rows, err := skills.Check(ctx, a.skillsDir(), a.findGit, "")
		if err != nil {
			return err
		}
		m := map[string]skills.CheckRow{}
		for _, row := range rows {
			m[row.Name] = row
		}
		a.mu.Lock()
		a.checks = m
		a.mu.Unlock()
		skills.FormatCheck(out, rows)
		return nil
	})
}

func (a *SkillsAPI) action(w http.ResponseWriter, r *http.Request) {
	name, action := r.PathValue("name"), r.PathValue("action")
	if err := skills.CheckName(name); err != nil {
		skillsErr(w, http.StatusBadRequest, skills.Sanitize(err.Error()))
		return
	}
	switch action {
	case "install", "update", "uninstall", "reset":
	case "keep", "unkeep", "restore":
		if a.info == nil {
			skillsErr(w, http.StatusServiceUnavailable, "The skill history is not available.")
			return
		}
		a.startTask(w, name, func(ctx context.Context, h TaskHandle, out *bytes.Buffer) error {
			var err error
			switch action {
			case "keep":
				if err = a.info.SetKept(ctx, name, true); err == nil {
					fmt.Fprintf(out, "Kept %s. The curator never archives it.\n", name)
				}
			case "unkeep":
				if err = a.info.SetKept(ctx, name, false); err == nil {
					fmt.Fprintf(out, "%s is not kept now. The curator can archive it.\n", name)
				}
			default:
				if err = a.info.Restore(ctx, name); err == nil {
					fmt.Fprintf(out, "Restored %s. It loads in new sessions.\n", name)
				}
			}
			if err != nil {
				return errors.New(skills.Sanitize(err.Error()))
			}
			return nil
		})
		return
	default:
		skillsErr(w, http.StatusNotFound, `Unknown skill action "`+action+`".`)
		return
	}
	var body struct {
		Force bool `json:"force"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, bodyMaxBytes)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		skillsErr(w, http.StatusBadRequest, textBadBody)
		return
	}
	a.startTask(w, name, func(ctx context.Context, h TaskHandle, out *bytes.Buffer) error {
		if action == "reset" {
			if err := skills.Reset(a.skillsDir(), name); err != nil {
				return err
			}
			out.WriteString("Reset ")
			out.WriteString(name)
			out.WriteString(".\n")
			return nil
		}
		in, err := skills.NewInstaller(a.dataDir, a.cfg, newInstallPrompter(ctx, a.ask, out, name, h), out, a.cronUsers)
		if err != nil {
			return err
		}
		in.OnNeedForce = func() { h.Code("need_force") }
		switch action {
		case "install":
			_, err = in.Install(ctx, skills.InstallRequest{Source: a.source(name), Force: body.Force})
		case "update":
			err = in.Update(ctx, skills.UpdateRequest{Names: []string{name}, Force: body.Force})
		default:
			err = in.Uninstall(ctx, skills.UninstallRequest{Name: name})
		}
		return err
	})
}
