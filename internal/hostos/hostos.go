// Package hostos finds the operating system of the host. The install hints
// need the system, because each system has a different install command.
package hostos

import (
	"os"
	"runtime"
	"strings"
	"sync"
)

// System is an operating system that has its own install commands.
type System string

// The systems that the install hints know.
const (
	Ubuntu System = "ubuntu"
	Debian System = "debian"
	MacOS  System = "macos"
	Other  System = "other"
)

// osRelease is the file that names the Linux distribution.
const osRelease = "/etc/os-release"

var (
	detectOnce sync.Once
	mu         sync.Mutex
	current    System
)

// Detect returns the system for goos. On Linux it reads the ID and ID_LIKE
// fields of /etc/os-release with readFile. A read error gives Other.
func Detect(goos string, readFile func(string) ([]byte, error)) System {
	switch goos {
	case "darwin":
		return MacOS
	case "linux":
	default:
		return Other
	}
	data, err := readFile(osRelease)
	if err != nil {
		return Other
	}
	var id, idLike string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(strings.TrimRight(line, "\r\n"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ID":
			id = unquote(val)
		case "ID_LIKE":
			idLike = unquote(val)
		}
	}
	// ID comes first, so a derivative that names its own ID wins over its parent.
	for _, f := range append([]string{id}, strings.Fields(idLike)...) {
		switch f {
		case "ubuntu":
			return Ubuntu
		case "debian":
			return Debian
		}
	}
	return Other
}

// unquote removes one pair of double or single quotes around v.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// Current returns the system of this host. It reads /etc/os-release on the
// first call only, so the startup of the binary does no file read for it.
func Current() System {
	detectOnce.Do(func() {
		s := Detect(runtime.GOOS, os.ReadFile)
		mu.Lock()
		current = s
		mu.Unlock()
	})
	mu.Lock()
	defer mu.Unlock()
	return current
}

// Set replaces the system that Current returns, for tests. It returns a
// function that puts back the previous value. Use it with t.Cleanup.
func Set(s System) (restore func()) {
	prev := Current() // The detection runs first, so it cannot replace s later.
	mu.Lock()
	current = s
	mu.Unlock()
	return func() {
		mu.Lock()
		current = prev
		mu.Unlock()
	}
}
