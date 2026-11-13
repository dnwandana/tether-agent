// The bridge checks these paths against the protected paths and the work folder.

package acp

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/dnwandana/tether-agent/internal/security"
)

// requestPaths returns each path of a permission request in a resolved form. It reads the
// locations, the diff paths, and the raw paths, in that order. A path that starts with "~"
// gets the home folder. A relative path is joined to cwd. An absolute path is resolved
// through its symlinks, and a ".." applies to the real folder before it, as in the kernel. The result has no empty path and no duplicate. A request
// with no path gives nil.
func requestPaths(in permissionInput, cwd string) []string {
	var all []string
	all = append(all, in.Locations...)
	for _, d := range in.Diffs {
		all = append(all, d.Path)
	}
	all = append(all, in.RawPaths...)
	var out []string
	seen := map[string]bool{}
	for _, p := range all {
		if p == "" {
			continue
		}
		if p == "~" || strings.HasPrefix(p, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				p = home + p[1:]
			}
		}
		if !filepath.IsAbs(p) && cwd != "" {
			// No Join here: Join cleans the path, and a ".." must apply after the links.
			p = cwd + string(filepath.Separator) + p
		}
		if filepath.IsAbs(p) {
			p = filepath.Clean(security.RealPath(p))
		} else {
			p = filepath.Clean(p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
