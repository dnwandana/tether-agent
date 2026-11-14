package obs

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

// seedUsage writes 212 glm-4.7 calls with no stored cost, 9 priced z-ai/glm-4.7 calls, and 1 call of a
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
	rows, _, err := UsageReport(ctx, d, time.Now().Add(-24*time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("%+v", rows)
	}
	out := FormatUsage(rows)
	for _, want := range []string{"glm-4.7", "212", "z-ai/glm-4.7", "$0.07", "—", "3.18M", "mystery"} {
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
			if r.CostKnown || r.Calls != 212 || r.InTok != 212*15000 || r.OutTok != 212*900 {
				t.Fatalf("%+v", r)
			}
		case "z-ai/glm-4.7":
			if !r.CostKnown || r.CostUSD < 0.0699 || r.CostUSD > 0.0701 {
				t.Fatalf("%+v", r)
			}
		case "mystery":
			if r.CostKnown || r.TokPerSec != 0 {
				t.Fatalf("%+v", r)
			}
		}
	}
	header := strings.SplitN(out, "\n", 2)[0]
	for _, col := range []string{"model", "calls", "in tok", "cached", "hit", "out tok", "tok/s", "cost"} {
		if !strings.Contains(header, col) {
			t.Fatal(col, header)
		}
	}
}

