package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Prompter asks the owner a question at the terminal.
type Prompter interface {
	// Terminal is true only when stdin and stdout are terminals.
	Terminal() bool
	// Confirm asks a yes or no question. The prompter adds " [y/N]".
	Confirm(question string) (bool, error)
	// Ask asks for a line of text.
	Ask(question string) (string, error)
}

// ErrCancelled tells that the owner said no. The installer has already
// printed "Cancelled.", so the caller prints nothing more.
var ErrCancelled = errors.New("cancelled")

// Installer fetches, scans, and installs skills into SkillsDir, and records
// them in the lockfile.
type Installer struct {
	SkillsDir string
	// External holds the external skill folders from the config. The
	// collision check reads them.
	External []string
	Prompt   Prompter
	// Out gets the reports and the prompts. Err gets the error and warning
	// lines that the installer prints itself; nil means Out.
	Out io.Writer
	Err io.Writer
	// Now, FindGit, and Index replace time.Now, FindGit, and LookupIndex
	// when they are not nil.
	Now     func() time.Time
	FindGit func() (Git, error)
	Index   func(name string) (IndexEntry, bool, error)
}

// InstallRequest holds the arguments and flags of one install.
type InstallRequest struct {
	Source, Skill, Ref string
	Yes, Force         bool
}

// InstallResult tells what an install did.
type InstallResult struct {
	Name, Commit string
	// Override is true when the owner typed the name to pass a dangerous
	// verdict.
	Override bool
	// Replaced is true when the install replaced a skill that the lockfile
	// listed.
	Replaced bool
}

// staged is a skill in its staging folder, scanned and ready for the move.
type staged struct {
	st    *Staging
	src   Source
	skill Skill
	// skillPath is the slash path of the skill folder in the repo, "" for
	// the repo root.
	skillPath, commit string
	trust             Trust
	scan              ScanResult
	hash              string
	files             []string
}

// disclaimer is the Hermes warning before the confirm prompt.
const disclaimer = "You are about to install a third-party skill.\n" +
	"A skill can contain instructions that change what the agent does, and shell commands and\n" +
	"scripts. The guard does not catch everything. Read the files before you use the skill.\n"

func (in *Installer) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in *Installer) git() (Git, error) {
	if in.FindGit != nil {
		return in.FindGit()
	}
	return FindGit()
}

func (in *Installer) lookup(name string) (IndexEntry, bool, error) {
	if in.Index != nil {
		return in.Index(name)
	}
	return LookupIndex(name)
}

func (in *Installer) printf(format string, args ...any) {
	if in.Out != nil {
		fmt.Fprintf(in.Out, format, args...)
	}
}

func (in *Installer) errorf(format string, args ...any) {
	if in.Err == nil {
		in.printf(format, args...)
		return
	}
	fmt.Fprintf(in.Err, format, args...)
}

// Install parses the source and installs one skill from it.
func (in *Installer) Install(ctx context.Context, req InstallRequest) (InstallResult, error) {
	src, err := ParseSource(req.Source, SourceOptions{Ref: req.Ref, Skill: req.Skill})
	if err != nil {
		return InstallResult{}, err
	}
	return in.installSource(ctx, src, req)
}

