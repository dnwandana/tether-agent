package security

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzMatchAllDangerous checks that the matcher never panics, and that each protected rule
// that matches the first form of the text is in the result.
func FuzzMatchAllDangerous(f *testing.F) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		home = "/home/d"
	}
	AddProtectedPathPatterns(ProtectedPaths(filepath.Join(home, ".tether"), home))
	f.Cleanup(func() { AddProtectedPathPatterns(nil) })
	for _, s := range []string{
		"ls -la",
		"sqlite3 ~/.tether/state* .tables",
		`sqlite3 ~/.tether/state\.db .tables`,
		`sqlite3 ~/.tether/"state".db`,
		`cd ~/.tether && sqlite3 "state".db`,
		"cp /tmp/x ~/.tether/stat?.db",
		"rm -rf ~/.ss*",
		"mv ~/.tether/sk* /tmp",
		"busybox ed ~/.tether/config.yaml",
		`python3 -c "import os; open('~/.tether/.env','w')"`,
		"tee ~/.tether/.env <<< x",
		"[", "[ -f x ]", "a[", `\[`, "~/[", "*/?/[a-", "rm ~/.*",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		got := map[string]bool{}
		for _, p := range MatchAllDangerous(cmd) {
			got[p.ID] = true
		}
		s := prepare(cmd)
		protectedMu.RLock()
		extra, paths := protectedPatterns, protectedPaths
		protectedMu.RUnlock()
		_ = secondForm(s, paths)
		for _, list := range [][]Pattern{extra, TetherDangerousPatterns} {
			for _, p := range list {
				if IsProtectedPatternID(p.ID) && p.matches(s) && !got[p.ID] {
					t.Fatalf("%q: the protected rule %s matches the first form, but the result lacks it", cmd, p.ID)
				}
			}
		}
	})
}
