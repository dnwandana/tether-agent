// Package backup writes the state of the data dir to one archive with no
// secrets in it, and puts an archive back.
package backup

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// FormatVersion is the archive format that this binary writes and reads.
const FormatVersion = 1

// ManifestName is the name of the first tar entry, which describes the archive.
const ManifestName = "manifest.json"

// FileEntry describes one regular file in the archive.
type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
}

// Redacted records what the backup changed to remove secrets.
type Redacted struct {
	ConfigKeys     []string `json:"config_keys"`
	ConfigWarnings []string `json:"config_warnings"`
	StateDB        int      `json:"state_db"`
	Files          int      `json:"files"`
}

// Manifest is the content of manifest.json.
type Manifest struct {
	Format        int         `json:"format"`
	TetherVersion string      `json:"tether_version"`
	CreatedAt     time.Time   `json:"created_at"`
	SchemaVersion int         `json:"schema_version"`
	Files         []FileEntry `json:"files"`
	Redacted      Redacted    `json:"redacted"`
}

// topItems are the top-level names of the allow list. The swap moves these names.
var topItems = []string{"state.db", "config.yaml", "memory", "skills", "scripts"}

// memoryFiles are the only files that the archive can hold under memory/.
var memoryFiles = []string{"memory/memory.md", "memory/user_profile.md"}

// allowed reports whether name is in the allow list. name is a slash path
// with no trailing "/".
func allowed(name string) bool {
	if slices.Contains(topItems, name) || slices.Contains(memoryFiles, name) {
		return true
	}
	return strings.HasPrefix(name, "skills/") || strings.HasPrefix(name, "scripts/")
}

// checkEntryName returns the clean name of a tar entry. It fails when the name
// can point outside the data dir or is not in the allow list.
func checkEntryName(name string) (clean string, err error) {
	// A tar folder entry ends with one "/".
	clean = strings.TrimSuffix(name, "/")
	if clean == "" || path.IsAbs(clean) || path.Clean(clean) != clean || hasDotDot(clean) {
		return "", fmt.Errorf("the archive entry %s is outside the data folder. The archive is not safe", name)
	}
	if clean != ManifestName && !allowed(clean) {
		return "", fmt.Errorf("the archive entry %s is not a tether data file. The archive is not safe", name)
	}
	return clean, nil
}

// hasDotDot reports whether a slash path has a ".." element.
func hasDotDot(name string) bool {
	return slices.Contains(strings.Split(name, "/"), "..")
}

// modeString returns the permission bits of m as four octal digits.
func modeString(m fs.FileMode) string {
	return fmt.Sprintf("%04o", m.Perm())
}