// installSource runs the install flow for a parsed source. It writes
// skills/<name> only after the owner confirms, and it removes the staging
// folder on every exit path.
func (in *Installer) installSource(ctx context.Context, src Source, req InstallRequest) (InstallResult, error) {
	s, err := in.stage(ctx, src, req.Skill)
	if err != nil {
		return InstallResult{}, err
	}
	defer s.st.Remove()
	in.report(s)

	name := s.skill.Name
	lock, err := ReadLock(in.SkillsDir)
	if err != nil {
		return InstallResult{}, err
	}
	loaded, _, err := Load(in.SkillsDir, in.External, false)
	if err != nil {
		return InstallResult{}, err
	}
	col, err := CheckCollision(name, in.SkillsDir, lock, loaded, req.Force)
	if err != nil {
		return InstallResult{}, err
	}
	if col.Warning != "" {
		in.printf("%s\n", Sanitize(col.Warning))
	}
	override, err := in.authorize(s, req.Yes, req.Force, fmt.Sprintf("Install %q?", name), true)
	if err != nil {
		return InstallResult{}, err
	}

	if err := ctx.Err(); err != nil {
		return InstallResult{}, err
	}
	undo, err := swapIn(filepath.Join(in.SkillsDir, name), s.st.Skill(), s.st.Old(), col.Replace)
	if err != nil {
		return InstallResult{}, err
	}
	now := in.now().UTC().Format(time.RFC3339)
	kind := "git"
	if s.src.Kind == SourceLocal {
		kind = "local"
	}
	entry := LockEntry{
		Source:        kind,
		Identifier:    s.src.Identifier,
		URL:           s.src.URL,
		Ref:           s.src.Ref,
		Commit:        s.commit,
		SkillPath:     s.skillPath,
		TrustLevel:    s.trust,
		ScanVerdict:   s.scan.Verdict,
		OwnerOverride: override,
		ContentHash:   s.hash,
		InstallPath:   name,
		Files:         s.files,
		InstalledAt:   now,
		UpdatedAt:     now,
	}
	err = UpdateLock(in.SkillsDir, func(l *LockFile) error {
		if old, ok := l.Installed[name]; ok && col.Replace && old.InstalledAt != "" {
			entry.InstalledAt = old.InstalledAt
		}
		l.Installed[name] = entry
		return nil
	})
	if err != nil {
		if uerr := undo(); uerr != nil {
			return InstallResult{}, errors.Join(err, fmt.Errorf("cannot undo the move of skills/%s: %w", name, uerr))
		}
		return InstallResult{}, err
	}

	if s.commit != "" {
		in.printf("Installed %s (commit %s).\n", name, Sanitize(ShortCommit(s.commit)))
	} else {
		in.printf("Installed %s.\n", name)
	}
	in.printf("Running sessions keep their skill list. Send /new to use the skill now.\n")
	return InstallResult{Name: name, Commit: s.commit, Override: override, Replaced: col.Replace}, nil
}

