package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
)

// The read limits of read_file (Hermes).
const (
	maxReadLines   = 2000
	maxLineRunes   = 2000
	lineCutMarker  = " …[line truncated]"
	binarySniffLen = 8192
)

// readTrackerKey is the Env.Values key of the per-session ReadTracker.
const readTrackerKey = "read_tracker"

// valuesMu guards the creation of the tracker in Env.Values, because
// parallel-safe tools can run at the same time in one session.
var valuesMu sync.Mutex

// fileStamp is the state of a file at the time of a read or a write.
type fileStamp struct {
	mtime time.Time
	size  int64
}

// ReadTracker records the files that the session read, with their mtime and
// size, so that a write can refuse a file that changed after the read.
type ReadTracker struct {
	mu    sync.Mutex
	files map[string]fileStamp
}

// readState returns the ReadTracker of the session in env, and creates it
// on the first call.
func readState(env *Env) *ReadTracker {
	valuesMu.Lock()
	defer valuesMu.Unlock()
	if env.Values == nil {
		env.Values = map[string]any{}
	}
	if rt, ok := env.Values[readTrackerKey].(*ReadTracker); ok {
		return rt
	}
	rt := &ReadTracker{files: map[string]fileStamp{}}
	env.Values[readTrackerKey] = rt
	return rt
}

// MarkRead records that the session saw path in the state of info.
func (rt *ReadTracker) MarkRead(path string, info fs.FileInfo) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.files[path] = fileStamp{mtime: info.ModTime(), size: info.Size()}
}

// CheckFresh returns an error for the model when path exists and the session
// did not read it, or when its mtime or size changed after the read. A path
// that does not exist is fresh.
func (rt *ReadTracker) CheckFresh(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return toolErr("cannot check %s: %v", path, err)
	}
	rt.mu.Lock()
	st, ok := rt.files[path]
	rt.mu.Unlock()
	if !ok {
		return toolErr("%s exists and you did not read it in this session; call read_file first", path)
	}
	if !info.ModTime().Equal(st.mtime) || info.Size() != st.size {
		return toolErr("%s changed since you read it; call read_file again before you write it", path)
	}
	return nil
}

// ReadFileTool is the read_file tool.
type ReadFileTool struct{}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// Name returns "read_file".
func (ReadFileTool) Name() string { return "read_file" }

// Toolset returns the file toolset.
func (ReadFileTool) Toolset() string { return SetFile }

// ParallelSafe returns true, because a read changes nothing.
func (ReadFileTool) ParallelSafe() bool { return true }

// Schema returns the tool schema for the model.
func (ReadFileTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "read_file",
		Description: "Read a text file. The result has numbered lines: the line number, a tab, then the line. " +
			"One call returns at most 2000 lines and 2000 characters per line. When more lines follow, " +
			"the result ends with next_offset; call again with that offset to read more.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "The file path. A relative path starts at the session working folder."},
    "offset": {"type": "integer", "description": "The first line to read, starting at 1. The default is 1."},
    "limit": {"type": "integer", "description": "The maximum number of lines to read. The default and the maximum are 2000."}
  },
  "required": ["path"]
}`),
	}
}

// Run reads the file of the call and marks it as read for the session.
func (ReadFileTool) Run(_ context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a readArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", toolErr("path is required")
	}
	path := resolvePath(env, a.Path)
	offset := max(a.Offset, 1)
	limit := a.Limit
	if limit <= 0 || limit > maxReadLines {
		limit = maxReadLines
	}

	f, err := os.Open(path)
	if err != nil {
		return "", openError(path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", toolErr("cannot read %s: %v", path, err)
	}
	if info.IsDir() {
		return "", toolErr("%s is a folder; use the terminal tool with ls", path)
	}

	br := bufio.NewReaderSize(f, 64*1024)
	if sniff, _ := br.Peek(binarySniffLen); looksBinary(sniff) {
		return "", toolErr("%s: binary file; use the terminal tool with xxd or file", path)
	}

	var b strings.Builder
	lineNo, shown := 0, 0
	more := false
	for {
		line, ok, err := readLine(br)
		if err != nil {
			return "", toolErr("cannot read %s: %v", path, err)
		}
		if !ok {
			break
		}
		lineNo++
		if lineNo < offset {
			continue
		}
		if shown == limit {
			more = true
			break
		}
		fmt.Fprintf(&b, "%6d\t%s\n", lineNo, line)
		shown++
	}
	readState(env).MarkRead(path, info)

	switch {
	case lineNo == 0:
		return fmt.Sprintf("[%s is an empty file]", path), nil
	case shown == 0:
		return "", toolErr("offset %d is past the end of %s (%d lines)", offset, path, lineNo)
	}
	if more {
		fmt.Fprintf(&b, "[more lines follow; next_offset: %d]\n", offset+shown)
	}
	return b.String(), nil
}

// readLine returns the next line of br without its line end, as valid UTF-8
// and cut to maxLineRunes characters. A long line is never held in memory in
// full. ok is false at the end of the input.
func readLine(br *bufio.Reader) (line string, ok bool, err error) {
	var buf []byte
	cut := false
	got := false
	for {
		frag, err := br.ReadSlice('\n')
		if len(frag) > 0 {
			got = true
		}
		if !cut {
			buf = append(buf, frag...)
			// 4 bytes per rune is the worst case; keep a margin for the cut.
			if len(buf) > maxLineRunes*4+4 {
				buf = buf[:maxLineRunes*4+4]
				cut = true
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err == io.EOF {
			if !got {
				return "", false, nil
			}
			break
		}
		if err != nil {
			return "", false, err
		}
		break
	}
	buf = bytes.TrimSuffix(buf, []byte("\n"))
	buf = bytes.TrimSuffix(buf, []byte("\r"))
	s := ctxmgr.SanitizeUTF8(string(buf))
	if cut || utf8.RuneCountInString(s) > maxLineRunes {
		s = s[:runeByteOffset(s, maxLineRunes)] + lineCutMarker
	}
	return s, true, nil
}

// runeByteOffset returns the byte offset of rune n in s, or len(s).
func runeByteOffset(s string, n int) int {
	i := 0
	for off := range s {
		if i == n {
			return off
		}
		i++
	}
	return len(s)
}

// looksBinary reports whether the start of a file looks like binary data: it
// has a NUL byte, or more than 30% control characters (Hermes).
func looksBinary(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	ctrl := 0
	for _, c := range sample {
		if c < 32 && c != '\n' && c != '\r' && c != '\t' && c != '\f' && c != '\v' && c != 0x1b {
			ctrl++
		}
	}
	return ctrl*10 > len(sample)*3
}

// openError returns the model error for a failed open of path.
func openError(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return toolErr("%s does not exist", path)
	case errors.Is(err, fs.ErrPermission):
		return toolErr("no permission to read %s", path)
	}
	return toolErr("cannot open %s: %v", path, err)
}

// WriteFileTool is the write_file tool.
type WriteFileTool struct {
	// Protected returns an error when the agent must not write to path. When
	// it is nil, the tool refuses the paths of security.ProtectedPaths.
	Protected func(path string) error
}

type writeArgs struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

// Name returns "write_file".
func (WriteFileTool) Name() string { return "write_file" }

// Toolset returns the file toolset.
func (WriteFileTool) Toolset() string { return SetFile }

// ParallelSafe returns false, because a write changes a file.
func (WriteFileTool) ParallelSafe() bool { return false }

// Schema returns the tool schema for the model.
func (WriteFileTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "write_file",
		Description: "Write the full content of a file. The tool creates missing parent folders. " +
			"To replace an existing file, call read_file on it first in this session. " +
			"The tool refuses the write when the file changed after that read. Use patch for small edits.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "The file path. A relative path starts at the session working folder."},
    "content": {"type": "string", "description": "The full new content of the file."}
  },
  "required": ["path", "content"]
}`),
	}
}

