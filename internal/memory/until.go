package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// untilLine is the last line of an entry that has an end date.
var untilLine = regexp.MustCompile(`^until (\d{4}-\d{2}-\d{2})$`)

// ParseUntil splits an entry into its body and its end date. The last line of the
// entry, trimmed, must have the form "until YYYY-MM-DD". Else body is the entry,
// day is "", and ok is false. It does not check the calendar.
func ParseUntil(entry string) (body, day string, ok bool) {
	i := strings.LastIndexByte(entry, '\n')
	last := entry[i+1:]
	m := untilLine.FindStringSubmatch(strings.TrimSpace(last))
	if m == nil {
		return entry, "", false
	}
	if i < 0 {
		return "", m[1], true
	}
	return strings.TrimSpace(entry[:i]), m[1], true
}

// ValidUntil checks that day is a date after today and at most 366 days after
// today. Today is the date of now in the zone of now.
func ValidUntil(day string, now time.Time) error {
	_, err := time.ParseInLocation("2006-01-02", day, now.Location())
	if err != nil {
		return fmt.Errorf("the until date %q is not a valid date; use the form YYYY-MM-DD", day)
	}
	today := now.Format("2006-01-02")
	if day <= today {
		return fmt.Errorf("the until date %s is not after today (%s); give a later date, or leave until out", day, today)
	}
	if day > now.AddDate(0, 0, 366).Format("2006-01-02") {
		return fmt.Errorf("the until date %s is more than 366 days after today (%s)", day, today)
	}
	return nil
}

// Expired reports whether the entry has an end date before today. An entry loads
// through its date and stops on the next day.
func Expired(entry, today string) bool {
	_, day, ok := ParseUntil(entry)
	return ok && day < today
}

// LiveEntries returns the entries of target that are not expired. A prompt build
// calls it once, so a session prompt stays the same when an entry expires in the session.
func (s *Store) LiveEntries(target, today string) ([]string, error) {
	entries, err := s.ReadEntries(target)
	if err != nil {
		return nil, err
	}
	live := make([]string, 0, len(entries))
	for _, e := range entries {
		if !Expired(e, today) {
			live = append(live, e)
		}
	}
	return live, nil
}

// ArchiveNote returns the topic of the note that gets the expired entries of target.
func ArchiveNote(target string) string { return "archive-" + target }

// checkUntil applies the until rules of op to content and returns the content to
// store. The caller has trimmed content.
func (s *Store) checkUntil(op Op, content string) (string, error) {
	if op.Until != "" && (op.Action != ActionAdd && op.Action != ActionReplace || strings.TrimSpace(op.Topic) != "") {
		return "", errors.New("until works only for an add or a replace of a memory, user, or rule entry with no topic")
	}
	if op.Action != ActionAdd && op.Action != ActionReplace {
		return content, nil
	}
	last := content[strings.LastIndexByte(content, '\n')+1:]
	if low := strings.ToLower(strings.TrimSpace(last)); strings.HasPrefix(low, "until ") {
		if op.Until != "" {
			return "", errors.New("give the until date once: in until or as the last line of content, not both")
		}
		return content, ValidUntil(strings.TrimSpace(strings.TrimSpace(last)[len("until "):]), s.now())
	}
	if op.Until == "" {
		return content, nil
	}
	if err := ValidUntil(op.Until, s.now()); err != nil {
		return "", err
	}
	return content + "\nuntil " + op.Until, nil
}

// expire moves the expired entries of the tier-1 file at path to the archive note
// of target. It writes the note first, so a failed write never loses an entry.
// rest is the entries that stay, and line is the result line, or "". Call it with
// the lock held.
func (s *Store) expire(ctx context.Context, op Op, path string, entries []string) (rest []string, line string, err error) {
	today := s.now().Format("2006-01-02")
	type moved struct {
		idx  int
		text string
	}
	var gone []moved
	for i, e := range entries {
		if Expired(e, today) {
			gone = append(gone, moved{i, e})
		} else {
			rest = append(rest, e)
		}
	}
	if len(gone) == 0 {
		return entries, "", nil
	}
	names := make([]string, len(gone))
	added := make([]bool, len(gone))
	for i, m := range gone {
		name, _, add, err := s.appendToNote(ArchiveNote(op.Target), op.Target, m.text)
		if err != nil {
			return nil, "", err
		}
		names[i], added[i] = name, add
	}
	if err := writeAtomic(path, strings.Join(rest, Separator)); err != nil {
		return nil, "", err
	}
	for i, m := range gone {
		if !added[i] {
			// The note had the text already. A move row would let Undo take that older
			// note entry away, so the row is a remove from the file. Undo only puts the entry back.
			s.record(ctx, Edit{Target: op.Target, Idx: m.idx, Action: ActionRemove, OldText: m.text, Source: SourceExpiry})
			continue
		}
		s.record(ctx, Edit{Target: op.Target, Idx: m.idx, Dest: noteTarget(names[i]), Action: "expire",
			OldText: m.text, NewText: m.text, Source: SourceExpiry})
	}
	word := "entry"
	if len(gone) > 1 {
		word = "entries"
	}
	return rest, fmt.Sprintf("%d expired %s moved to note %s.", len(gone), word, ArchiveNote(op.Target)), nil
}
