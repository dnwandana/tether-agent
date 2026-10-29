package skills

import (
	"bytes"
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

// backupTimeFormat is the UTC time in the name of a backup folder.
const backupTimeFormat = "20060102T150405Z"

// UpdateRequest holds the arguments and flags of one update.
type UpdateRequest struct {
	// Names are the skills to update. When it is empty, the update takes
	// each skill that the check reports with an update or a source change.
	Names []string
	Yes   bool
	// Force replaces a skill that the agent changed, and passes a caution
	// verdict from a community source.
	Force bool
}

// FileChange is one difference between the installed folder and the new
// version of a skill.
type FileChange struct {
	// Op is 'M' for a changed file, 'A' for an added file, or 'D' for a
	// removed file.
	Op   byte
	Path string
}

// DiffDirs compares the regular files of oldDir and newDir by relative path
// and content. It returns the changes sorted by path.
func DiffDirs(oldDir, newDir string) ([]FileChange, error) {
	oldFiles, err := regularFiles(oldDir)
	if err != nil {
		return nil, err
	}
	newFiles, err := regularFiles(newDir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(oldFiles)+len(newFiles))
	for p := range oldFiles {
		paths = append(paths, p)
	}
	for p := range newFiles {
		if !oldFiles[p] {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	var changes []FileChange
	for _, p := range paths {
		switch {
		case !newFiles[p]:
			changes = append(changes, FileChange{Op: 'D', Path: p})
		case !oldFiles[p]:
			changes = append(changes, FileChange{Op: 'A', Path: p})
		default:
			same, err := sameContent(filepath.Join(oldDir, filepath.FromSlash(p)), filepath.Join(newDir, filepath.FromSlash(p)))
			if err != nil {
				return nil, err
			}
			if !same {
				changes = append(changes, FileChange{Op: 'M', Path: p})
			}
		}
	}
	return changes, nil
}

// regularFiles returns the slash paths of the regular files under dir. The
// walk does not follow symlinks.
func regularFiles(dir string) (map[string]bool, error) {
	files := map[string]bool{}
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
		files[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cannot list the files of %s: %w", dir, err)
	}
	return files, nil
}

func sameContent(a, b string) (bool, error) {
	da, err := os.ReadFile(a)
	if err != nil {
		return false, fmt.Errorf("cannot read %s for the diff: %w", a, err)
	}
	db, err := os.ReadFile(b)
	if err != nil {
		return false, fmt.Errorf("cannot read %s for the diff: %w", b, err)
	}
	return bytes.Equal(da, db), nil
}

// formatChanges returns the "Changed files" line of the update report.
func formatChanges(changes []FileChange) string {
	if len(changes) == 0 {
		return "Changed files: none"
	}
	var b strings.Builder
	b.WriteString("Changed files:")
	for i, c := range changes {
		sep := "   "
		if i == 0 {
			sep = "  "
		}
		fmt.Fprintf(&b, "%s%c %s", sep, c.Op, c.Path)
	}
	return b.String()
}

// noGitUpdateError returns ErrNoGit with the install hint for the current
// system and the next step of an update.
func noGitUpdateError() error {
	//lint:ignore ST1005 the text gives the next step as a full sentence.
	return fmt.Errorf("%w. %s, then run the update again.", ErrNoGit, installGit())
}

// Update installs the new version of each target skill. It goes on with the
// next skill when one fails or the owner cancels it. It returns an error when
// any skill was not updated.
func (in *Installer) Update(ctx context.Context, req UpdateRequest) error {
	lock, err := ReadLock(in.SkillsDir)
	if err != nil {
		return err
	}
	var names []string
	unchecked := 0
	if len(req.Names) > 0 {
		seen := map[string]bool{}
		for _, n := range req.Names {
			if _, ok := lock.Installed[n]; !ok {
				return fmt.Errorf("%q is not in the lockfile", Sanitize(n))
			}
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
		sort.Strings(names)
	} else {
		rows, err := Check(ctx, in.SkillsDir, in.FindGit, "")
		if err != nil {
			return err
		}
		for _, r := range rows {
			switch {
			case r.Update:
				names = append(names, r.Name)
			case r.Failed:
				// A skill that could not be checked is not up to date, so
				// the owner sees the cause.
				unchecked++
				in.errorf("%s: cannot check: %s\n", Sanitize(r.Name), Sanitize(r.Latest))
			}
		}
	}
	if len(names) == 0 {
		if unchecked > 0 {
			return fmt.Errorf("%d skill(s) could not be checked", unchecked)
		}
		in.printf("All installed skills are up to date.\n")
		return nil
	}

	failed := unchecked
	for i, name := range names {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%d skill(s) were not updated: %w", failed+len(names)-i, err)
		}
		if err := in.updateOne(ctx, name, lock.Installed[name], req); err != nil {
			failed++
			if !errors.Is(err, ErrCancelled) {
				// A bad lockfile entry can have any name, so the name also
				// goes through Sanitize.
				in.errorf("%s: %s\n", Sanitize(name), Sanitize(err.Error()))
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d skill(s) were not updated", failed)
	}
	return nil
}

// updateOne fetches, scans, and swaps in the new version of one skill. The
// old folder stays in place on every error.
func (in *Installer) updateOne(ctx context.Context, name string, entry LockEntry, req UpdateRequest) error {
	if err := CheckEntry(name, entry); err != nil {
		return err
	}
	src := Source{Identifier: entry.Identifier, URL: entry.URL, Ref: entry.Ref, Path: entry.SkillPath}
	switch entry.Source {
	case "git":
		src.Kind = SourceGit
	case "local":
		src.Kind = SourceLocal
	default:
		return fmt.Errorf("the lockfile entry has the unknown source %q", entry.Source)
	}
	dir := filepath.Join(in.SkillsDir, name)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the folder skills/%s is missing. Remove the entry with: tether skills uninstall %s", name, name)
	}

	// The check of the installed folder comes before the fetch, because a
	// refused skill does not need the network.
	stamp := in.now().UTC().Format(backupTimeFormat)
	backupRel := fmt.Sprintf("skills/.hub/backup/%s-%s/", name, stamp)
	hash, _, err := ContentHash(dir)
	if err != nil {
		return err
	}
	modified := hash != entry.ContentHash
	if modified && !req.Force {
		files, err := changedText(dir, entry)
		if err != nil {
			return err
		}
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return fmt.Errorf("the agent changed %s since install. Run the update again with --force to replace it.\n"+
			"The old folder will go to %s.", files, backupRel)
	}

	// The skill search starts at the skill folder, so it finds the skill at
	// that folder under any name. A new name then gets its own error.
	s, err := in.stage(ctx, src, "")
	if errors.Is(err, ErrNoGit) {
		// The install text offers a local folder, which an update cannot use.
		return noGitUpdateError()
	}
	if err != nil {
		return err
	}
	defer s.st.Remove()
	if s.skillPath != entry.SkillPath {
		return fmt.Errorf("the source has no skill at %s now. Uninstall %s, then install the skill again", Sanitize(entry.SkillPath), name)
	}
	if s.skill.Name != name {
		return fmt.Errorf("the upstream skill is now named %q. Uninstall %s, then install the new skill", Sanitize(s.skill.Name), name)
	}

	changes, err := DiffDirs(dir, s.st.Skill())
	if err != nil {
		return err
	}
	where := Sanitize(entry.Identifier)
	if entry.SkillPath != "" {
		where += ", " + Sanitize(entry.SkillPath)
	}
	in.printf("%s: %s → %s (%s)\n", name, Sanitize(version(entry.Commit, entry.ContentHash)), Sanitize(version(s.commit, s.hash)), where)
	in.printf("%s\n", Sanitize(formatChanges(changes)))
	in.report(s)
	if req.Force {
		var reasons []string
		if modified {
			reasons = append(reasons, "the agent changed the skill")
		}
		if Decide(s.trust, s.scan.Verdict, false) == DecideNeedForce {
			reasons = append(reasons, "community source, caution verdict")
		}
		if len(reasons) > 0 {
			in.printf("--force passes: %s\n", strings.Join(reasons, "; "))
		}
	}

	override, err := in.authorize(s, req.Yes, req.Force, fmt.Sprintf("Install the new version of %q?", name), false)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if modified {
		if err := in.backup(dir, name, stamp); err != nil {
			return err
		}
		in.printf("The old folder is now in %s.\n", backupRel)
	}
	undo, err := swapIn(dir, s.st.Skill(), s.st.Old(), true)
	if err != nil {
		return err
	}
	now := in.now().UTC().Format(time.RFC3339)
	err = UpdateLock(in.SkillsDir, func(l *LockFile) error {
		e, ok := l.Installed[name]
		if !ok {
			return fmt.Errorf("%q is not in the lockfile", name)
		}
		e.Commit = s.commit
		e.ContentHash = s.hash
		e.Files = s.files
		e.ScanVerdict = s.scan.Verdict
		e.OwnerOverride = override
		e.TrustLevel = s.trust
		e.UpdatedAt = now
		l.Installed[name] = e
		return nil
	})
	if err != nil {
		if uerr := undo(); uerr != nil {
			return errors.Join(err, fmt.Errorf("cannot put back the old skills/%s: %w", name, uerr))
		}
		return err
	}

	if s.commit != "" {
		in.printf("Updated %s (commit %s).\n", name, Sanitize(ShortCommit(s.commit)))
	} else {
		in.printf("Updated %s.\n", name)
	}
	return nil
}

// backup copies the skill folder to skills/.hub/backup/<name>-<stamp>. The
// update never removes a backup.
func (in *Installer) backup(dir, name, stamp string) error {
	parent := filepath.Join(HubDir(in.SkillsDir), "backup")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("cannot make the backup folder %s: %w", parent, err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("cannot set the mode of %s: %w", parent, err)
	}
	dst := filepath.Join(parent, name+"-"+stamp)
	if err := CopyTree(dir, dst); err != nil {
		return fmt.Errorf("cannot back up skills/%s: %w", name, err)
	}
	if err := os.Chmod(dst, 0o700); err != nil {
		return fmt.Errorf("cannot set the mode of %s: %w", dst, err)
	}
	return nil
}

// changedText names the files that the agent added or removed since the
// install. The lockfile keeps no old content, so a change of content only
// gets a general text.
func changedText(dir string, e LockEntry) (string, error) {
	added, removed, _, err := ChangedFiles(dir, e)
	if err != nil {
		return "", err
	}
	names := append(append([]string{}, added...), removed...)
	if len(names) == 0 {
		return "the content of the files", nil
	}
	sort.Strings(names)
	shown := names
	if len(shown) > maxChangedNames {
		shown = shown[:maxChangedNames]
	}
	text := strings.Join(shown, ", ")
	if rest := len(names) - len(shown); rest > 0 {
		text += fmt.Sprintf(", and %d more", rest)
	}
	return text, nil
}

// version returns the short commit, or the start of the content hash when
// a local source has no commit.
func version(commit, hash string) string {
	if commit != "" {
		return ShortCommit(commit)
	}
	return ShortCommit(strings.TrimPrefix(hash, "sha256:"))
}