// Run writes the content of the call to the file.
func (t WriteFileTool) Run(_ context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a writeArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", toolErr("path is required")
	}
	if a.Content == nil {
		return "", toolErr("content is required")
	}
	path := resolvePath(env, a.Path)
	target, err := writeTarget(path, protectedCheck(t.Protected, env))
	if err != nil {
		return "", err
	}
	rt := readState(env)
	if err := rt.CheckFresh(path); err != nil {
		return "", err
	}
	if err := atomicWrite(target, []byte(*a.Content)); err != nil {
		return "", err
	}
	markWritten(rt, path)
	return fmt.Sprintf("wrote %d bytes to %s", len(*a.Content), path), nil
}

// protectedCheck returns custom when it is set. Otherwise it returns the
// check of security.ProtectedPaths for the data dir of env and the home
// folder. skills/ is not writable through it.
func protectedCheck(custom func(string) error, env *Env) func(string) error {
	if custom != nil {
		return custom
	}
	var dataDir string
	if env != nil {
		dataDir = env.DataDir
	}
	home, _ := os.UserHomeDir()
	paths := security.ProtectedPaths(dataDir, home)
	return func(p string) error { return security.CheckWritable(p, paths, false) }
}

// writeTarget checks path for a write and returns the file to replace. It
// follows a symlink, so the write changes the target and keeps the link.
func writeTarget(path string, protected func(string) error) (string, error) {
	target := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		target = real
	}
	if protected != nil {
		for _, p := range []string{path, target} {
			if err := protected(p); err != nil {
				if errors.Is(err, security.ErrProtected) {
					return "", toolErr("%v", err)
				}
				return "", toolErr("cannot write %s: %v", path, err)
			}
		}
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return "", toolErr("%s is a folder", path)
	}
	return target, nil
}

// markWritten records the new state of path after a write by the session.
func markWritten(rt *ReadTracker, path string) {
	if info, err := os.Stat(path); err == nil {
		rt.MarkRead(path, info)
	}
}

// atomicWrite writes data to path through a temp file in the same folder and
// a rename. It creates the parent folders. It keeps the mode of an existing
// file; a new file gets 0644.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return toolErr("cannot create the folder %s: %v", dir, err)
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return toolErr("cannot write %s: %v", path, err)
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return toolErr("cannot write %s: %v", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return toolErr("cannot write %s: %v", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return toolErr("cannot write %s: %v", path, err)
	}
	return nil
}

// expandHome replaces a leading "~" in path with the home folder.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

// resolvePath returns path as a clean absolute path. A relative path starts
// at env.WorkDir.
func resolvePath(env *Env, path string) string {
	path = expandHome(path)
	if !filepath.IsAbs(path) && env != nil && env.WorkDir != "" {
		path = filepath.Join(env.WorkDir, path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}
