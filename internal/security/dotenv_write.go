package security

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// dotEnvLockWait is the longest wait for the .env lock. Tests make it shorter.
var dotEnvLockWait = 5 * time.Second

// maxSecretBytes is the longest value that SaveDotEnv accepts.
const maxSecretBytes = 4096

// ErrDotEnvLocked is the error of SaveDotEnv when another process holds the .env lock.
//
//lint:ignore ST1005 the text is a fixed user message with full sentences.
var ErrDotEnvLocked = errors.New(".env is locked by another process. Try again.")

//lint:ignore ST1005 the text is a fixed user message with full sentences.
var errSecretValue = errors.New("The value has a line break or is longer than 4096 bytes. Send it again.")

var dotEnvNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// CheckSecretValue returns an error for a value that has a line break or is longer than
// 4096 bytes. The error text goes to the owner.
func CheckSecretValue(value string) error {
	if len(value) > maxSecretBytes || strings.ContainsAny(value, "\r\n") {
		return errSecretValue
	}
	return nil
}

// QuoteDotEnvValue returns the value in the .env syntax of LoadDotEnv. A value with spaces,
// quotes, "#", or a backslash goes in double quotes with escapes.
func QuoteDotEnvValue(v string) string {
	if v == "" || !strings.ContainsAny(v, " \t#\"'\\\n") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(v) + `"`
}

// lockDotEnv takes an exclusive flock on lockPath. It tries again every 10 ms for up to
// dotEnvLockWait. unlock releases the lock and closes the file, and keeps the file.
func lockDotEnv(lockPath string) (unlock func(), err error) {
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf(".env: cannot open the lock file %s: %w", lockPath, err)
	}
	deadline := time.Now().Add(dotEnvLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf(".env: cannot lock %s: %w", lockPath, err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, ErrDotEnvLocked
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// dotEnvLineName returns the variable name of a line, with the same parse as LoadDotEnv.
// It returns "" for a blank line, a comment, or a line with no "=".
func dotEnvLineName(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	name, _, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(name)
}

// setDotEnvLine puts line in the place of the last line of name and removes each earlier
// line of the name. With no line of the name, it adds line at the end. The text ends with
// one newline.
func setDotEnvLine(text, name, line string) string {
	var lines []string
	if text != "" {
		lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	}
	last := -1
	for i, l := range lines {
		if dotEnvLineName(l) == name {
			last = i
		}
	}
	out := make([]string, 0, len(lines)+1)
	for i, l := range lines {
		switch {
		case i == last:
			out = append(out, line)
		case dotEnvLineName(l) == name:
		default:
			out = append(out, l)
		}
	}
	if last < 0 {
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n"
}

// SaveDotEnv sets name=value in the .env file at path. It takes an flock on path+".lock",
// reads the file again, changes or adds one line, and writes a temp file with mode 0600, then
// renames it. When path is a symlink, it writes to the target.
func SaveDotEnv(path, name, value string) error {
	if !dotEnvNameRE.MatchString(name) {
		return fmt.Errorf("%q is not a valid .env name", name)
	}
	if err := CheckSecretValue(value); err != nil {
		return err
	}
	abs := absClean(path)
	unlock, err := lockDotEnv(abs + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	target := RealPath(abs)
	var text string
	switch data, err := os.ReadFile(target); {
	case err == nil:
		text = string(data)
	case errors.Is(err, fs.ErrNotExist):
	default:
		return fmt.Errorf(".env: cannot read %s: %w", target, err)
	}
	out := setDotEnvLine(text, name, name+"="+QuoteDotEnvValue(value))

	tmp, err := os.CreateTemp(filepath.Dir(target), ".env-save-*")
	if err != nil {
		return fmt.Errorf(".env: cannot write a temp file next to %s: %w", target, err)
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf(".env: cannot write %s: %w", target, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.WriteString(out); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf(".env: cannot write %s: %w", target, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf(".env: cannot replace %s: %w", target, err)
	}
	return nil
}
