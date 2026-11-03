package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// guardCache keeps the scan result of each external skill folder with the
// fingerprint of the folder. Load runs on each skill tool call, and a scan
// reads every file of the skill and runs all threat patterns on it. A
// folder with the same fingerprint gets the kept result.
var guardCache = struct {
	sync.Mutex
	m map[string]guardCacheEntry
}{m: map[string]guardCacheEntry{}}

type guardCacheEntry struct {
	fingerprint string
	result      ScanResult
}

// scanDirCached is ScanDir with guardCache. It scans again when a file, a
// folder, or a symlink of dir changed. Without a fingerprint, it always
// scans.
func scanDirCached(dir string) (ScanResult, error) {
	fp, ok := dirFingerprint(dir)
	if !ok {
		return ScanDir(dir)
	}
	guardCache.Lock()
	e, hit := guardCache.m[dir]
	guardCache.Unlock()
	if hit && e.fingerprint == fp {
		return e.result, nil
	}
	// The fingerprint is from before the scan. A change during the scan
	// gives a new fingerprint at the next call, so the next call scans again.
	r, err := ScanDir(dir)
	if err != nil {
		return r, err
	}
	guardCache.Lock()
	guardCache.m[dir] = guardCacheEntry{fingerprint: fp, result: r}
	guardCache.Unlock()
	return r, nil
}

// dirFingerprint returns a hash of the path, the type, the mode, the size,
// the modification time, the inode, and the change time of each entry under
// dir, and of the target of each symlink. A user can set the modification
// time of a file back, but not its change time, so an edit always changes
// the fingerprint. ok is false when the platform gives no inode or change
// time, or when the walk fails.
func dirFingerprint(dir string) (string, bool) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false
	}
	h := sha256.New()
	fmt.Fprintf(h, "%q\n", root)
	ok := true
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		key, has := statKey(fi)
		if !has {
			ok = false
			return fs.SkipAll
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%q %v %d %d %s", rel, fi.Mode(), fi.Size(), fi.ModTime().UnixNano(), key)
		if fi.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, " -> %q", target)
		}
		h.Write([]byte{'\n'})
		return nil
	})
	if err != nil || !ok {
		return "", false
	}
	return hex.EncodeToString(h.Sum(nil)), true
}