// stage fetches the source into a new staging folder, selects one skill,
// copies it to the skill folder of the staging, checks it, and scans it in
// strict mode. It removes the staging folder when it returns an error.
func (in *Installer) stage(ctx context.Context, src Source, skillName string) (_ *staged, err error) {
	// --skill cannot go with an index name, so a select hint names the repo.
	again := src.Identifier
	if src.Kind == SourceIndex {
		e, ok, lerr := in.lookup(src.IndexName)
		if lerr != nil {
			return nil, lerr
		}
		if !ok {
			return nil, fmt.Errorf("no skill named %q in the index. See the index with: tether skills list --available", Sanitize(src.IndexName))
		}
		src.Kind = SourceGit
		src.URL, src.Ref, src.Path = e.URL, e.Ref, e.Path
		again = e.URL
	}
	var g Git
	if src.Kind == SourceGit {
		if g, err = in.git(); err != nil {
			return nil, err
		}
	}

	st, err := NewStaging(in.SkillsDir, in.now())
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			st.Remove()
		}
	}()

	s := &staged{st: st, src: src}
	if src.Kind == SourceGit {
		if s.commit, err = g.Clone(ctx, src.URL, src.Ref, st.Repo()); err != nil {
			return nil, err
		}
	} else {
		// CopyTree refuses a symlink as the root, so resolve the folder first.
		real, rerr := filepath.EvalSymlinks(src.URL)
		if rerr != nil {
			return nil, fmt.Errorf("cannot read the local folder %s: %w", Sanitize(src.URL), rerr)
		}
		if err = CopyTree(real, st.Repo()); err != nil {
			return nil, err
		}
		if lg, gerr := in.git(); gerr == nil {
			if c, cerr := lg.HeadCommit(ctx, real); cerr == nil {
				s.commit = c
			}
		}
	}

	root, err := searchRoot(st.Repo(), src.Path)
	if err != nil {
		return nil, err
	}
	found, warns, err := FindSkills(root)
	if err != nil {
		return nil, err
	}
	// A skipped SKILL.md is often the cause of a "no SKILL.md found" error,
	// so the owner sees each one.
	for _, w := range warns {
		p := w.Path
		if rel, rerr := filepath.Rel(root, w.Path); rerr == nil {
			p = filepath.ToSlash(rel)
		}
		in.errorf("warning: %s is skipped: %s\n", Sanitize(p), Sanitize(w.Text))
	}
	sel, err := SelectSkill(found, skillName)
	if err != nil {
		var many *ManySkillsError
		if errors.As(err, &many) {
			many.Source = again
		}
		return nil, err
	}
	if err = CopyTree(filepath.Join(root, filepath.FromSlash(sel.RelDir)), st.Skill()); err != nil {
		return nil, err
	}
	if s.skill, err = ParseSkillFile(filepath.Join(st.Skill(), skillFile)); err != nil {
		return nil, err
	}
	if err = CheckName(s.skill.Name); err != nil {
		return nil, err
	}
	if rel, ok, lerr := FindSymlink(st.Skill()); lerr != nil {
		return nil, fmt.Errorf("cannot check the skill for symlinks: %w", lerr)
	} else if ok {
		return nil, fmt.Errorf("the skill contains a symlink at %s; symlinks are not allowed", Sanitize(rel))
	}
	if rel, ok, nerr := NestedSkill(st.Skill()); nerr != nil {
		return nil, nerr
	} else if ok {
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return nil, fmt.Errorf("the folder of %q contains another skill at %s. Install one skill at a time.",
			s.skill.Name, Sanitize(rel))
	}
	if s.scan, err = ScanDirMode(st.Skill(), ScanStrict); err != nil {
		return nil, err
	}
	if s.hash, s.files, err = ContentHash(st.Skill()); err != nil {
		return nil, err
	}
	s.skillPath = path.Join(src.Path, sel.RelDir)
	if s.skillPath == "." {
		s.skillPath = ""
	}
	if src.Kind == SourceGit {
		s.trust = TrustForURL(src.URL)
	} else {
		s.trust = TrustCommunity
	}
	return s, nil
}

// searchRoot returns the folder in repo where the skill search starts. A
// symlink in the repo can point the path at a folder outside the repo, so
// the resolved path must stay in the resolved repo.
func searchRoot(repo, sub string) (string, error) {
	root := filepath.Join(repo, filepath.FromSlash(sub))
	if sub == "" {
		return root, nil
	}
	realRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", fmt.Errorf("cannot read the fetched source: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("the source has no folder %s", Sanitize(sub))
	}
	if err != nil {
		return "", fmt.Errorf("cannot read the folder %s of the source: %w", Sanitize(sub), err)
	}
	rel, err := filepath.Rel(realRepo, realRoot)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("the folder %s is outside the source", Sanitize(sub))
	}
	return realRoot, nil
}

// report prints the skill, its files, its source, and the scan findings.
// Each string from the skill or the remote goes through Sanitize.
func (in *Installer) report(s *staged) {
	in.printf("Skill: %s — %s\n", Sanitize(s.skill.Name), Sanitize(s.skill.Description))
	var size int64
	for _, rel := range s.files {
		if info, err := os.Lstat(filepath.Join(s.st.Skill(), filepath.FromSlash(rel))); err == nil {
			size += info.Size()
		}
	}
	noun := "files"
	if len(s.files) == 1 {
		noun = "file"
	}
	in.printf("Files: %s (%d %s, %d KB)\n", Sanitize(strings.Join(s.files, ", ")), len(s.files), noun, (size+1023)/1024)
	if s.commit != "" {
		in.printf("Source: %s (commit %s)\n", Sanitize(s.src.Identifier), Sanitize(ShortCommit(s.commit)))
	} else {
		in.printf("Source: %s\n", Sanitize(s.src.Identifier))
	}
	in.printf("%s\n", Sanitize(FormatFindings(s.scan)))
}

