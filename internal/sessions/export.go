package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// exportLine is one line of a JSONL export.
type exportLine struct {
	ID           int64        `json:"id"`
	Role         string       `json:"role"`
	Content      string       `json:"content"`
	ToolCalls    []exportCall `json:"tool_calls"`
	ToolCallID   string       `json:"tool_call_id"`
	ToolName     string       `json:"tool_name"`
	Model        string       `json:"model"`
	InputTokens  int          `json:"input_tokens"`
	OutputTokens int          `json:"output_tokens"`
	Timestamp    string       `json:"timestamp"`
}

// exportCall is one tool call in an export line. Arguments is the raw JSON text.
type exportCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ExportJSONL writes the messages of the session to w as JSONL, one object for each message,
// and returns the number of lines. It reads and writes one row at a time. The content and
// the tool arguments go through s.Redactor. The error wraps ErrNotFound for an unknown id.
func (s *Store) ExportJSONL(ctx context.Context, id string, w io.Writer) (int, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM messages
		WHERE session_id = ? ORDER BY id`, id)
	if err != nil {
		return 0, fmt.Errorf("sessions: export %s: %w", id, err)
	}
	defer rows.Close()
	enc := json.NewEncoder(w)
	n := 0
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return n, fmt.Errorf("sessions: export %s: %w", id, err)
		}
		line := exportLine{
			ID:           m.ID,
			Role:         string(m.Msg.Role),
			Content:      s.Redactor.Redact(m.Msg.Content),
			ToolCallID:   m.Msg.ToolCallID,
			ToolName:     m.Msg.ToolName,
			Model:        m.Meta.Model,
			InputTokens:  m.Meta.Usage.InputTokens,
			OutputTokens: m.Meta.Usage.OutputTokens,
			Timestamp:    m.At.Format(time.RFC3339),
		}
		for _, c := range m.Msg.ToolCalls {
			line.ToolCalls = append(line.ToolCalls, exportCall{ID: c.ID, Name: c.Name, Arguments: s.Redactor.Redact(c.Args)})
		}
		if err := enc.Encode(line); err != nil {
			return n, fmt.Errorf("sessions: export %s: write: %w", id, err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, fmt.Errorf("sessions: export %s: %w", id, err)
	}
	return n, nil
}

// ListAll returns the newest sessions of all keys, archived sessions included, with the
// most recently active session first. It returns at most limit sessions.
func (s *Store) ListAll(ctx context.Context, limit int) ([]SessionSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixCols("s.")+`,
		(SELECT count(*) FROM messages m WHERE m.session_id = s.id)
		FROM sessions s ORDER BY s.last_active DESC, s.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sessions: list: %w", err)
	}
	defer rows.Close()
	var out []SessionSummary
	for rows.Next() {
		var count int
		sess, err := scanSession(rows, &count)
		if err != nil {
			return nil, fmt.Errorf("sessions: list: %w", err)
		}
		out = append(out, SessionSummary{Session: *sess, MessageCount: count})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessions: list: %w", err)
	}
	return out, nil
}
