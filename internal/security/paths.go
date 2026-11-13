package security

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// ErrProtected marks the error of CheckWritable for a protected path.
var ErrProtected = errors.New("protected path")

// skillsDir is the name of the skills folder in the data dir. Only
// skill_manage can write in it.
const skillsDir = "skills"

// scriptsDir is the name of the cron scripts folder in the data dir. A cron
// script job runs with no approval, so no tool can write in it. Only the user
// can put a script there.
const scriptsDir = "scripts"

// backupsDir is the name of the backup archive folder in the data dir. No
// tool can write in it. A restore of an archive installs its skills and
// scripts, so an agent must not plant a crafted archive there.
const backupsDir = "backups"

// memoryDir is the memory folder in the data dir. Its two tier-1 files go into
// the system prompt, so only the memory tool, with its threat scan, writes them.
const memoryDir = "memory"

// maxLinkHops limits the symlinks that RealPath follows for one path.
const maxLinkHops = 40

// ProtectedError is the error of CheckWritable. Its text is the text that
// the model sees.
type ProtectedError struct {
	Path string
}

// Error returns the refusal text for the model.
func (e *ProtectedError) Error() string {
	return "refused: " + e.Path + " is a protected path (see SECURITY.md)"
}

// Is makes errors.Is(err, ErrProtected) true.
func (e *ProtectedError) Is(target error) bool { return target == ErrProtected }

// homeStartupFiles are the files in the home folder that a shell, X, vim, git,
// or a package tool reads at start or for each call. A write to one of them runs
// code at the next login, shell, or editor, or changes the credentials of a tool.
// The shell rules of the approval gate already protect most of them, so the file
// tools must not write them with no approval.
var homeStartupFiles = []string{
	".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".bash_aliases", ".profile",
	".zshrc", ".zshenv", ".zprofile", ".zlogin", ".zlogout",
	".xinitrc", ".xprofile", ".vimrc",
	".netrc", ".pgpass", ".npmrc", ".pypirc", ".gitconfig",
}

// homeStartupDirs are the folders in the home folder that start programs, that
// configure a shell or git, or that hold credentials. A credential file can also
// start a program, for example credential_process in ~/.aws/config.
// ~/.local/bin and .git stay writable, because a refusal there blocks common
// requests such as the install of a tool or of a git hook.
var homeStartupDirs = []string{
	".config/systemd", ".config/autostart", ".config/environment.d", ".config/fish",
	".config/git", "Library/LaunchAgents",
	".aws", ".gnupg", ".kube", ".docker", ".azure", ".config/gh", ".config/gcloud",
}

// ProtectedPaths returns the protected paths. A path that ends in "/" is a
// folder. A file path also covers the names that start with it and a "-",
// for example state.db-wal and state.db-shm. An empty dataDir or home gives
// no paths for that root.
func ProtectedPaths(dataDir, home string) []string {
	var out []string
	if home != "" {
		out = append(out, filepath.Join(home, ".ssh")+"/")
		for _, f := range homeStartupFiles {
			out = append(out, filepath.Join(home, f))
		}
		for _, d := range homeStartupDirs {
			out = append(out, filepath.Join(home, filepath.FromSlash(d))+"/")
		}
	}
	if dataDir != "" {
		out = append(out,
			filepath.Join(dataDir, ".env"),
			filepath.Join(dataDir, "config.yaml"),
			filepath.Join(dataDir, "state.db"),
			filepath.Join(dataDir, "state.db.lock"),
			filepath.Join(dataDir, memoryDir, "memory.md"),
			filepath.Join(dataDir, memoryDir, "user_profile.md"),
			filepath.Join(dataDir, skillsDir)+"/",
			filepath.Join(dataDir, scriptsDir)+"/",
			filepath.Join(dataDir, backupsDir)+"/",
		)
	}
	return out
}

// CheckWritable returns a *ProtectedError when a write to path touches a
// protected path. It checks the clean absolute path and the real path. The
// real path resolves the symlinks of each existing part, and a ".." applies to
// the real folder before it. A dangling link
// counts as its target. When allowSkills is true, the skills folder of the
// data dir is writable (skill_manage).
func CheckWritable(path string, protected []string, allowSkills bool) error {
	clean := absClean(path)
	cands := []string{clean}
	// The real path of the raw path is the file that the kernel opens. The real path of
	// the clean path is also checked, because it costs little.
	for _, real := range []string{RealPath(rawAbs(path)), RealPath(clean)} {
		if !slices.Contains(cands, real) {
			cands = append(cands, real)
		}
	}
	for _, p := range protected {
		if p == "" {
			continue
		}
		dir := strings.HasSuffix(p, "/")
		base := absClean(strings.TrimSuffix(p, "/"))
		if dir && allowSkills && filepath.Base(base) == skillsDir {
			continue
		}
		bases := []string{base}
		if real := RealPath(base); real != base {
			bases = append(bases, real)
		}
		for _, c := range cands {
			for _, b := range bases {
				if covered(c, b, dir) {
					return &ProtectedError{Path: path}
				}
			}
		}
	}
	return nil
}

// pathFold is the Unicode case fold of the path check. It is stateless and
// safe for concurrent use.
var pathFold = cases.Fold()

// foldPath returns p in NFC form with Unicode case folding. APFS ignores
// case and the normalization form, so the check must ignore them too.
func foldPath(p string) string {
	return pathFold.String(norm.NFC.String(p))
}

// covered reports whether path is the protected path base or is in it. It
// compares the folded forms, so a change of case or of the Unicode
// normalization form does not get past the check.
func covered(path, base string, dir bool) bool {
	path, base = foldPath(path), foldPath(base)
	if path == base {
		return true
	}
	if dir {
		return strings.HasPrefix(path, base+string(filepath.Separator)) || base == string(filepath.Separator)
	}
	return strings.HasPrefix(path, base+"-")
}

// splitLast splits p at its last separator. Unlike filepath.Dir, it does not clean the
// folder part, because a ".." in it must apply after the symlinks before it. The folder of
// the root is the root.
func splitLast(p string) (dir, base string) {
	sep := string(filepath.Separator)
	trimmed := strings.TrimRight(p, sep)
	if trimmed == "" {
		return p, ""
	}
	i := strings.LastIndex(trimmed, sep)
	switch {
	case i < 0:
		return ".", trimmed
	case i == 0:
		return sep, trimmed[1:]
	}
	return trimmed[:i], trimmed[i+1:]
}

// rawAbs returns path as an absolute path that is not cleaned. A relative path gets the
// working folder in front.
func rawAbs(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if wd, err := os.Getwd(); err == nil {
		return wd + string(filepath.Separator) + path
	}
	return path
}

// absClean returns path as a clean absolute path.
func absClean(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// RealPath returns the real path of the absolute path p. It resolves the symlinks of each
// existing part. A ".." part applies to the real folder before it, as in the kernel, so do
// not clean p before the call. The parts that do not exist stay as they are. A dangling
// link resolves to its target.
func RealPath(p string) string {
	hops := maxLinkHops
	return resolveLinks(p, &hops)
}

func resolveLinks(p string, hops *int) string {
	for *hops > 0 {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return p
			}
			if !filepath.IsAbs(target) {
				dir, _ := splitLast(p)
				target = resolveLinks(dir, hops) + string(filepath.Separator) + target
			}
			p = target
			*hops--
			continue
		}
		dir, base := splitLast(p)
		if dir == p {
			return p
		}
		*hops--
		return filepath.Join(resolveLinks(dir, hops), base)
	}
	return p
}
