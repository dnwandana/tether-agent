package security

import (
	"os"
	"path/filepath"
	"strings"
)

// shellBuiltins holds the bash builtins and keywords. bash runs them itself, so they have
// no file.
var shellBuiltins = func() map[string]bool {
	m := map[string]bool{}
	for w := range strings.FieldsSeq(`. : [ alias bg bind break builtin caller cd command compgen
		complete compopt continue declare dirs disown echo enable eval exec exit export false fc fg
		getopts hash help history jobs kill let local logout mapfile popd printf pushd pwd read
		readarray readonly return set shift shopt source suspend test times trap true type typeset
		ulimit umask unalias unset wait
		if then else elif fi case esac for select while until do done in function time coproc
		{ } ! [[ ]]`) {
		m[w] = true
	}
	return m
}()

// ResolvePrograms returns the real path of the program of each simple command of p, in
// Cmds order. A builtin gives "builtin:<name>", and a command on another host gives
// "remote:<name>". ok is false when the request is not eligible for an Always grant, also when
// the command changes how the shell finds a program.
func ResolvePrograms(p Parsed, workdir, path string) (programs []string, ok bool) {
	if !p.Read || p.lookupChanged {
		return nil, false
	}
	moved := false // a cd, pushd, or popd came before
	for _, c := range p.Cmds {
		if len(c.Argv) == 0 || c.assigned {
			return nil, false
		}
		name := c.Argv[0]
		switch {
		case shellBuiltins[name]:
			programs = append(programs, "builtin:"+name)
			if name == "cd" || name == "pushd" || name == "popd" {
				moved = true
			}
			continue
		case onHost(c.Via):
			programs = append(programs, "remote:"+name)
			continue
		}
		var real string
		var relative, found bool
		if strings.Contains(name, "/") {
			full := name
			relative = !filepath.IsAbs(name)
			if relative {
				if workdir == "" {
					return nil, false
				}
				full = workdir + "/" + name
			}
			real = RealPath(full)
			found = isExecFile(real)
		} else {
			real, relative, found = lookPath(name, workdir, path)
		}
		if !found || (relative && (workdir == "" || moved)) {
			return nil, false
		}
		programs = append(programs, real)
	}
	return programs, true
}

// onHost reports whether via has a host or container wrapper.
func onHost(via []string) bool {
	for _, v := range via {
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		if k, ok := LookupWrapper(f[0]); ok && k == WrapHost {
			return true
		}
	}
	return false
}

// lookPath searches the directories of path for an executable regular file with the name
// and returns its real path. A relative or empty directory means workdir.
func lookPath(name, workdir, path string) (real string, relative bool, ok bool) {
	if path == "" {
		return "", false, false
	}
	for dir := range strings.SplitSeq(path, string(os.PathListSeparator)) {
		rel := !filepath.IsAbs(dir)
		if rel {
			if workdir == "" {
				// The search needs a workdir. Report it as a relative hit that fails.
				return "", true, false
			}
			dir = workdir + "/" + dir
		}
		cand := RealPath(dir + "/" + name)
		if isExecFile(cand) {
			return cand, rel, true
		}
	}
	return "", false, false
}

// isExecFile reports whether p is a regular file with an execute bit.
func isExecFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}