func TestUsageReportHasNoFlatPlan(t *testing.T) {
	d := seedUsage(t)
	rows, _, err := UsageReport(context.Background(), d, time.Time{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if out := FormatUsage(rows); strings.Contains(out, "flat") {
		t.Fatal(out)
	}
}

func TestReportCountsCacheReadsAsInput(t *testing.T) {
	d := openUsageDB(t)
	insertUsage(t, d, usageRow{model: "m", prov: "p", in: 100, cache: 900, out: 1})
	rows, _, err := UsageReport(context.Background(), d, time.Now().Add(-time.Hour), "")
	if err != nil || len(rows) != 1 || rows[0].InTok != 1000 {
		t.Fatal(rows, err)
	}
	if rows[0].CacheTok != 900 {
		t.Fatalf("CacheTok must be the sum of cache_read_tokens: %+v", rows[0])
	}
}

func TestTokPerSecExcludesFirstTokenLatency(t *testing.T) {
	d := openUsageDB(t)
	insertUsage(t, d, usageRow{model: "m", prov: "p", out: 100, latency: 3000, first: 500})
	rows, _, err := UsageReport(context.Background(), d, time.Now().Add(-time.Hour), "")
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
	rows, n, err := UsageReport(ctx, d, time.Time{}, "a")
	if err != nil || n != 2 || len(rows) != 1 || rows[0].OutTok != 5 {
		t.Fatal(rows, n, err)
	}
	_, n, err = UsageReport(ctx, d, time.Now().Add(-time.Hour), "")
	if err != nil || n != 3 {
		t.Fatal(n, err)
	}
}

func TestReportEmpty(t *testing.T) {
	rows, n, err := UsageReport(context.Background(), openUsageDB(t), time.Now().Add(-time.Hour), "")
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
		999_499_999: "999M", 999_949_999: "1B", 1_000_000_000: "1B", 12_300_000_000: "12.3B",
	} {
		if got := HumanTokens(in); got != want {
			t.Errorf("HumanTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHitRate(t *testing.T) {
	for _, tc := range []struct {
		cache, in, pct int
		ok             bool
	}{
		{0, 0, 0, false},
		{999, 1000, 99, true}, // rounds down
		{1000, 1000, 100, true},
		{0, 500, 0, true},
		{14_600_000, 15_200_000, 96, true},
	} {
		if pct, ok := HitRate(tc.cache, tc.in); pct != tc.pct || ok != tc.ok {
			t.Errorf("HitRate(%d, %d) = %d, %v; want %d, %v", tc.cache, tc.in, pct, ok, tc.pct, tc.ok)
		}
	}
}

// A total above 1 billion tokens must fit in the 8 characters of the narrow tokens cell.
func TestTableTokens(t *testing.T) {
	for in, want := range map[int]string{
		999: "999", 999_949: "999.9k", 999_950: "1.0M",
		999_949_999: "999.9M", 1_000_000_000: "1.0B", 12_300_000_000: "12.3B",
		999_000_000_000: "999.0B",
	} {
		got := tableTokens(in)
		if got != want {
			t.Errorf("tableTokens(%d) = %q, want %q", in, got, want)
		}
		if n := utf8.RuneCountInString(got); n > narrowTokens {
			t.Errorf("tableTokens(%d) = %q has %d runes", in, got, n)
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
		"all":   {},
	} {
		if got, ok := UsageSince(name, now); !ok || !got.Equal(want) {
			t.Errorf("%s: %v %v", name, got, ok)
		}
	}
	if got, ok := UsageSince("all", now); !ok || !got.IsZero() {
		t.Fatalf("all: %v %v", got, ok)
	}
	if _, ok := UsageSince("1y", now); ok {
		t.Fatal("1y")
	}
}

func TestUsageRangeHead(t *testing.T) {
	for name, want := range map[string]string{
		"today": "today", "7d": "7 days", "30d": "30 days", "all": "all time", "session": "", "1y": "",
	} {
		if got := UsageRangeHead(name); got != want {
			t.Errorf("UsageRangeHead(%q) = %q, want %q", name, got, want)
		}
	}
	if !slices.Equal(UsageRanges, []string{"today", "7d", "30d", "all"}) {
		t.Fatal(UsageRanges)
	}
	// Each range has a start and a header text.
	for _, name := range UsageRanges {
		if _, ok := UsageSince(name, time.Now()); !ok || UsageRangeHead(name) == "" {
			t.Error(name)
		}
	}
}

// tableRows returns the two rows of the usage table examples.
func tableRows() []UsageRow {
	return []UsageRow{
		{Model: "anthropic/claude-sonnet-5", Provider: "openrouter", Calls: 18, InTok: 412300, CacheTok: 380000, OutTok: 21700, CostUSD: 1.62, CostKnown: true},
		{Model: "zai/glm-4.6", Provider: "zai", Calls: 7, InTok: 88100, OutTok: 4200, CostUSD: 0.05, CostKnown: true},
	}
}

func TestFormatUsageMarkdown(t *testing.T) {
	want := "| Model | Calls | In | Cached | Hit | Out | Cost |\n" +
		"|:--|--:|--:|--:|--:|--:|--:|\n" +
		"| claude-sonnet-5 | 18 | 412.3k | 380.0k | 92% | 21.7k | $1.62 |\n" +
		"| glm-4.6 | 7 | 88.1k | 0 | 0% | 4.2k | $0.05 |\n" +
		"| **Total** | **25** | **500.4k** | **380.0k** | **75%** | **25.9k** | **$1.67** |"
	if got := FormatUsageMarkdown(tableRows()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatUsageMarkdownEscapesAndUnknownCost(t *testing.T) {
	rows := append(tableRows(), UsageRow{Model: `x/my_model|v2*\`, Calls: 1, InTok: 10, OutTok: 5})
	got := FormatUsageMarkdown(rows)
	if !strings.Contains(got, "\n| my\\_model\\|v2\\*\\\\ | 1 | 10 | 0 | 0% | 5 | – |\n") {
		t.Fatalf("got:\n%s", got)
	}
	if !strings.HasSuffix(got, "| **Total** | **26** | **500.4k** | **380.0k** | **75%** | **25.9k** | **$1.67+** |") {
		t.Fatalf("got:\n%s", got)
	}
	// Two rows with the same short name keep the full names. 0 input shows the dash.
	got = FormatUsageMarkdown([]UsageRow{{Model: "a/m", Calls: 1}, {Model: "b/m", Calls: 1}})
	if !strings.Contains(got, "\n| a/m | 1 | 0 | 0 | – | 0 | – |\n") || !strings.Contains(got, "\n| b/m | 1 |") ||
		!strings.HasSuffix(got, "| **Total** | **2** | **0** | **0** | **–** | **0** | **–** |") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestFormatUsageNarrow(t *testing.T) {
	want := "model       calls  tokens     cost\n" +
		"claude-so…     18  434.0k    $1.62\n" +
		"glm-4.6         7   92.3k    $0.05\n" +
		"──────────────────────────────────\n" +
		"total          25  526.3k    $1.67\n" +
		"cache 380.0k · hit 75%"
	got := FormatUsageNarrow(tableRows())
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	checkNarrowWidths(t, got)
	got = FormatUsageNarrow([]UsageRow{{Model: "m", Calls: 2, InTok: 3, OutTok: 4}})
	if !strings.Contains(got, "\nm"+strings.Repeat(" ", 15)+"2       7        –\n") ||
		!strings.HasSuffix(got, "total           2       7        –\ncache 0 · hit 0%") {
		t.Fatalf("got:\n%s", got)
	}
	if got = FormatUsageNarrow([]UsageRow{{Model: "m", Calls: 1, OutTok: 4}}); !strings.HasSuffix(got, "\ncache 0 · hit –") {
		t.Fatalf("got:\n%s", got)
	}
}

// checkNarrowWidths checks that each table line has 34 runes, and that the last line is the
// summary line with 34 runes or fewer.
func checkNarrowWidths(t *testing.T, got string) {
	t.Helper()
	lines := strings.Split(got, "\n")
	for _, line := range lines[:len(lines)-1] {
		if n := utf8.RuneCountInString(line); n != 34 {
			t.Fatalf("%q has %d runes", line, n)
		}
	}
	last := lines[len(lines)-1]
	if n := utf8.RuneCountInString(last); !strings.HasPrefix(last, "cache ") || n > 34 {
		t.Fatalf("summary line %q has %d runes", last, n)
	}
}

func TestFormatUsageNarrowBillion(t *testing.T) {
	rows := []UsageRow{
		{Model: "zai/glm-5.3", Calls: 50000, InTok: 700_000_000, CacheTok: 690_000_000, OutTok: 3_000_000, CostUSD: 12.34, CostKnown: true},
		{Model: "moonshot/kimi-k2.6", Calls: 40000, InTok: 600_000_000, CacheTok: 500_000_000, OutTok: 2_000_000, CostUSD: 5, CostKnown: true},
	}
	got := FormatUsageNarrow(rows)
	checkNarrowWidths(t, got)
	if !strings.HasSuffix(got, "1.3B   $17.34\ncache 1.2B · hit 91%") {
		t.Fatalf("got:\n%s", got)
	}
}

// A total of $100 or more with an unknown price ends with "+". The cost cell must keep a space
// before it and keep the line at 34 runes.
func TestFormatUsageNarrowWideCost(t *testing.T) {
	rows := []UsageRow{
		{Model: "zai/glm-5.3", Calls: 50000, InTok: 700_000_000, CacheTok: 690_000_000, OutTok: 3_000_000, CostUSD: 123.45, CostKnown: true},
		{Model: "moonshot/kimi-k2.6", Calls: 4, InTok: 1000, OutTok: 10},
	}
	got := FormatUsageNarrow(rows)
	checkNarrowWidths(t, got)
	for line := range strings.SplitSeq(got, "\n") {
		if strings.Contains(line, "$") && !strings.Contains(line, " $") {
			t.Fatalf("the cost touches the tokens cell: %q", line)
		}
	}
	if !strings.Contains(got, " $123.45+\n") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestFormatUsageCacheColumns(t *testing.T) {
	want := "model                      calls  in tok  cached  hit  out tok  tok/s   cost\n" +
		"anthropic/claude-sonnet-5     18    412k    380k  92%    21.7k      —  $1.62\n" +
		"zai/glm-4.6                    7   88.1k       0   0%     4.2k      —  $0.05\n" +
		"cache 380k · hit 75%"
	if got := FormatUsage(tableRows()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// 0 input shows the CLI dash in the hit cell and in the summary line.
	got := FormatUsage([]UsageRow{{Model: "m", Calls: 1, OutTok: 10}})
	if f := strings.Fields(strings.Split(got, "\n")[1]); f[4] != "—" || !strings.HasSuffix(got, "\ncache 0 · hit —") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestUsageTotalCost(t *testing.T) {
	priced := UsageRow{Model: "p", CostKnown: true, CostUSD: 0.5}
	unknown := UsageRow{Model: "u"}
	for _, tc := range []struct {
		rows []UsageRow
		want string
	}{
		{[]UsageRow{unknown}, "–"},
		{[]UsageRow{priced}, "$0.50"},
		{[]UsageRow{priced, priced}, "$1.00"},
		{[]UsageRow{priced, unknown}, "$0.50+"},
	} {
		if got := totalCost(tc.rows); got != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.rows, got, tc.want)
		}
	}
}

func TestSessionScopeCountsGoalJudgeRows(t *testing.T) {
	ctx := context.Background()
	d := openUsageDB(t)
	for _, s := range []struct{ id, kind, parent string }{
		{"g1", "chat", ""}, {"j1", "goal_judge", "g1"}, {"j2", "goal_judge", "g2"}, {"r1", "review", "g1"},
	} {
		if _, err := d.Exec(`INSERT INTO sessions (id, kind, parent_id) VALUES (?, ?, ?)`, s.id, s.kind, s.parent); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"g1", "j1", "j1", "j2", "r1"} {
		insertUsage(t, d, usageRow{session: s, model: "m", prov: "p", out: 5})
	}
	rows, _, err := UsageReport(ctx, d, time.Time{}, "g1")
	if err != nil || len(rows) != 1 || rows[0].Calls != 4 || rows[0].OutTok != 20 {
		t.Fatalf("the session range counts g1, its goal judge j1, and its review r1: %+v %v", rows, err)
	}
	rows, _, err = UsageReport(ctx, d, time.Time{}, "")
	if err != nil || len(rows) != 1 || rows[0].Calls != 5 {
		t.Fatalf("the time range counts every row: %+v %v", rows, err)
	}
}

// insertKindSession writes one sessions row with the kind, the parent, and the start time.
func insertKindSession(t *testing.T, d *db.DB, id, kind, parent string, started time.Time) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions (id, kind, parent_id, started_at) VALUES (?, ?, ?, ?)`,
		id, kind, parent, started.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

// insertCompaction writes one compactions row of the session.
func insertCompaction(t *testing.T, d *db.DB, session string, at time.Time) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO compactions (session_id, summary, created_at) VALUES (?, 's', ?)`,
		session, at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

// reportRanges gives the start of each range test. "today" is not in the list, because the
// test times can be before the local midnight.
func reportRanges() map[string]time.Time {
	now := time.Now()
	return map[string]time.Time{"1h": now.Add(-time.Hour), "7d": now.AddDate(0, 0, -7),
		"30d": now.AddDate(0, 0, -30), "all": {}}
}

func TestUsageReportSkipsForkCopies(t *testing.T) {
	ctx := context.Background()
	d := openUsageDB(t)
	t0 := time.Now().Add(-30 * time.Minute)
	insertKindSession(t, d, "o", "chat", "", t0)
	insertKindSession(t, d, "f", "chat", "o", t0.Add(10*time.Second))
	for _, s := range []string{"o", "f"} {
		// Fork copies each message and each compaction with its old time.
		insertUsage(t, d, usageRow{session: s, model: "m", prov: "p", in: 10, cache: 90, out: 1, at: t0.Add(time.Second)})
		insertUsage(t, d, usageRow{session: s, model: "m", prov: "p", in: 10, cache: 90, out: 1, at: t0.Add(2 * time.Second)})
		insertCompaction(t, d, s, t0.Add(3*time.Second))
	}
	insertUsage(t, d, usageRow{session: "f", model: "m", prov: "p", in: 10, cache: 90, out: 1, at: t0.Add(11 * time.Second)})

	for name, since := range reportRanges() {
		rows, n, err := UsageReport(ctx, d, since, "")
		if err != nil || len(rows) != 1 || rows[0].Calls != 3 || rows[0].CacheTok != 270 || n != 1 {
			t.Fatalf("%s: the range must skip the fork copies: %+v %d %v", name, rows, n, err)
		}
	}
	if rows, n, err := UsageReport(ctx, d, time.Time{}, "f"); err != nil || len(rows) != 1 || rows[0].Calls != 1 || n != 0 {
		t.Fatalf("the fork counts only its own calls: %+v %d %v", rows, n, err)
	}
	if rows, n, err := UsageReport(ctx, d, time.Time{}, "o"); err != nil || len(rows) != 1 || rows[0].Calls != 2 || n != 1 {
		t.Fatalf("the walk must stop at the fork: %+v %d %v", rows, n, err)
	}
	// /delete of the original removes its rows and clears parent_id of the fork.
	for _, q := range []string{
		`DELETE FROM messages WHERE session_id = 'o'`,
		`DELETE FROM compactions WHERE session_id = 'o'`,
		`DELETE FROM sessions WHERE id = 'o'`,
		`UPDATE sessions SET parent_id = '' WHERE id = 'f'`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for name, since := range reportRanges() {
		rows, n, err := UsageReport(ctx, d, since, "")
		if err != nil || len(rows) != 1 || rows[0].Calls != 1 || n != 0 {
			t.Fatalf("%s: the copies must not count after /delete of the original: %+v %d %v", name, rows, n, err)
		}
	}
}

// /delete can remove a session while a turn appends to it. Append does not check the
// session, so the message has no session row. It must still count.
func TestUsageReportCountsMessageWithNoSessionRow(t *testing.T) {
	ctx := context.Background()
	d := openUsageDB(t)
	insertUsage(t, d, usageRow{session: "gone", model: "m", prov: "p", out: 5, at: time.Now().Add(-time.Minute)})
	insertCompaction(t, d, "gone", time.Now().Add(-time.Minute))
	for name, since := range reportRanges() {
		rows, n, err := UsageReport(ctx, d, since, "")
		if err != nil || len(rows) != 1 || rows[0].Calls != 1 || n != 1 {
			t.Fatalf("%s: %+v %d %v", name, rows, n, err)
		}
	}
	if rows, n, err := UsageReport(ctx, d, time.Time{}, "gone"); err != nil || len(rows) != 1 || rows[0].Calls != 1 || n != 1 {
		t.Fatalf("session: %+v %d %v", rows, n, err)
	}
}

func TestSessionScopeWalksHiddenChildren(t *testing.T) {
	ctx := context.Background()
	d := openUsageDB(t)
	t0 := time.Now().Add(-time.Hour)
	for _, s := range []struct{ id, kind, parent string }{
		{"c", "chat", ""},
		{"j", "goal_judge", "c"},
		{"sa", "subagent", "c"},
		{"gc", "subagent", "sa"}, // a grandchild, with max_spawn_depth above 1
		{"r", "review", "c"},
		{"x", "aux", "c"},
		{"sx", "aux", "sa"}, // an aux child of a subagent
		{"f", "chat", "c"},  // a fork: the walk stops here
		{"fj", "goal_judge", "f"},
		{"other", "chat", ""},
	} {
		insertKindSession(t, d, s.id, s.kind, s.parent, t0)
		insertUsage(t, d, usageRow{session: s.id, model: "m", prov: "p", out: 1, at: t0.Add(time.Second)})
	}
	insertCompaction(t, d, "gc", t0.Add(2*time.Second))
	insertCompaction(t, d, "f", t0.Add(2*time.Second))
	// c, j, sa, gc, r, x, and sx count. f, fj, and other do not.
	if rows, n, err := UsageReport(ctx, d, time.Time{}, "c"); err != nil || len(rows) != 1 || rows[0].Calls != 7 || n != 1 {
		t.Fatalf("session: %+v %d %v", rows, n, err)
	}
	if rows, n, err := UsageReport(ctx, d, time.Time{}, ""); err != nil || len(rows) != 1 || rows[0].Calls != 10 || n != 2 {
		t.Fatalf("range: %+v %d %v", rows, n, err)
	}
}

// UNION (not UNION ALL) stops a cycle in parent_id.
func TestSessionScopeStopsAtParentCycle(t *testing.T) {
	ctx := context.Background()
	d := openUsageDB(t)
	t0 := time.Now().Add(-time.Hour)
	insertKindSession(t, d, "p", "subagent", "q", t0)
	insertKindSession(t, d, "q", "subagent", "p", t0)
	insertUsage(t, d, usageRow{session: "p", model: "m", prov: "p", out: 1, at: t0.Add(time.Second)})
	insertUsage(t, d, usageRow{session: "q", model: "m", prov: "p", out: 1, at: t0.Add(time.Second)})
	if rows, _, err := UsageReport(ctx, d, time.Time{}, "p"); err != nil || len(rows) != 1 || rows[0].Calls != 2 {
		t.Fatalf("%+v %v", rows, err)
	}
}
