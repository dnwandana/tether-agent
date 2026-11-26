package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"
)

// The sources of a memory change.
const (
	SourceChat   = "chat"
	SourceReview = "review"
	SourceTidy   = "tidy"
	SourceOwner  = "owner"
	SourceCron   = "cron"
	SourceExpiry = "expiry"
)

// Edit is one row of the memory history: one change of one entry.
type Edit struct {
	ID    int64
	Stamp string
	At    time.Time
	// Target is memory, user, rule, or note:<topic>.
	Target string
	// Idx is the index of the entry, or -1 for an add.
	Idx int
	// Dest is memory, user, rule, or note:<topic>. A move sets it.
	Dest      string
	Action    string
	OldText   string
	NewText   string
	Source    string
	SessionID string
	Quote     string
	Outside   []string
	// GroupID is the id of the first row of a two-row change, or 0.
	GroupID  int64
	UndoneBy int64
}

// Block is a fact that the owner deleted. The agent must not save it again.
type Block struct {
	Norm, Text string
	EditID     int64
	At         time.Time
}

// History stores the memory changes and the block list. A nil History records
// nothing, and Undo is not available. Each method reads all rows and closes
// them before it returns, because state.db has one connection.
type History interface {
	Record(ctx context.Context, e Edit) (id int64, err error)
	Get(ctx context.Context, id int64) (Edit, error)
	MarkUndone(ctx context.Context, id, undoID int64) error
	Recent(ctx context.Context, limit int) ([]Edit, error)
	// Blocked reports whether a block row matches norm. An empty norm never
	// matches. Equal texts match. Else the shorter text must have 20 or more
	// runes and be inside the longer text on word boundaries.
	Blocked(ctx context.Context, norm string) (bool, error)
	AddBlock(ctx context.Context, b Block) error
	ClearBlock(ctx context.Context, norm string) error
	DeleteBlockOf(ctx context.Context, editID int64) error
}

// newStamp returns 4 lower-case hex characters from the random source.
func newStamp() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on a supported system. A stamp of zeros still works.
		return "0000"
	}
	return hex.EncodeToString(b[:])
}

// noteTarget returns the Edit target of the note of topic.
func noteTarget(topic string) string { return "note:" + topic }

// record writes one history row and returns its id, or 0 when there is no
// History or the write fails. A failed write only logs, because the file is
// the source of truth. Call it with the lock held, so that the rows keep the
// order of the file writes.
func (s *Store) record(ctx context.Context, e Edit) int64 {
	if s.History == nil {
		return 0
	}
	e.Stamp = newStamp()
	e.At = s.now()
	id, err := s.History.Record(context.WithoutCancel(ctx), e)
	if err != nil {
		slog.Warn("memory: cannot record the change", "action", e.Action, "target", e.Target, "err", err)
		return 0
	}
	s.blockAfter(context.WithoutCancel(ctx), e, id)
	return id
}

// withOp copies the source, the session, the quote, and the outside sources of op into e.
func withOp(op Op, e Edit) Edit {
	e.Source, e.SessionID, e.Quote, e.Outside = op.Source, op.SessionID, op.Quote, op.Outside
	return e
}

// recordIDs records e with the fields of op and returns the id as a list, or nil.
func (s *Store) recordIDs(ctx context.Context, op Op, e Edit) []int64 {
	if id := s.record(ctx, withOp(op, e)); id != 0 {
		return []int64{id}
	}
	return nil
}
