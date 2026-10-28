package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
)

// exportSecret is a .env value that must never reach an export file.
const exportSecret = "hunter2-super-secret-value"

// seedSessionWithTool writes a session with 4 messages: user, assistant tool call, tool
// result, and the final answer. The tool call and the tool result hold exportSecret.
func seedSessionWithTool(t *testing.T, s *Store) *Session {
	t.Helper()
	ctx := context.Background()
	sess, err := s.Create(ctx, Session{Key: "cli:user", Source: "cli", Title: "env check"})
	if err != nil {
		t.Fatal(err)
	}
	msgs := []struct {
		m    provider.Message
		meta MessageMeta
	}{
		{provider.Message{Role: provider.RoleUser, Content: "read .env"}, MessageMeta{}},
		{provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "terminal", Args: `{"command":"echo ` + exportSecret + `"}`}}},
			MessageMeta{Usage: provider.Usage{InputTokens: 50, OutputTokens: 7}, Model: "glm-4.7"}},
		{provider.Message{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "terminal", Content: "TOKEN=" + exportSecret}, MessageMeta{}},
		{provider.Message{Role: provider.RoleAssistant, Content: "done"}, MessageMeta{Usage: provider.Usage{InputTokens: 70, OutputTokens: 1}, Model: "glm-4.7"}},
	}
	for _, x := range msgs {
		if _, err := s.Append(ctx, sess.ID, x.m, x.meta); err != nil {
			t.Fatal(err)
		}
	}
	return sess
}

func TestExportValidJSONL(t *testing.T) {
	s := New(openTestDB(t))
	s.Redactor = security.NewRedactor([]string{exportSecret})
	sess := seedSessionWithTool(t, s)
	var buf bytes.Buffer
	n, err := s.ExportJSONL(context.Background(), sess.ID, &buf)
	if err != nil || n != 4 {
		t.Fatal(n, err)
	}
	if strings.Contains(buf.String(), exportSecret) {
		t.Fatal("secret in export:", buf.String())
	}
	var rows []map[string]any
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(sc.Text(), err)
		}
		rows = append(rows, m)
	}
	if len(rows) != 4 {
		t.Fatal(len(rows))
	}
	for _, k := range []string{"id", "role", "content", "tool_calls", "tool_call_id", "tool_name", "model", "input_tokens", "output_tokens", "timestamp"} {
		if _, ok := rows[1][k]; !ok {
			t.Fatalf("field %s is missing: %v", k, rows[1])
		}
	}
	if rows[0]["role"] != "user" || rows[0]["content"] != "read .env" {
		t.Fatal(rows[0])
	}
	calls, _ := rows[1]["tool_calls"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["name"] != "terminal" || rows[1]["input_tokens"] != float64(50) || rows[1]["model"] != "glm-4.7" {
		t.Fatal(rows[1])
	}
	if rows[2]["tool_call_id"] != "c1" || rows[2]["tool_name"] != "terminal" {
		t.Fatal(rows[2])
	}
	if _, err := time.Parse(time.RFC3339, rows[3]["timestamp"].(string)); err != nil {
		t.Fatal(err)
	}
}

func TestExportUnknownSession(t *testing.T) {
	s := New(openTestDB(t))
	var buf bytes.Buffer
	_, err := s.ExportJSONL(context.Background(), "nope", &buf)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "no session nope") || buf.Len() != 0 {
		t.Fatal(err, buf.String())
	}
}

func TestListAllNewestFirst(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	old := seedSessionWithTool(t, s)
	time.Sleep(2 * time.Millisecond)
	newer, _ := s.Create(ctx, Session{Kind: "cron", Title: "nightly"})
	got, err := s.ListAll(ctx, 50)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if got[0].ID != newer.ID || got[0].MessageCount != 0 || got[1].ID != old.ID || got[1].MessageCount != 4 || got[1].Title != "env check" {
		t.Fatalf("%+v", got)
	}
	if one, _ := s.ListAll(ctx, 1); len(one) != 1 {
		t.Fatal(one)
	}
}
