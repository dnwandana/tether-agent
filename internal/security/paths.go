package security

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// maxLinkHops limits the symlinks that realPath follows for one path.
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

// ProtectedPaths returns the protected paths. A path that ends in "/" is a
// folder. A file path also covers the names that start with it and a "-",
// for example state.db-wal and state.db-shm. An empty dataDir or home gives
// no paths for that root.
func ProtectedPaths(dataDir, home string) []string {
	var out []string
	if home != "" {
		out = append(out, filepath.Join(home, ".ssh")+"/")
	}
	if dataDir != "" {
		out = append(out,
			filepath.Join(dataDir, ".env"),
			filepath.Join(dataDir, "config.yaml"),
			filepath.Join(dataDir, "state.db"),
			filepath.Join(dataDir, skillsDir)+"/",
			filepath.Join(dataDir, scriptsDir)+"/",
		)
	}
	return out
}

// CheckWritable returns a *ProtectedError when a write to path touches a
// protected path. It checks the clean absolute path and the real path, after
// it resolves ".." and the symlinks of each existing part. A dangling link
// counts as its target. When allowSkills is true, the skills folder of the
// data dir is writable (skill_manage).
func CheckWritable(path string, protected []string, allowSkills bool) error {
	clean := absClean(path)
	cands := []string{clean}
	if real := realPath(clean); real != clean {
		cands = append(cands, real)
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
		if real := realPath(base); real != base {
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

// covered reports whether path is the protected path base or is in it.
func covered(path, base string, dir bool) bool {
	if path == base {
		return true
	}
	if dir {
		return strings.HasPrefix(path, base+string(filepath.Separator)) || base == string(filepath.Separator)
	}
	return strings.HasPrefix(path, base+"-")
}

// absClean returns path as a clean absolute path.
func absClean(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// realPath returns the real path of the clean absolute path p. The parts
// that do not exist stay as they are. A dangling link resolves to its target.
func realPath(p string) string {
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
				target = filepath.Join(filepath.Dir(p), target)
			}
			p = filepath.Clean(target)
			*hops--
			continue
		}
		dir, base := filepath.Dir(p), filepath.Base(p)
		if dir == p {
			return p
		}
		*hops--
		return filepath.Join(resolveLinks(dir, hops), base)
	}
	return p
}