// authorize applies the install policy to the scan verdict and asks the
// owner when the policy needs it. The override result is true when the owner
// typed the skill name to pass a dangerous verdict. A no from the owner
// prints "Cancelled." and returns ErrCancelled.
func (in *Installer) authorize(s *staged, yes, force bool, question string, disclaim bool) (override bool, err error) {
	name := s.skill.Name
	terminal := in.Prompt != nil && in.Prompt.Terminal()
	switch Decide(s.trust, s.scan.Verdict, force) {
	case DecideNeedForce:
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return false, errors.New("blocked (community source, caution verdict).\n" +
			"Read the findings. Then run the command again with --force to install.")
	case DecideNeedOverride:
		if !terminal {
			//lint:ignore ST1005 the text is a fixed user message with full sentences.
			return false, errors.New("blocked (dangerous verdict). To install anyway, run the command in a terminal and type the skill name when asked.")
		}
		in.printf("The guard rates this skill DANGEROUS. The loader will not scan it again.\n")
		answer, err := in.Prompt.Ask("Type the skill name to install it anyway:")
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(answer) != name {
			in.printf("Cancelled.\n")
			return false, ErrCancelled
		}
		return true, nil
	}
	if yes {
		return false, nil
	}
	if !terminal {
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return false, errors.New("no terminal for the confirm prompt. Run the command again with --yes to confirm.")
	}
	if disclaim {
		in.printf("%s\nFiles go to: %s/\n", disclaimer, filepath.Join(in.SkillsDir, name))
	}
	ok, err := in.Prompt.Confirm(question)
	if err != nil {
		return false, err
	}
	if !ok {
		in.printf("Cancelled.\n")
		return false, ErrCancelled
	}
	return false, nil
}

// swapIn moves newDir to dst. When replace is true and dst exists, it first
// moves dst to oldDir. When replace is false, a dst that exists is an error:
// the collision check ran before the prompt, and a folder that appeared since
// then is not ours to move. The undo function puts back the state from before
// the call.
func swapIn(dst, newDir, oldDir string, replace bool) (undo func() error, err error) {
	if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) || (err == nil && !replace) {
		if err == nil {
			//lint:ignore ST1005 the text gives the next step as a full sentence.
			return nil, fmt.Errorf("%s appeared during the install. Rename or delete that folder first.", dst)
		}
		// A rename onto a folder that is not empty fails, so a folder that
		// appears after the Lstat is not lost.
		if err := os.Rename(newDir, dst); err != nil {
			return nil, fmt.Errorf("cannot move the skill to %s: %w", dst, err)
		}
		return func() error {
			if err := os.Rename(dst, newDir); err != nil {
				return os.RemoveAll(dst)
			}
			return nil
		}, nil
	} else if err != nil {
		return nil, fmt.Errorf("cannot check the skill folder %s: %w", dst, err)
	}
	if err := os.Rename(dst, oldDir); err != nil {
		return nil, fmt.Errorf("cannot move the old skill %s aside: %w", dst, err)
	}
	if err := os.Rename(newDir, dst); err != nil {
		if rerr := os.Rename(oldDir, dst); rerr != nil {
			return nil, errors.Join(
				fmt.Errorf("cannot move the skill to %s: %w", dst, err),
				fmt.Errorf("cannot put back the old skill from %s: %w", oldDir, rerr))
		}
		return nil, fmt.Errorf("cannot move the skill to %s: %w", dst, err)
	}
	return func() error {
		if err := os.Rename(dst, newDir); err != nil {
			return err
		}
		return os.Rename(oldDir, dst)
	}, nil
}
