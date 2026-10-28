package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxChangedNames is the number of file names that the changed-files line
// shows. The line gives the count of the other names.
const maxChangedNames = 5

// UninstallRequest holds the arguments and flags of one uninstall.
type UninstallRequest struct {
	Name string
	Yes  bool
	// CronUsers returns one line for each cron job that uses the skill, for
	// example `"morning-brief" (a1b2)`. When it is nil, the uninstall does
	// not check the cron jobs.
	CronUsers func(name string) []string
}

// ChangedFiles compares the files under dir with the files of the lockfile
// entry. The lockfile keeps no old content, so the function can name only
// the added and the removed files. contentChanged is true when the content
// hash differs and no file was added or removed.
func ChangedFiles(dir string, e LockEntry) (added, removed []string, contentChanged bool, err error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, nil, false, fmt.Errorf("cannot read the skill folder %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, nil, false, fmt.Errorf("the skill path %s is not a folder", dir)
	}
	hash, files, err := ContentHash(dir)
	if err != nil {
		return nil, nil, false, err
	}
	now := make(map[string]bool, len(files))
	for _, f := range files {
		now[f] = true
	}
	before := make(map[string]bool, len(e.Files))
	for _, f := range e.Files {
		before[f] = true
		if !now[f] {
			removed = append(removed, f)
		}
	}
	for _, f := range files {
		if !before[f] {
			added = append(added, f)
		}
	}
	sort.Strings(removed)
	contentChanged = hash != e.ContentHash && len(added) == 0 && len(removed) == 0
	return added, removed, contentChanged, nil
}

// Uninstall removes skills/<name> and its lockfile entry. It removes only a
// skill that the lockfile lists, and it checks the entry before it deletes
// anything.
func (in *Installer) Uninstall(ctx context.Context, req UninstallRequest) error {
	name := req.Name
	lock, err := ReadLock(in.SkillsDir)
	if err != nil {
		return err
	}
	entry, ok := lock.Installed[name]
	if !ok {
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return fmt.Errorf("%q is not in the lockfile. Only installed skills can be removed this way.", Sanitize(name))
	}
	if err := CheckEntry(name, entry); err != nil {
		return err
	}
	dir := filepath.Join(in.SkillsDir, name)

	in.printf("%s\n", in.uninstallHeader(name, entry))
	gone := false
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		gone = true
	} else if err != nil {
		return fmt.Errorf("cannot check the skill folder %s: %w", dir, err)
	}
	if !gone {
		in.printChanges(dir, entry)
	}
	if req.CronUsers != nil {
		for _, line := range req.CronUsers(name) {
			in.printf("Cron job %s uses this skill. Its runs will skip it.\n", Sanitize(line))
		}
	}

	if !req.Yes {
		if in.Prompt == nil || !in.Prompt.Terminal() {
			//lint:ignore ST1005 the text is a fixed user message with full sentences.
			return errors.New("no terminal for the confirm prompt. Run the command again with --yes to confirm.")
		}
		ok, err := in.Prompt.Confirm(fmt.Sprintf("Remove %s?", dir))
		if err != nil {
			return err
		}
		if !ok {
			in.printf("Cancelled.\n")
			return ErrCancelled
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if gone {
		in.printf("The folder skills/%s was already gone.\n", name)
	} else if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("cannot remove the skill folder %s: %w", dir, err)
	}
	err = UpdateLock(in.SkillsDir, func(l *LockFile) error {
		delete(l.Installed, name)
		return nil
	})
	if err != nil {
		return err
	}
	in.printf("Removed %s.\n", name)
	return nil
}

// uninstallHeader returns the line that names the skill, its source, its
// commit, and the date of the install.
func (in *Installer) uninstallHeader(name string, e LockEntry) string {
	date := e.InstalledAt
	if t, err := time.Parse(time.RFC3339, e.InstalledAt); err == nil {
		date = t.UTC().Format("2006-01-02")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — installed from %s", name, Sanitize(e.Identifier))
	if e.Commit != "" {
		fmt.Fprintf(&b, " (commit %s)", Sanitize(ShortCommit(e.Commit)))
	}
	if date != "" {
		fmt.Fprintf(&b, " on %s", Sanitize(date))
	}
	return b.String()
}

// printChanges prints the files that the agent added or removed since the
// install. A folder that cannot be compared gets a note, and the uninstall
// goes on, because the owner wants the folder gone.
func (in *Installer) printChanges(dir string, e LockEntry) {
	added, removed, contentChanged, err := ChangedFiles(dir, e)
	if err != nil {
		in.errorf("warning: cannot compare skills/%s with the lockfile: %s\n", e.InstallPath, Sanitize(err.Error()))
		return
	}
	if contentChanged {
		in.printf("The agent changed the content of the files since install.\n")
		return
	}
	names := append(append([]string{}, added...), removed...)
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	noun := "files"
	if len(names) == 1 {
		noun = "file"
	}
	shown := names
	if len(shown) > maxChangedNames {
		shown = shown[:maxChangedNames]
	}
	list := strings.Join(shown, ", ")
	if rest := len(names) - len(shown); rest > 0 {
		list += fmt.Sprintf(", and %d more", rest)
	}
	in.printf("The agent changed %d %s since install: %s\n", len(names), noun, Sanitize(list))
}
