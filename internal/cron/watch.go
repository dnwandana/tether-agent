package cron

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The limits of a watch job.
const (
	WatchDiffMaxChars    = 4000      // the cut of the diff in the prompt, in runes
	WatchOutputMaxRunes  = 8000      // the cut of the stored baseline and of the diff inputs
	WatchDiffMaxCells    = 4_000_000 // the limit of old lines × new lines for a diff
	WatchModelRunsPerDay = 24        // the model runs of one watch job in 24 h
)

// watchTooLarge is the text of WatchDiff for a change that is too large to diff.
const watchTooLarge = "The output changed. It is too large to diff."

// watchCutChange tells the model that the output changed only in the part that the cut removed.
const watchCutChange = "The change is in the part of the output that is cut."

// watchContext is the number of equal lines around a change in a hunk.
const watchContext = 3

// ParseWakeGate reads the last stdout line of a watch script. ok is true only when the line is
// a JSON object with the key "wakeAgent" and a boolean value. The rule is Hermes
// _parse_wake_gate (cron/scheduler_prompt.py).
func ParseWakeGate(line string) (wake, ok bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &m); err != nil {
		return false, false
	}
	switch string(bytes.TrimSpace(m["wakeAgent"])) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// diffOp is one line of the full diff. kind is ' ', '-', or '+'.
type diffOp struct {
	kind byte
	text string
}

func watchLines(s string) []string {
	if r := []rune(s); len(r) > WatchOutputMaxRunes {
		s = string(r[:WatchOutputMaxRunes])
	}
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// WatchDiff returns a unified line diff of old and new, cut at WatchDiffMaxChars. Equal lines
// give "". A diff above WatchDiffMaxCells gives watchTooLarge.
func WatchDiff(old, new string) string {
	a, b := watchLines(old), watchLines(new)
	if len(a)*len(b) > WatchDiffMaxCells {
		return watchTooLarge
	}
	// l[i][j] is the length of the longest common subsequence of a[i:] and b[j:].
	l := make([][]int32, len(a)+1)
	for i := range l {
		l[i] = make([]int32, len(b)+1)
	}
	for i, ai := range slices.Backward(a) {
		for j := len(b) - 1; j >= 0; j-- {
			if ai == b[j] {
				l[i][j] = l[i+1][j+1] + 1
			} else if l[i+1][j] >= l[i][j+1] {
				l[i][j] = l[i+1][j]
			} else {
				l[i][j] = l[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case i < len(a) && (j == len(b) || l[i+1][j] >= l[i][j+1]):
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	return TruncateRunes(renderHunks(ops), WatchDiffMaxChars)
}

// renderHunks groups the changes of ops into hunks with watchContext lines around them.
func renderHunks(ops []diffOp) string {
	// oldPos[k] and newPos[k] are the count of old and new lines before ops[k].
	oldPos := make([]int, len(ops)+1)
	newPos := make([]int, len(ops)+1)
	for k, op := range ops {
		oldPos[k+1], newPos[k+1] = oldPos[k], newPos[k]
		if op.kind != '+' {
			oldPos[k+1]++
		}
		if op.kind != '-' {
			newPos[k+1]++
		}
	}
	var out strings.Builder
	k := 0
	for k < len(ops) {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		start := max(k-watchContext, 0)
		last := k // index of the last change in this hunk
		for n := k + 1; n < len(ops) && n-last <= 2*watchContext+1; n++ {
			if ops[n].kind != ' ' {
				last = n
			}
		}
		end := min(last+watchContext+1, len(ops))
		oc, nc := oldPos[end]-oldPos[start], newPos[end]-newPos[start]
		os, ns := oldPos[start]+1, newPos[start]+1
		if oc == 0 {
			os--
		}
		if nc == 0 {
			ns--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", os, oc, ns, nc)
		for _, op := range ops[start:end] {
			out.WriteByte(op.kind)
			out.WriteString(op.text)
			out.WriteByte('\n')
		}
		k = end
	}
	return out.String()
}
