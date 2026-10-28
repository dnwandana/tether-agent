package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Decision is the result of the install policy for one scan verdict.
type Decision int

const (
	// DecideAllow lets the install go on to the confirm prompt.
	DecideAllow Decision = iota + 1
	// DecideNeedForce stops the install until the owner adds --force.
	DecideNeedForce
	// DecideNeedOverride stops the install until the owner types the skill
	// name at a terminal. No flag gives this override.
	DecideNeedOverride
)

// Decide returns the install decision for a trust level and a verdict.
// A dangerous verdict always needs the typed-name override, so force has no
// effect on it. Force passes only a verdict that Policy blocks, which is a
// caution verdict from a community source.
func Decide(trust Trust, v Verdict, force bool) Decision {
	if v != VerdictSafe && v != VerdictCaution {
		return DecideNeedOverride
	}
	if Policy(trust, v) == ActionAllow {
		return DecideAllow
	}
	if force {
		return DecideAllow
	}
	return DecideNeedForce
}

// CheckName returns an error when name is not a valid install name. The
// install folder takes this name, so a display name such as "Convex Best
// Practices" is refused.
func CheckName(name string) error {
	return validName(name)
}

// FindSymlink returns the slash path, relative to dir, of the first symlink
// in the tree in walk order. The walk uses Lstat, so it does not follow a
// link. A link can point outside the skill folder, so install refuses it.
func FindSymlink(dir string) (rel string, ok bool, err error) {
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		r, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		rel, ok = filepath.ToSlash(r), true
		return fs.SkipAll
	})
	if err != nil {
		return "", false, err
	}
	return rel, ok, nil
}

// Collision tells how an install name relates to the skills that exist.
type Collision struct {
	// Replace is true when the lockfile lists the name and the owner gave
	// --force, so the install replaces the installed copy.
	Replace bool
	// Warning is text for the owner. It is empty when there is nothing to
	// tell.
	Warning string
}

// CheckCollision checks an install name against the bootstrap skills, the
// lockfile, the skills folder, and the loaded skills. An error stops the
// install. Only a lockfile entry can be replaced, and only with force. A
// folder that the lockfile does not list belongs to the owner or the agent,
// so no flag lets the install write over it.
func CheckCollision(name, skillsDir string, lock *LockFile, loaded []Skill, force bool) (Collision, error) {
	var c Collision
	if isBootstrapName(name) {
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return c, fmt.Errorf("%q is the name of a bootstrap skill. Install a skill with another name.", name)
	}
	if lock != nil {
		if e, ok := lock.Installed[name]; ok {
			if !force {
				//lint:ignore ST1005 the text is a fixed user message with full sentences.
				return c, fmt.Errorf("%q is already installed from %s. Use tether skills update %s, or run the install again with --force.",
					name, e.Identifier, name)
			}
			c.Replace = true
		}
	}
	if !c.Replace {
		exists, err := pathExists(filepath.Join(skillsDir, name))
		if err != nil {
			return Collision{}, err
		}
		if exists || hasLocalSkill(loaded, name) {
			//lint:ignore ST1005 the text is a fixed user message with full sentences.
			return Collision{}, fmt.Errorf("skills/%s already exists and the lockfile does not list it. Rename or delete that folder first.", name)
		}
	}
	for _, s := range loaded {
		if s.External && s.Name == name {
			c.Warning = fmt.Sprintf("The installed copy of %q now wins over the external copy at %s.", name, s.Path)
			break
		}
	}
	return c, nil
}

// pathExists reports whether path exists. It uses Lstat, so a dangling
// symlink also counts.
func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("cannot check the skill folder %s: %w", path, err)
}

// hasLocalSkill reports whether a loaded skill that is not external has the
// name. Such a skill can live in a folder with another name.
func hasLocalSkill(loaded []Skill, name string) bool {
	for _, s := range loaded {
		if !s.External && s.Name == name {
			return true
		}
	}
	return false
}
