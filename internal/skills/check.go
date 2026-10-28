package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

// The status texts of the check command. A row can join a main status and
// StatusModified, for example "update available, modified locally".
const (
	StatusUpToDate      = "up to date"
	StatusUpdate        = "update available"
	StatusSourceChanged = "source changed"
	StatusPinned        = "pinned"
	StatusModified      = "modified locally"
	StatusCannotCheck   = "cannot check"
)

// CheckRow is one line of the check report.
type CheckRow struct {
	Name      string
	Status    string
	Installed string
	Latest    string
	// Update is true for "update available" and "source changed".
	Update bool
	// Modified is true when the installed folder differs from the lockfile hash.
	Modified bool
	// Failed is true when the row could not be checked.
	Failed bool
}

// Check compares each lockfile entry with its source and returns one row for
// each entry, sorted by name. An empty name checks every entry. A nil findGit
// uses FindGit. Check calls findGit at most once, and only when a git entry
// needs the remote. Check writes nothing to the disk.
func Check(ctx context.Context, skillsDir string, findGit func() (Git, error), name string) ([]CheckRow, error) {
	l, err := ReadLock(skillsDir)
	if err != nil {
		return nil, err
	}
	var names []string
	if name != "" {
		if _, ok := l.Installed[name]; !ok {
			return nil, fmt.Errorf("%q is not in the lockfile", name)
		}
		names = []string{name}
	} else {
		for n := range l.Installed {
			names = append(names, n)
		}
		sort.Strings(names)
	}
	if findGit == nil {
		findGit = FindGit
	}
	c := &checker{ctx: ctx, skillsDir: skillsDir, findGit: findGit}
	rows := make([]CheckRow, 0, len(names))
	for _, n := range names {
		rows = append(rows, c.row(n, l.Installed[n]))
	}
	return rows, nil
}

// checker holds the state that the rows of one check share. The git program
// is looked up once, on the first git row.
type checker struct {
	ctx       context.Context
	skillsDir string
	findGit   func() (Git, error)
	looked    bool
	git       Git
	gitErr    error
}

func (c *checker) gitRunner() (Git, error) {
	if !c.looked {
		c.looked = true
		c.git, c.gitErr = c.findGit()
	}
	return c.git, c.gitErr
}

// row checks one lockfile entry.
func (c *checker) row(name string, e LockEntry) CheckRow {
	r := CheckRow{Name: name}
	fail := func(cause string) CheckRow {
		r.Status = StatusCannotCheck
		r.Latest = cause
		r.Failed = true
		return r
	}
	// A bad install_path could point the hash at a folder outside skills/.
	if err := CheckEntry(name, e); err != nil {
		return fail("bad lockfile entry")
	}
	dir := filepath.Join(c.skillsDir, e.InstallPath)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return fail("folder missing")
	}
	hash, _, err := ContentHash(dir)
	if err != nil {
		return fail("cannot read the folder")
	}
	modified := hash != e.ContentHash

	var status string
	switch {
	case e.Source == "git" && IsCommitSHA(e.Ref):
		// A pinned commit cannot change, so the remote is not called.
		status = StatusPinned
		r.Installed = ShortCommit(e.Commit)
		r.Latest = "--ref " + ShortCommit(e.Ref)
	case e.Source == "git":
		r.Installed = ShortCommit(e.Commit)
		latest, err := c.remoteCommit(e)
		if err != nil {
			status = StatusCannotCheck
			r.Latest = shortCause(err)
			r.Failed = true
		} else {
			r.Latest = ShortCommit(latest)
			if latest == e.Commit {
				status = StatusUpToDate
			} else {
				status = StatusUpdate
				r.Update = true
			}
		}
	case e.Source == "local":
		r.Installed = "(local)"
		src := filepath.Join(e.URL, filepath.FromSlash(e.SkillPath))
		if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
			status = StatusCannotCheck
			r.Latest = "source folder missing"
			r.Failed = true
			break
		}
		srcHash, _, err := ContentHash(src)
		switch {
		case err != nil:
			status = StatusCannotCheck
			r.Latest = "cannot read the source folder"
			r.Failed = true
		case srcHash == e.ContentHash:
			status = StatusUpToDate
			r.Latest = "same"
		default:
			status = StatusSourceChanged
			r.Latest = "hash differs"
			r.Update = true
		}
	default:
		return fail("unknown source " + Sanitize(fmt.Sprintf("%q", e.Source)))
	}
	if modified {
		status += ", " + StatusModified
		r.Modified = true
	}
	r.Status = status
	return r
}

func (c *checker) remoteCommit(e LockEntry) (string, error) {
	g, err := c.gitRunner()
	if err != nil {
		return "", err
	}
	return g.RemoteCommit(c.ctx, e.URL, e.Ref)
}

// shortCause returns a short one-line cause for the LATEST column.
func shortCause(err error) string {
	if errors.Is(err, ErrNoGit) {
		return "git is not on PATH"
	}
	var ge *GitError
	if errors.As(err, &ge) && ge.after == 0 {
		if cause := ge.cause(); cause != "see the git output" {
			return Sanitize(ge.Op + ": " + cause)
		}
	}
	text := Sanitize(err.Error())
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return text
}

// CheckExitCode returns the exit code of the check command: 1 when a row has
// an update, 2 when rows exist but none could be checked, else 0.
func CheckExitCode(rows []CheckRow) int {
	failed := 0
	for _, r := range rows {
		if r.Update {
			return 1
		}
		if r.Failed {
			failed++
		}
	}
	if len(rows) > 0 && failed == len(rows) {
		return 2
	}
	return 0
}

// FormatCheck writes the check report to w. Each field goes through
// Sanitize, because a cause can come from a remote.
func FormatCheck(w io.Writer, rows []CheckRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No installed skills. Install one with: tether skills install <source>")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tINSTALLED\tLATEST")
	update := false
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", cell(r.Name), cell(r.Status), cell(r.Installed), cell(r.Latest))
		update = update || r.Update
	}
	tw.Flush()
	if update {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run tether skills update to install the updates.")
	}
}

// cell returns s as one table cell: no control characters, and no tab or
// newline that would break the columns.
func cell(s string) string {
	return strings.NewReplacer("\t", " ", "\n", " ").Replace(Sanitize(s))
}
