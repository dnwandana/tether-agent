package security

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrNoProgram is the error of ResolvePinTargets when a matched program, or its shebang
// interpreter, cannot be found. Its text is the withheld reason.
var ErrNoProgram = errors.New("the program cannot be found on PATH")

// maxPinBytes is the largest program file that a pin hashes.
const maxPinBytes = 256 << 20

// maxPinCache is the largest count of entries in the hash cache.
const maxPinCache = 256

// pinHashReads counts the program files that hashProgram reads. Tests read it.
var pinHashReads atomic.Int64

var (
	pinCacheMu sync.Mutex
	pinCache   = map[string]string{}
)

// ResolvePinTargets returns the real path and the sha256 of the program of each part in
// parts, and of the shebang interpreter of that program. path is the PATH of the child env.
// It returns ErrNoProgram when a program or an interpreter cannot be found.
func ResolvePinTargets(p Parsed, parts []int, workdir, path string) ([]PinTarget, error) {
	programs, ok := ResolvePrograms(p, workdir, path)
	if !ok {
		return nil, ErrNoProgram
	}
	var out []PinTarget
	seen := map[string]bool{}
	add := func(real string, interp bool) error {
		if seen[real] {
			return nil
		}
		sum, err := hashProgram(real)
		if err != nil {
			return err
		}
		seen[real] = true
		out = append(out, PinTarget{Path: real, SHA256: sum, Interpreter: interp})
		return nil
	}
	for _, i := range parts {
		if i < 0 || i >= len(programs) {
			return nil, ErrNoProgram
		}
		real := programs[i]
		if strings.HasPrefix(real, "builtin:") || strings.HasPrefix(real, "remote:") {
			return nil, ErrNoProgram
		}
		if err := add(real, false); err != nil {
			return nil, err
		}
		word, viaEnv, ok, err := shebang(real)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var interp string
		if viaEnv {
			found, _, ok := lookPath(word, workdir, path)
			if !ok {
				return nil, ErrNoProgram
			}
			interp = found
		} else {
			if !filepath.IsAbs(word) {
				return nil, ErrNoProgram
			}
			interp = RealPath(word)
			if !isExecFile(interp) {
				return nil, ErrNoProgram
			}
		}
		if err := add(interp, true); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// shebang returns the interpreter word of the file at path: the program after env, or the
// first word. ok is false when the file does not start with "#!".
func shebang(path string) (interp string, viaEnv bool, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, false, fmt.Errorf("the program %s cannot be read: %w", path, err)
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", false, false, fmt.Errorf("the program %s cannot be read: %w", path, err)
	}
	buf = buf[:n]
	if !bytes.HasPrefix(buf, []byte("#!")) {
		return "", false, false, nil
	}
	line := buf[2:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	words := strings.Fields(string(line))
	if len(words) == 0 {
		return "", false, false, nil
	}
	if filepath.Base(words[0]) != "env" {
		return words[0], false, true, nil
	}
	for _, w := range words[1:] {
		if strings.HasPrefix(w, "-") || strings.Contains(w, "=") {
			continue
		}
		return w, true, true, nil
	}
	return "", false, false, nil
}

// hashProgram returns the sha256 hex of the file at path. It refuses a file over 256 MB,
// and keeps the hash in a cache keyed by the stat of the file.
func hashProgram(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("the program %s cannot be read: %w", path, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("the program %s cannot be read: %w", path, err)
	}
	if fi.Size() > maxPinBytes {
		return "", fmt.Errorf("the program %s is larger than 256 MB", path)
	}
	key, cacheable := pinStatKey(fi)
	if cacheable {
		key += ":" + strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
		pinCacheMu.Lock()
		sum, hit := pinCache[key]
		pinCacheMu.Unlock()
		if hit {
			return sum, nil
		}
	}
	pinHashReads.Add(1)
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("the program %s cannot be read: %w", path, err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if cacheable {
		pinCacheMu.Lock()
		if len(pinCache) >= maxPinCache {
			clear(pinCache)
		}
		pinCache[key] = sum
		pinCacheMu.Unlock()
	}
	return sum, nil
}
