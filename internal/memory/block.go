package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/textmatch"
)

// ErrBlocked is the error of a write that the block list refuses.
var ErrBlocked = errors.New("memory: the owner deleted this fact")

// blockedError is ErrBlocked with the day of the delete.
type blockedError struct{ day string }

func (e *blockedError) Error() string {
	day := "an earlier day"
	if e.day != "" {
		day = e.day
	}
	return fmt.Sprintf("The owner deleted this fact on %s. Do not save it again unless the owner says it again.", day)
}

func (e *blockedError) Unwrap() error { return ErrBlocked }

// blockMatch reports whether two normalized texts name the same fact. An empty
// text never matches. The shorter text of a contained match needs 20 runes.
func blockMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	long, short := a, b
	if utf8.RuneCountInString(short) > utf8.RuneCountInString(long) {
		long, short = short, long
	}
	return utf8.RuneCountInString(short) >= 20 && textmatch.ContainsOnWords(long, short)
}

// blockBody returns the text of an entry without its until line. The until line
// is a lifetime, not a part of the fact, so the block list holds the body only.
func blockBody(entry string) string {
	if body, _, ok := ParseUntil(entry); ok {
		return body
	}
	return entry
}

// addsBlock reports whether the row e puts its old text on the block list.
func addsBlock(e Edit) bool {
	switch e.Action {
	case "remove", "undo", "redo":
	default:
		return false
	}
	return e.Source == SourceOwner && e.GroupID == 0 && e.OldText != "" && e.NewText == ""
}

// blockAfter adds a block row for the row e with the id id when addsBlock allows it.
func (s *Store) blockAfter(ctx context.Context, e Edit, id int64) {
	if s.History == nil || id == 0 || !addsBlock(e) {
		return
	}
	body := blockBody(e.OldText)
	b := Block{Norm: textmatch.Normalize(body), Text: body, EditID: id, At: e.At}
	if err := s.History.AddBlock(ctx, b); err != nil {
		slog.Warn("memory: cannot add the block", "edit", id, "err", err)
	}
}

// CheckBlocked refuses an add or a replace of a text that the owner deleted. The
// owner and a chat write with a valid quote pass.
func (s *Store) CheckBlocked(ctx context.Context, op Op) error {
	if s.History == nil || (op.Action != ActionAdd && op.Action != ActionReplace) {
		return nil
	}
	if op.Source == SourceOwner || (op.Source == SourceChat && op.QuoteOK) {
		return nil
	}
	norm := textmatch.Normalize(blockBody(op.Content))
	blocked, err := s.History.Blocked(ctx, norm)
	if err != nil {
		return fmt.Errorf("memory: cannot read the block list: %w", err)
	}
	if !blocked {
		return nil
	}
	return &blockedError{day: s.blockDay(ctx, norm)}
}

// blockDay returns the day of the newest delete that matches norm, or "".
func (s *Store) blockDay(ctx context.Context, norm string) string {
	rows, err := s.History.Recent(ctx, 200)
	if err != nil {
		return ""
	}
	for _, e := range rows {
		if addsBlock(e) && blockMatch(textmatch.Normalize(blockBody(e.OldText)), norm) {
			return e.At.In(time.Local).Format("2006-01-02")
		}
	}
	return ""
}
