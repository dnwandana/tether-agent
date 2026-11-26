package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// Flag is the curator record of one skill of the data dir.
type Flag struct {
	ID           int64
	Name, RelDir string
	Kept         bool
	FirstSeen    time.Time
	ArchivedAt   time.Time
	ArchivedPath string
}

// CuratorStore keeps the curator flags and the last read time of each skill.
type CuratorStore interface {
	Flags(ctx context.Context) (map[string]Flag, error)
	SaveFlag(ctx context.Context, f Flag) (Flag, error)
	LastActivity(ctx context.Context, name string) (time.Time, bool, error)
	Backfill(ctx context.Context) error
	LastRun(ctx context.Context) (time.Time, bool, error)
	SetLastRun(ctx context.Context, t time.Time) error
}

// Curator archives the skills of Dir that nobody reads. It never deletes a
// skill and makes no model call. It never scans or moves External.
type Curator struct {
	Dir       string
	External  []string
	Store     CuratorStore
	CronUsers func(ctx context.Context, names []string) (map[string][]string, error)
	Cfg       config.CuratorConfig
	Now       func() time.Time
}

// CuratorReport tells what one curator run did.
type CuratorReport struct {
	Archived                         []Flag
	Stale                            []string
	Kept, SkippedBundled, SkippedHub int
	StaleAfterDays, ArchiveAfterDays int
}

// StaleRow is one line of the stale list. State is "active" or "stale".
type StaleRow struct {
	Name         string
	LastActivity time.Time
	State        string
}

const day = 24 * time.Hour

func (c *Curator) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// limits gives the effective times. A value of 0 or less uses the default.
// An archive time below the stale time uses the stale time.
func (c *Curator) limits() (interval, idle time.Duration, stale, archive time.Duration) {
	pick := func(v, def int) int {
		if v <= 0 {
			return def
		}
		return v
	}
	interval = time.Duration(pick(c.Cfg.IntervalHours, 168)) * time.Hour
	idle = time.Duration(pick(c.Cfg.MinIdleHours, 2)) * time.Hour
	stale = time.Duration(pick(c.Cfg.StaleAfterDays, 14)) * day
	archive = max(time.Duration(pick(c.Cfg.ArchiveAfterDays, 30))*day, stale)
	return
}

func (c *Curator) relDir(s Skill) string {
	rel, err := filepath.Rel(c.Dir, s.Dir)
	if err != nil {
		return s.Dir
	}
	return filepath.ToSlash(rel)
}

func (c *Curator) users(ctx context.Context, skills []Skill) (map[string][]string, error) {
	if c.CronUsers == nil {
		return nil, nil
	}
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return c.CronUsers(ctx, names)
}

// candidate applies the candidate rules 1 to 5. The skip text is "bundled",
// "hub", "kept", "cron", or "nested" when the skill is not a candidate.
func candidate(s Skill, flag Flag, lock *LockFile, users map[string][]string) (ok bool, skip string) {
	if slices.Contains(BootstrapNames(), s.Name) {
		return false, "bundled"
	}
	if lock != nil {
		if _, hub := lock.Installed[s.Name]; hub {
			return false, "hub"
		}
	}
	if flag.Kept {
		return false, "kept"
	}
	if len(users[s.Name]) > 0 {
		return false, "cron"
	}
	inner, _, err := scanDir(s.Dir, false)
	if err != nil {
		return false, "nested"
	}
	for _, in := range inner {
		if in.Dir != s.Dir {
			return false, "nested"
		}
	}
	return true, ""
}

func (c *Curator) activity(ctx context.Context, name string, flag Flag) (time.Time, error) {
	t, ok, err := c.Store.LastActivity(ctx, name)
	if err != nil {
		return time.Time{}, err
	}
	if !ok || flag.FirstSeen.After(t) {
		t = flag.FirstSeen
	}
	return t, nil
}

