package obs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// usageRow is one assistant message for the usage tests.
type usageRow struct {
	session, model, prov string
	in, out, cache       int
	latency, first       int64
	cost                 any // nil stores NULL
	at                   time.Time
}

func openUsageDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func insertUsage(t *testing.T, d *db.DB, r usageRow) {
	t.Helper()
	if r.session == "" {
		r.session = "s1"
	}
	if r.at.IsZero() {
		r.at = time.Now()
	}
	_, err := d.Exec(`INSERT INTO messages (session_id, role, content, input_tokens, output_tokens,
		cache_read_tokens, latency_ms, latency_first_ms, cost_usd, model, provider, timestamp)
		VALUES (?, 'assistant', 'x', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.session, r.in, r.out, r.cache, r.latency, r.first, r.cost, r.model, r.prov, r.at.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
}

// seedUsage writes 212 flat glm-4.7 calls, 9 priced z-ai/glm-4.7 calls, and 1 call of a
// model with no price.
func seedUsage(t *testing.T) *db.DB {
	t.Helper()
	d := openUsageDB(t)
	for i := 0; i < 212; i++ {
		insertUsage(t, d, usageRow{model: "glm-4.7", prov: "primary", in: 15000, out: 900, latency: 2000, first: 500})
	}
	for i := 0; i < 9; i++ {
		insertUsage(t, d, usageRow{model: "z-ai/glm-4.7", prov: "openrouter", in: 1000, out: 100, latency: 1000, first: 200, cost: 0.07 / 9})
	}
	insertUsage(t, d, usageRow{model: "mystery", prov: "local", in: 10, out: 10})
	// The report must skip old rows, user rows, and rows with no model.
	insertUsage(t, d, usageRow{model: "old-model", prov: "local", in: 10, out: 10, at: time.Now().Add(-48 * time.Hour)})
	if _, err := d.Exec(`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('s1', 'user', 'hi', ?)`, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestReportAndFormat(t *testing.T) {
	ctx := context.Background()
	d := seedUsage(t)
	rows, _, err := UsageReport(ctx, d, time.Now().Add(-24*time.Hour), "", map[string]bool{"primary": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("%+v", rows)
	}
	out := FormatUsage(rows)
	for _, want := range []string{"glm-4.7", "212", "flat plan", "z-ai/glm-4.7", "$0.07", "—", "3.18M", "mystery"} {
		if !strings.Contains(out, want) {
			t.Fatal(want, "\n", out)
		}
	}
	if strings.Contains(out, "old-model") {
		t.Fatal("old rows must not show:\n", out)
	}
	for _, r := range rows {
		switch r.Model {
		case "glm-4.7":
			if !r.Flat || r.Calls != 212 || r.InTok != 212*15000 || r.OutTok != 212*900 {
				t.Fatalf("%+v", r)
			}
		case "z-ai/glm-4.7":
			if r.Flat || !r.CostKnown || r.CostUSD < 0.0699 || r.CostUSD > 0.0701 {
				t.Fatalf("%+v", r)
			}
		case "mystery":
			if r.Flat || r.CostKnown || r.TokPerSec != 0 {
				t.Fatalf("%+v", r)
			}
		}
	}
	header := strings.SplitN(out, "\n", 2)[0]
	for _, col := range []string{"model", "calls", "in tok", "out tok", "tok/s", "cost"} {
		if !strings.Contains(header, col) {
			t.Fatal(col, header)
		}
	}
}

func TestReportCountsCacheReadsAsInput(t *testing.T) {
	d := openUsageDB(t)
	insertUsage(t, d, usageRow{model: "m", prov: "p", in: 100, cache: 900, out: 1})
	rows, _, err := UsageReport(context.Background(), d, time.Now().Add(-time.Hour), "", nil)
	if err != nil || len(rows) != 1 || rows[0].InTok != 1000 {
		t.Fatal(rows, err)
	}
}

func TestTokPerSecExcludesFirstTokenLatency(t *testing.T) {
	d := openUsageDB(t)
	insertUsage(t, d, usageRow{model: "m", prov: "p", out: 100, latency: 3000, first: 500})
	rows, _, err := UsageReport(context.Background(), d, time.Now().Add(-time.Hour), "", nil)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if rows[0].TokPerSec != 40 {
		t.Fatal(rows[0].TokPerSec)
	}
	if !strings.Contains(FormatUsage(rows), "40.0") {
		t.Fatal(FormatUsage(rows))
	}
}

func TestTokPerSecNoLatencyShowsDash(t *testing.T) {
	rows := []UsageRow{{Model: "m", Calls: 1, OutTok: 10, CostKnown: true, CostUSD: 0.5}}
	out := FormatUsage(rows)
	line := strings.Split(strings.TrimSpace(out), "\n")[1]
	if !strings.Contains(line, "—") || !strings.Contains(line, "$0.50") {
		t.Fatal(out)
	}
}

func TestSessionScopeShowsCompactions(t *testing.T) {
	ctx := context.Background()
	d := openUsageDB(t)
	insertUsage(t, d, usageRow{session: "a", model: "m", prov: "p", out: 5})
	insertUsage(t, d, usageRow{session: "b", model: "m", prov: "p", out: 7})
	for _, s := range []string{"a", "a", "b"} {
		if _, err := d.Exec(`INSERT INTO compactions (session_id, summary, created_at) VALUES (?, 's', ?)`, s, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	rows, n, err := UsageReport(ctx, d, time.Time{}, "a", nil)
	if err != nil || n != 2 || len(rows) != 1 || rows[0].OutTok != 5 {
		t.Fatal(rows, n, err)
	}
	_, n, err = UsageReport(ctx, d, time.Now().Add(-time.Hour), "", nil)
	if err != nil || n != 3 {
		t.Fatal(n, err)
	}
}

func TestReportEmpty(t *testing.T) {
	rows, n, err := UsageReport(context.Background(), openUsageDB(t), time.Now().Add(-time.Hour), "", nil)
	if err != nil || len(rows) != 0 || n != 0 {
		t.Fatal(rows, n, err)
	}
	if out := FormatUsage(nil); !strings.Contains(out, "No model calls") {
		t.Fatal(out)
	}
}

func TestHumanTokens(t *testing.T) {
	for in, want := range map[int]string{
		0: "0", 950: "950", 1500: "1.5k", 12345: "12.3k", 188000: "188k",
		999999: "1M", 3410000: "3.41M", 25000000: "25M", 123456789: "123M",
	} {
		if got := humanTokens(in); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanCost(t *testing.T) {
	for in, want := range map[float64]string{0.07: "$0.07", 0.004: "$0.0040", 12.5: "$12.50", 0: "$0.00"} {
		if got := humanCost(in); got != want {
			t.Errorf("humanCost(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestUsageSince(t *testing.T) {
	now := time.Date(2026, 10, 27, 15, 4, 5, 0, time.Local)
	for name, want := range map[string]time.Time{
		"today": time.Date(2026, 10, 27, 0, 0, 0, 0, time.Local),
		"7d":    time.Date(2026, 10, 20, 15, 4, 5, 0, time.Local),
		"30d":   time.Date(2026, 9, 27, 15, 4, 5, 0, time.Local),
	} {
		if got, ok := UsageSince(name, now); !ok || !got.Equal(want) {
			t.Errorf("%s: %v %v", name, got, ok)
		}
	}
	if _, ok := UsageSince("1y", now); ok {
		t.Fatal("1y")
	}
}
