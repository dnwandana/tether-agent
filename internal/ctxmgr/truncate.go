// Package ctxmgr keeps the model context small: output truncation, token estimates, and compaction.
package ctxmgr

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Truncate returns s when s has max runes or fewer. For a longer s, it first writes the full
// s to spillDir/<id>.txt. It then returns the head (60% of max), a marker with the spill path,
// and the tail (30% of max). The result is always valid UTF-8. A failed spill write is an
// error, because the marker must never point to a file that does not exist.
func Truncate(s string, max int, spillDir string) (string, error) {
	clean := SanitizeUTF8(s)
	total := utf8.RuneCountInString(clean)
	if max <= 0 || total <= max {
		return clean, nil
	}
	path, err := writeSpill(spillDir, s)
	if err != nil {
		return "", err
	}
	head := max * 6 / 10
	tail := max * 3 / 10
	headEnd := runeOffset(clean, head)
	tailStart := runeOffset(clean, total-tail)
	marker := fmt.Sprintf("[… %d chars omitted. Full output: %s. Use read_file with offset to see more.]",
		total-head-tail, path)
	return clean[:headEnd] + "\n" + marker + "\n" + clean[tailStart:], nil
}

// SanitizeUTF8 replaces each run of invalid bytes with U+FFFD, so the JSON to the provider stays valid.
func SanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

// CleanupSpill removes the files in dir that were modified before now minus olderThan.
// A missing dir is not an error.
func CleanupSpill(dir string, olderThan time.Duration, now time.Time) (removed int, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("spill cleanup: read %s: %w", dir, err)
	}
	cutoff := now.Add(-olderThan)
	var errs []error
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // The file was removed after ReadDir.
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("spill cleanup: %w", errors.Join(errs...))
	}
	return removed, nil
}

// writeSpill writes the raw output to a new file in dir and returns the file path.
func writeSpill(dir, s string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("spill: create folder %s: %w", dir, err)
	}
	var rnd [3]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("spill: make file id: %w", err)
	}
	id := time.Now().UTC().Format("20060102-150405") + hex.EncodeToString(rnd[:])
	path := filepath.Join(dir, id+".txt")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("spill: create %s: %w", path, err)
	}
	if _, err := f.WriteString(s); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("spill: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("spill: write %s: %w", path, err)
	}
	return path, nil
}

// runeOffset returns the byte offset of rune n in s.
func runeOffset(s string, n int) int {
	if n <= 0 {
		return 0
	}
	i := 0
	for off := range s {
		if i == n {
			return off
		}
		i++
	}
	return len(s)
}