// Stale lists the skills that the curator could archive, oldest activity first.
func (c *Curator) Stale(ctx context.Context) ([]StaleRow, error) {
	_, _, stale, _ := c.limits()
	skills, _, err := scanDir(c.Dir, false)
	if err != nil {
		return nil, err
	}
	flags, err := c.Store.Flags(ctx)
	if err != nil {
		return nil, err
	}
	lock, err := ReadLock(c.Dir)
	if err != nil {
		return nil, err
	}
	users, err := c.users(ctx, skills)
	if err != nil {
		return nil, err
	}
	now := c.now()
	var rows []StaleRow
	for _, s := range skills {
		flag, has := flags[s.Name]
		if ok, _ := candidate(s, flag, lock, users); !ok {
			continue
		}
		act, err := c.activity(ctx, s.Name, flag)
		if err != nil {
			return nil, err
		}
		state := "active"
		if has && now.Sub(act) > stale {
			state = "stale"
		}
		rows = append(rows, StaleRow{Name: s.Name, LastActivity: act, State: state})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastActivity.Before(rows[j].LastActivity) })
	return rows, nil
}

// Archived lists the archived skills, newest first.
func (c *Curator) Archived(ctx context.Context) ([]Flag, error) {
	flags, err := c.Store.Flags(ctx)
	if err != nil {
		return nil, err
	}
	var out []Flag
	for _, f := range flags {
		if !f.ArchivedAt.IsZero() {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ArchivedAt.Equal(out[j].ArchivedAt) {
			return out[i].Name < out[j].Name
		}
		return out[i].ArchivedAt.After(out[j].ArchivedAt)
	})
	return out, nil
}

// IsArchived tells whether the skill is in the archive. A store error gives false.
func (c *Curator) IsArchived(ctx context.Context, name string) bool {
	flags, err := c.Store.Flags(ctx)
	if err != nil {
		return false
	}
	return !flags[name].ArchivedAt.IsZero()
}

// SetKept marks a skill as kept or not kept. A kept skill never moves.
func (c *Curator) SetKept(ctx context.Context, name string, kept bool) error {
	flags, err := c.Store.Flags(ctx)
	if err != nil {
		return err
	}
	f, ok := flags[name]
	if !ok {
		skills, _, err := scanDir(c.Dir, false)
		if err != nil {
			return err
		}
		for _, s := range skills {
			if s.Name == name {
				f = Flag{Name: name, RelDir: c.relDir(s), FirstSeen: c.now()}
				ok = true
				break
			}
		}
	}
	if !ok {
		return fmt.Errorf("no skill named %s", name)
	}
	f.Kept = kept
	_, err = c.Store.SaveFlag(ctx, f)
	return err
}

// saveMissing saves a flag for each skill that has none.
func (c *Curator) saveMissing(ctx context.Context, skills []Skill, flags map[string]Flag, now time.Time) error {
	for _, s := range skills {
		if _, ok := flags[s.Name]; ok {
			continue
		}
		f, err := c.Store.SaveFlag(ctx, Flag{Name: s.Name, RelDir: c.relDir(s), FirstSeen: now})
		if err != nil {
			return err
		}
		flags[s.Name] = f
	}
	return nil
}

