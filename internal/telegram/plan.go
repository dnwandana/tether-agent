package telegram

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dnwandana/tether-agent/internal/tools"
)

// planMessage returns the text of the plan message for one todo_list call, from the list
// before the call and the list after the call. The text is plain text. An empty text means
// no message. done is true only for the done list, which closes the plan.
func planMessage(before, after []tools.TodoItem) (text string, done bool) {
	if slices.Equal(after, before) || len(after) == 0 {
		return "", false
	}
	openBefore, openAfter := planOpen(before), planOpen(after)
	shares := planSharesPair(before, after)
	newPlan := len(before) == 0 || !openBefore || !shares
	d, total := tools.TodoSummary(after)
	switch {
	case newPlan && openAfter:
		return planList(fmt.Sprintf("📋 Plan %d/%d", d, total), after), false
	case !openAfter && openBefore:
		return planList(fmt.Sprintf("📋 Plan done %d/%d", d, total), after), true
	case !openAfter && (len(before) == 0 || !shares):
		// A new plan that has only closed items gets the done list, not the start list.
		return planList(fmt.Sprintf("📋 Plan done %d/%d", d, total), after), true
	case !openAfter:
		// A finished plan that keeps its items gets no second done list.
		return "", false
	}
	if lines := planChangeLines(before, after); len(lines) > 0 {
		return planFit("", lines), false
	}
	return "", false
}

// planOpen reports whether items has a pending or in_progress item.
func planOpen(items []tools.TodoItem) bool {
	return slices.ContainsFunc(items, func(it tools.TodoItem) bool {
		return it.Status == "pending" || it.Status == "in_progress"
	})
}

// planSharesPair reports whether an (ID, Content) pair of before is in after. A list with
// no shared pair is a new plan, also when the agent uses the same IDs again.
func planSharesPair(before, after []tools.TodoItem) bool {
	type pair struct{ id, content string }
	seen := make(map[pair]bool, len(before))
	for _, it := range before {
		seen[pair{it.ID, it.Content}] = true
	}
	return slices.ContainsFunc(after, func(it tools.TodoItem) bool {
		return seen[pair{it.ID, it.Content}]
	})
}

// planList returns the header and one line for each item. A subtask has two spaces of
// indent for each parent level, as in the /todo reply.
func planList(header string, items []tools.TodoItem) string {
	parents := make(map[string]string, len(items))
	for _, it := range items {
		parents[it.ID] = it.Parent
	}
	lines := make([]string, 0, len(items))
	for _, it := range items {
		indent := strings.Repeat("  ", tools.TodoLevel(parents, it.Parent, len(items)))
		lines = append(lines, indent+planMarker(it.Status)+" "+oneLine(it.Content))
	}
	return planFit(header, lines)
}

// planChangeLines returns the lines for the items that become completed, the items that
// become cancelled, and the new items, in that group order. The n-th item with an ID in
// after matches the n-th item with that ID in before, because an ID such as "?" can occur
// more than once.
func planChangeLines(before, after []tools.TodoItem) []string {
	byID := make(map[string][]tools.TodoItem, len(before))
	for _, it := range before {
		byID[it.ID] = append(byID[it.ID], it)
	}
	seen := make(map[string]int, len(after))
	var completed, cancelled, added []tools.TodoItem
	for _, it := range after {
		n := seen[it.ID]
		seen[it.ID]++
		if n >= len(byID[it.ID]) {
			added = append(added, it)
			continue
		}
		old := byID[it.ID][n]
		switch {
		case it.Status == "completed" && old.Status != "completed":
			completed = append(completed, it)
		case it.Status == "cancelled" && old.Status != "cancelled":
			cancelled = append(cancelled, it)
		}
	}
	doneAfter, total := tools.TodoSummary(after)
	lines := make([]string, 0, len(completed)+len(cancelled)+len(added))
	// The count goes up line by line, so the last line shows the done count after the call.
	for i, it := range completed {
		lines = append(lines, fmt.Sprintf("✅ %d/%d %s", doneAfter-len(completed)+i+1, total, oneLine(it.Content)))
	}
	for _, it := range cancelled {
		lines = append(lines, "🚫 "+oneLine(it.Content))
	}
	for _, it := range added {
		lines = append(lines, "➕ "+oneLine(it.Content))
	}
	return lines
}

// planFit joins header and lines with "\n". When the text is longer than statusTextLimit, it
// gives up the last lines and ends with a "+N more" line. A header that is not empty stays.
func planFit(header string, lines []string) string {
	var head []string
	if header != "" {
		head = []string{header}
	}
	cut := make([]string, len(lines))
	// size[k] is the length of head and cut[:k], with the "\n" separators.
	size := make([]int, len(lines)+1)
	size[0] = UTF16Len(header)
	for i, l := range lines {
		cut[i] = cutUTF16(l, statusTextLimit)
		size[i+1] = size[i] + UTF16Len(cut[i])
		if i > 0 || header != "" {
			size[i+1]++
		}
	}
	for keep := len(cut); ; keep-- {
		n := size[keep]
		var tail []string
		if more := len(cut) - keep; more > 0 {
			tail = []string{fmt.Sprintf("+%d more", more)}
			n += UTF16Len(tail[0])
			if keep > 0 || header != "" {
				n++
			}
		}
		if keep == 0 || n <= statusTextLimit {
			return strings.Join(slices.Concat(head, cut[:keep], tail), "\n")
		}
	}
}
