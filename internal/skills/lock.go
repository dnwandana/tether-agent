package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// lockVersion is the only lockfile version that this code reads and writes.
const lockVersion = 1

// LockEntry records one skill that the install command put into skills/.
// The JSON key order is the field order, so a diff of the file is easy to read.
type LockEntry struct {
	Source        string   `json:"source"`
	Identifier    string   `json:"identifier"`
	URL           string   `json:"url"`
	Ref           string   `json:"ref"`
	Commit        string   `json:"commit"`
	SkillPath     string   `json:"skill_path"`
	TrustLevel    Trust    `json:"trust_level"`
	ScanVerdict   Verdict  `json:"scan_verdict"`
	OwnerOverride bool     `json:"owner_override"`
	ContentHash   string   `json:"content_hash"`
	InstallPath   string   `json:"install_path"`
	Files         []string `json:"files"`
	InstalledAt   string   `json:"installed_at"`
	UpdatedAt     string   `json:"updated_at"`
}

// LockFile is the content of skills/.hub/lock.json. encoding/json sorts the
// map keys, so the entries are in name order.
type LockFile struct {
	Version   int                  `json:"version"`
	Installed map[string]LockEntry `json:"installed"`
}

// HubDir returns the folder that holds the lockfile, staging, and backups.
func HubDir(skillsDir string) string {
	return filepath.Join(skillsDir, ".hub")
}

func lockPath(skillsDir string) string {
	return filepath.Join(HubDir(skillsDir), "lock.json")
}

// ReadLock returns the lockfile of skillsDir. A missing file is an empty
// lockfile. A bad file is an error and stays as it is, because the lockfile
// is the only record of the installs.
func ReadLock(skillsDir string) (*LockFile, error) {
	path := lockPath(skillsDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &LockFile{Version: lockVersion, Installed: map[string]LockEntry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the lockfile %s: %w", path, err)
	}
	var l LockFile
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("the lockfile %s is not valid JSON: %w", path, err)
	}
	if l.Version != lockVersion {
		return nil, fmt.Errorf("the lockfile %s has version %d, but only version %d is known", path, l.Version, lockVersion)
	}
	if l.Installed == nil {
		l.Installed = map[string]LockEntry{}
	}
	return &l, nil
}

// UpdateLock reads the lockfile, calls fn on it, and writes the result. It
// holds an exclusive flock from the read to the write, so two writers cannot
// lose an entry. When fn returns an error, UpdateLock writes nothing.
func UpdateLock(skillsDir string, fn func(*LockFile) error) error {
	hub := HubDir(skillsDir)
	if err := os.MkdirAll(hub, 0o700); err != nil {
		return fmt.Errorf("cannot make the folder %s: %w", hub, err)
	}
	unlock, err := lockHub(hub)
	if err != nil {
		return err
	}
	defer unlock()
	l, err := ReadLock(skillsDir)
	if err != nil {
		return err
	}
	if err := fn(l); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode the lockfile: %w", err)
	}
	path := lockPath(skillsDir)
	if err := writeFileAtomic(path, append(data, '\n')); err != nil {
		return fmt.Errorf("cannot write the lockfile %s: %w", path, err)
	}
	return nil
}

// lockHub takes a blocking exclusive flock on .hub/lock.json.lock and returns
// the function that releases it.
func lockHub(hub string) (func(), error) {
	path := filepath.Join(hub, "lock.json.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open the lockfile lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("cannot lock %s: %w", path, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// CheckEntry returns an error when a lockfile entry could point a delete or a
// replace at a folder other than skills/<name>. A changed lockfile must not
// send uninstall to another folder.
func CheckEntry(name string, e LockEntry) error {
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("the lockfile entry %q has a name that is not valid", name)
	}
	if e.InstallPath != name {
		return fmt.Errorf("the lockfile entry %q has install_path %q, which is not the entry name", name, e.InstallPath)
	}
	return nil
}

// ContentHash returns the SHA-256 of the files in dir and their sorted
// relative paths. Each regular file adds its slash path, a NUL byte, its
// content, and a NUL byte. The walk does not follow symlinks. A symlink adds
// its target in place of the content, and another special file adds only its
// type, so that a link or a FIFO that the agent adds shows as a change.
func ContentHash(dir string) (string, []string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", nil, fmt.Errorf("cannot list the files of %s: %w", dir, err)
	}
	sort.Strings(files)
	h := sha256.New()
	for _, rel := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Lstat(p)
		if err != nil {
			return "", nil, fmt.Errorf("cannot read %s for the content hash: %w", rel, err)
		}
		var data []byte
		switch {
		case info.Mode().IsRegular():
			data, err = os.ReadFile(p)
		case info.Mode()&fs.ModeSymlink != 0:
			var target string
			target, err = os.Readlink(p)
			data = []byte("symlink:" + target)
		default:
			data = []byte("special:" + info.Mode().Type().String())
		}
		if err != nil {
			return "", nil, fmt.Errorf("cannot read %s for the content hash: %w", rel, err)
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), files, nil
}

// ShortCommit returns the first 7 characters of a commit, the form that the
// reports show.
func ShortCommit(c string) string {
	if len(c) <= 7 {
		return c
	}
	return c[:7]
}