// Run archives the skills that nobody reads. It returns ran == false when the
// run is not due. The first run only sets the clock and the first-seen times.
func (c *Curator) Run(ctx context.Context, force bool, idleSince time.Time) (rep CuratorReport, ran bool, err error) {
	interval, idle, stale, archive := c.limits()
	rep.StaleAfterDays = int(stale / day)
	rep.ArchiveAfterDays = int(archive / day)
	now := c.now()
	firstRun := false
	if !force {
		if !c.Cfg.Enabled {
			return rep, false, nil
		}
		last, ok, err := c.Store.LastRun(ctx)
		if err != nil {
			return rep, false, err
		}
		if !ok {
			firstRun = true
		} else if now.Sub(last) < interval {
			return rep, false, nil
		} else if !idleSince.IsZero() && now.Sub(idleSince) < idle {
			return rep, false, nil
		}
	}
	skills, _, err := scanDir(c.Dir, false)
	if err != nil {
		return rep, false, err
	}
	if firstRun {
		if err := c.Store.Backfill(ctx); err != nil {
			return rep, false, err
		}
	}
	flags, err := c.Store.Flags(ctx)
	if err != nil {
		return rep, false, err
	}
	if err := c.saveMissing(ctx, skills, flags, now); err != nil {
		return rep, false, err
	}
	if firstRun {
		return rep, false, c.Store.SetLastRun(ctx, now)
	}
	lock, err := ReadLock(c.Dir)
	if err != nil {
		return rep, false, err
	}
	users, err := c.users(ctx, skills)
	if err != nil {
		return rep, false, err
	}
	var due []Skill
	for _, s := range skills {
		flag := flags[s.Name]
		ok, skip := candidate(s, flag, lock, users)
		if !ok {
			switch skip {
			case "bundled":
				rep.SkippedBundled++
			case "hub":
				rep.SkippedHub++
			case "kept":
				rep.Kept++
			}
			continue
		}
		if now.Sub(flag.FirstSeen) <= stale {
			continue
		}
		act, err := c.activity(ctx, s.Name, flag)
		if err != nil {
			return rep, false, err
		}
		switch age := now.Sub(act); {
		case age > archive:
			due = append(due, s)
		case age > stale:
			rep.Stale = append(rep.Stale, s.Name)
		}
	}
	if len(due) > 0 {
		unlock, err := LockSkills(c.Dir)
		if err != nil {
			return rep, false, err
		}
		defer unlock()
		if err := os.MkdirAll(filepath.Join(c.Dir, ".archive"), 0o700); err != nil {
			return rep, false, fmt.Errorf("cannot make the archive folder: %w", err)
		}
		for _, s := range due {
			flag := flags[s.Name]
			rel := c.relDir(s)
			dest := filepath.Join(c.Dir, ".archive", strings.ReplaceAll(rel, "/", "__")+"-"+strconv.FormatInt(flag.ID, 10))
			if _, err := os.Lstat(dest); err == nil {
				continue
			}
			if err := os.Rename(s.Dir, dest); err != nil {
				return rep, false, fmt.Errorf("cannot archive the skill %s: %w", s.Name, err)
			}
			flag.RelDir, flag.ArchivedAt, flag.ArchivedPath = rel, now, dest
			saved, err := c.Store.SaveFlag(ctx, flag)
			if err != nil {
				return rep, false, err
			}
			rep.Archived = append(rep.Archived, saved)
		}
	}
	if err := c.Store.SetLastRun(ctx, now); err != nil {
		return rep, true, err
	}
	return rep, true, nil
}

// Restore moves an archived skill back to its old place. The skill gets the
// full grace time again.
func (c *Curator) Restore(ctx context.Context, name string) error {
	unlock, err := LockSkills(c.Dir)
	if err != nil {
		return err
	}
	defer unlock()
	flags, err := c.Store.Flags(ctx)
	if err != nil {
		return err
	}
	flag, ok := flags[name]
	if !ok || flag.ArchivedAt.IsZero() {
		return fmt.Errorf("the skill %s is not archived", name)
	}
	skills, _, err := scanDir(c.Dir, false)
	if err != nil {
		return err
	}
	target := filepath.Join(c.Dir, filepath.FromSlash(flag.RelDir))
	exists := false
	for _, s := range skills {
		if s.Name == name {
			exists = true
		}
	}
	if _, err := os.Lstat(target); err == nil {
		exists = true
	}
	if exists {
		return fmt.Errorf("a skill named %s exists; restore refused", name)
	}
	if flag.ArchivedPath == "" {
		return errors.New("the archive path of the skill " + name + " is empty")
	}
	// The paths come from state.db. Refuse a row that names a folder outside the archive,
	// or a place outside the skills folder, so Restore never moves another folder.
	archive := filepath.Join(c.Dir, ".archive")
	if flag.ArchivedPath == archive || !within(archive, flag.ArchivedPath) ||
		target == c.Dir || !within(c.Dir, target) || within(archive, target) {
		return fmt.Errorf("the skill %s cannot be restored: its archived folder is not in the skill archive, or its folder is outside the skills folder", name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("cannot make the folder for %s: %w", name, err)
	}
	if err := os.Rename(flag.ArchivedPath, target); err != nil {
		return fmt.Errorf("cannot restore the skill %s: %w", name, err)
	}
	flag.ArchivedAt, flag.ArchivedPath, flag.FirstSeen = time.Time{}, "", c.now()
	_, err = c.Store.SaveFlag(ctx, flag)
	return err
}
