package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// Span is a byte range [Start, End) in a file content.
type Span struct{ Start, End int }

// diffContext is the number of unchanged lines around each diff hunk.
const diffContext = 3

// maxLCSCells limits the work of the line LCS in UnifiedDiff. A larger
// changed region shows as one block of removed and added lines.
const maxLCSCells = 4_000_000

// PatchTool is the patch tool.
type PatchTool struct {
	// Protected returns an error when the agent must not write to path. When
	// it is nil, the tool refuses the paths of security.ProtectedPaths.
	Protected func(path string) error
}

type patchArgs struct {
	Path       string  `json:"path"`
	OldString  *string `json:"old_string"`
	NewString  *string `json:"new_string"`
	ReplaceAll bool    `json:"replace_all"`
}

// Name returns "patch".
func (PatchTool) Name() string { return "patch" }

// Toolset returns the file toolset.
func (PatchTool) Toolset() string { return SetFile }

// ParallelSafe returns false, because a patch changes a file.
func (PatchTool) ParallelSafe() bool { return false }

// Schema returns the tool schema for the model.
func (PatchTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "patch",
		Description: "Replace old_string with new_string in a file, and return a unified diff of the change. " +
			"The match first tries the exact text, then tolerates whitespace and indentation drift per line. " +
			"After a drift match, new_string gets the indentation of the file. When old_string matches more " +
			"than one place, the tool changes nothing and lists the lines; add context or set replace_all.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "The file path. A relative path starts at the session working folder."},
    "old_string": {"type": "string", "description": "The text to replace. Include enough lines to make it unique."},
    "new_string": {"type": "string", "description": "The replacement text."},
    "replace_all": {"type": "boolean", "description": "Replace every match. The default is false."}
  },
  "required": ["path", "old_string", "new_string"]
}`),
	}
}

// Run applies the patch of the call to the file. A patch counts as a read
// and a write for the read tracker of the session.
func (t PatchTool) Run(_ context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a patchArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", toolErr("path is required")
	}
	if a.OldString == nil || a.NewString == nil {
		return "", toolErr("old_string and new_string are required")
	}
	path := resolvePath(env, a.Path)
	target, err := writeTarget(path, protectedCheck(t.Protected, env))
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) {
		return "", toolErr("%s does not exist; use write_file to create a file", path)
	}
	if err != nil {
		return "", openError(path, err)
	}
	if looksBinary(data[:min(len(data), binarySniffLen)]) {
		return "", toolErr("%s: binary file; patch works on text files only", path)
	}
	before := string(data)
	after, n, err := applyPatch(before, *a.OldString, *a.NewString, a.ReplaceAll)
	if err != nil {
		return "", fmt.Errorf("%w (file %s)", err, path)
	}
	if err := atomicWrite(target, []byte(after)); err != nil {
		return "", err
	}
	markWritten(readState(env), path)
	word := "replacement"
	if n != 1 {
		word = "replacements"
	}
	return fmt.Sprintf("patched %s (%d %s)\n%s", path, n, word, UnifiedDiff(path, before, after)), nil
}

// applyPatch replaces old with new in content and returns the new content
// and the number of replacements. More than one match is an error unless
// replaceAll is true. After a drift match, new gets the indentation of the
// matched region. On an error, applyPatch returns content unchanged.
func applyPatch(content, old, new string, replaceAll bool) (string, int, error) {
	if old == new {
		return content, 0, toolErr("old_string and new_string are identical; nothing to change")
	}
	spans, err := FuzzyFind(content, old)
	if err != nil {
		return content, 0, err
	}
	if len(spans) > 1 && !replaceAll {
		return content, 0, toolErr("old_string matches %d places (lines %s); add more context to make it unique, or set replace_all",
			len(spans), startLines(content, spans))
	}
	var b strings.Builder
	last := 0
	for _, s := range spans {
		region := content[s.Start:s.End]
		repl := new
		if region != old {
			repl = reindent(region, old, new)
		}
		b.WriteString(content[last:s.Start])
		b.WriteString(repl)
		last = s.End
	}
	b.WriteString(content[last:])
	return b.String(), len(spans), nil
}

// startLines returns the unique 1-based start lines of spans, joined by ", ".
func startLines(content string, spans []Span) string {
	var out []string
	prev := -1
	for _, s := range spans {
		n := strings.Count(content[:s.Start], "\n") + 1
		if n != prev {
			out = append(out, strconv.Itoa(n))
			prev = n
		}
	}
	return strings.Join(out, ", ")
}

// FuzzyFind returns the non-overlapping places of old in content. It first
// tries an exact match. When there is none, it compares whole lines after it
// trims each line and collapses runs of spaces and tabs. A line window
// matches only when every line matches, and a blank line matches only a blank
// line. With no match, the error names the most similar line.
func FuzzyFind(content, old string) (matches []Span, err error) {
	if old == "" {
		return nil, toolErr("old_string is empty")
	}
	for i := 0; i <= len(content)-len(old); {
		j := strings.Index(content[i:], old)
		if j < 0 {
			break
		}
		matches = append(matches, Span{Start: i + j, End: i + j + len(old)})
		i += j + len(old)
	}
	if len(matches) > 0 {
		return matches, nil
	}

	trailingNL := strings.HasSuffix(old, "\n")
	oldLines := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	normOld := make([]string, len(oldLines))
	allBlank := true
	for i, l := range oldLines {
		normOld[i] = normalizeLine(l)
		if normOld[i] != "" {
			allBlank = false
		}
	}
	lines := splitLines(content)
	normContent := make([]string, len(lines))
	for i, l := range lines {
		normContent[i] = normalizeLine(content[l.Start:l.End])
	}
	if !allBlank {
		for i := 0; i+len(normOld) <= len(lines); {
			if !windowEqual(normContent[i:i+len(normOld)], normOld) {
				i++
				continue
			}
			end := lines[i+len(normOld)-1].End
			if trailingNL && end < len(content) && content[end] == '\n' {
				end++
			}
			matches = append(matches, Span{Start: lines[i].Start, End: end})
			i += len(normOld)
		}
	}
	if len(matches) > 0 {
		return matches, nil
	}
	return nil, noMatchError(normContent, normOld, lines, content)
}

// windowEqual reports whether the two line slices are equal.
func windowEqual(a, b []string) bool {
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// splitLines returns the byte range of each line of s, without the "\n".
// A final "\n" does not start an extra line.
func splitLines(s string) []Span {
	var out []Span
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, Span{Start: start, End: i})
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, Span{Start: start, End: len(s)})
	}
	return out
}

// normalizeLine trims line and collapses each run of spaces and tabs to one space.
func normalizeLine(line string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
}

// noMatchError returns the no-match error with the content line that is most
// similar to the first non-blank line of old.
func noMatchError(normContent, normOld []string, lines []Span, content string) error {
	first := ""
	for _, l := range normOld {
		if l != "" {
			first = l
			break
		}
	}
	best, bestScore := -1, 0.0
	for i, l := range normContent {
		if s := similarity(l, first); s > bestScore {
			best, bestScore = i, s
		}
	}
	if best < 0 || bestScore < 0.3 {
		return toolErr("no match for old_string, and no line is similar; call read_file to see the current text")
	}
	text := []rune(strings.TrimSpace(content[lines[best].Start:lines[best].End]))
	if len(text) > 200 {
		text = append(text[:200], '…')
	}
	return toolErr("no match for old_string; the most similar line is line %d: %s", best+1, string(text))
}

// similarity returns the length of the common prefix plus the common suffix
// of a and b, divided by the length of the longer one. It is 1 for equal
// strings and 0 when they share no prefix or suffix.
func similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	n := max(len(a), len(b))
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	return float64(p+s) / float64(n)
}

// reindent shifts new so that it has the indentation of the file region
// instead of the indentation of old (Hermes _reindent_replacement). The base
// indent is the leading whitespace of the first non-blank line. Each non-blank
// line of new keeps its indent relative to the base of old. Blank lines stay
// as they are.
func reindent(region, old, new string) string {
	oldIndent, ok1 := firstIndent(old)
	fileIndent, ok2 := firstIndent(region)
	if new == "" || !ok1 || !ok2 || oldIndent == fileIndent {
		return new
	}
	lines := strings.Split(new, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, oldIndent) {
			lines[i] = fileIndent + line[len(oldIndent):]
		} else {
			lines[i] = fileIndent + strings.TrimLeft(line, " \t")
		}
	}
	return strings.Join(lines, "\n")
}

// firstIndent returns the leading whitespace of the first non-blank line of s.
func firstIndent(s string) (string, bool) {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line[:len(line)-len(strings.TrimLeft(line, " \t"))], true
		}
	}
	return "", false
}

// diffOp is one line of a line diff: ' ' keeps, '-' removes, '+' adds.
type diffOp struct {
	kind byte
	text string
}

// UnifiedDiff returns a unified diff from before to after with 3 lines of
// context. It is empty when the texts have the same lines.
func UnifiedDiff(path, before, after string) string {
	a, b := diffLines(before), diffLines(after)
	ops := lineDiff(a, b)
	changed := false
	for _, op := range ops {
		if op.kind != ' ' {
			changed = true
			break
		}
	}
	if !changed {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", strings.TrimPrefix(path, "/"), strings.TrimPrefix(path, "/"))

	// aLine and bLine are the 0-based line numbers before op i.
	aLine := make([]int, len(ops)+1)
	bLine := make([]int, len(ops)+1)
	for i, op := range ops {
		aLine[i+1], bLine[i+1] = aLine[i], bLine[i]
		if op.kind != '+' {
			aLine[i+1]++
		}
		if op.kind != '-' {
			bLine[i+1]++
		}
	}
	i := 0
	for i < len(ops) {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(i-diffContext, 0)
		end := i
		// Extend the hunk while the next change is within 2*context lines.
		for {
			for end < len(ops) && ops[end].kind != ' ' {
				end++
			}
			next := end
			for next < len(ops) && ops[next].kind == ' ' {
				next++
			}
			if next < len(ops) && next-end <= 2*diffContext {
				end = next
				continue
			}
			end = min(end+diffContext, len(ops))
			break
		}
		aCount := aLine[end] - aLine[start]
		bCount := bLine[end] - bLine[start]
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(aLine[start], aCount), hunkRange(bLine[start], bCount))
		for _, op := range ops[start:end] {
			out.WriteByte(op.kind)
			out.WriteString(op.text)
			out.WriteByte('\n')
		}
		i = end
	}
	return out.String()
}

// hunkRange formats the start and count of one side of a hunk header.
func hunkRange(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}

// diffLines splits s into lines. A final "\n" does not start an extra line.
func diffLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lineDiff returns the edit script from a to b. It strips the common head
// and tail, then runs an LCS on the rest.
func lineDiff(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var ops []diffOp
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{' ', l})
	}
	ops = append(ops, lcsDiff(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{' ', l})
	}
	return ops
}

// lcsDiff returns a minimal edit script from a to b. When the table would be
// too large, it removes all of a and adds all of b.
func lcsDiff(a, b []string) []diffOp {
	var ops []diffOp
	if len(a)*len(b) > maxLCSCells {
		for _, l := range a {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{'+', l})
		}
		return ops
	}
	// dp[i][j] is the LCS length of a[i:] and b[j:].
	w := len(b) + 1
	dp := make([]int32, (len(a)+1)*w)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i*w+j] = dp[(i+1)*w+j+1] + 1
			} else {
				dp[i*w+j] = max(dp[(i+1)*w+j], dp[i*w+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case dp[(i+1)*w+j] >= dp[i*w+j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}
