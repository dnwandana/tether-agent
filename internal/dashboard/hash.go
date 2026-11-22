package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// SourceHash returns the SHA-256, in hex, of the page sources under web/ of repoRoot.
// make web writes it to dist/.source-hash, and a test compares the two. Test files and
// generated types do not change the built page, so they are not part of the hash.
func SourceHash(repoRoot string) (string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z", "--cached", "--others",
		"--exclude-standard", "--", "web").Output()
	if err != nil {
		return "", fmt.Errorf("dashboard: list the web sources: %w", err)
	}
	seen := map[string]bool{}
	var paths []string
	for p := range strings.SplitSeq(string(out), "\x00") {
		if p == "" || seen[p] || strings.HasSuffix(p, ".test.ts") || strings.HasSuffix(p, ".gen.ts") {
			continue
		}
		skip := false
		for seg := range strings.SplitSeq(p, "/") {
			if seg == "node_modules" {
				skip = true
			}
		}
		if skip {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Strings(paths)
	outer := sha256.New()
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(p)))
		if os.IsNotExist(err) {
			continue // git still tracks a deleted file
		}
		if err != nil {
			return "", fmt.Errorf("dashboard: read the web source %s: %w", p, err)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(outer, "%s\x00%s\n", p, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(outer.Sum(nil)), nil
}
