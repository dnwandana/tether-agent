package obs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// UsageRow is the usage of one model through one provider in a time range.
type UsageRow struct {
	Model string
	// Provider is the provider entry name of the calls.
	Provider string
	// InTok counts the input tokens and the cache read tokens.
	Calls, InTok, OutTok int
	// CacheTok counts the cache read tokens. InTok includes them.
	CacheTok int
	// TokPerSec is 0 when the rows have no latency data.
	TokPerSec float64
	// CostUSD is the sum of the stored costs. It is valid only when CostKnown is true.
	CostUSD float64
	// CostKnown is true when one or more calls have a stored cost.
	CostKnown bool
}

// HitRate returns the cached share of the input as a whole percent, rounded down.
// ok is false when the input is 0.
func HitRate(cache, in int) (pct int, ok bool) {
	if in == 0 {
		return 0, false
	}
	// Integer division rounds down, so "100%" shows only when all input came from the cache.
	return cache * 100 / in, true
}

// UsageReport returns the usage of each model since the time, from the stored assistant
// messages. A sessionID that is not empty limits the report to that session and its hidden
// children at each depth: subagents, reviews, goal judges, and aux sessions. The walk stops
// at a fork, because a fork has a visible kind. The second value is the number of
// compactions in the same scope.
//
// A row in a session of a visible kind that is older than the session is a fork copy. It
// does not count. A row with no session row counts.
//
// The report reads the stored cost_usd and never prices old rows again.
func UsageReport(ctx context.Context, d *db.DB, since time.Time, sessionID string) ([]UsageRow, int, error) {
	var sinceMS int64
	if !since.IsZero() {
		sinceMS = since.UnixMilli()
	}
	// tok/s uses only the calls that have latency data, and it excludes the time to the
	// first token.
	q := `SELECT m.model, m.provider, COUNT(*),
		SUM(m.input_tokens + m.cache_read_tokens), SUM(m.cache_read_tokens), SUM(m.output_tokens),
		SUM(CASE WHEN m.latency_ms > m.latency_first_ms THEN m.output_tokens ELSE 0 END),
		SUM(CASE WHEN m.latency_ms > m.latency_first_ms THEN m.latency_ms - m.latency_first_ms ELSE 0 END),
		COUNT(m.cost_usd), COALESCE(SUM(m.cost_usd), 0)
		FROM messages m
		WHERE m.role = 'assistant' AND m.model != '' AND m.timestamp >= ?`
	filter, fargs := usageScope("m.session_id", "m.timestamp", sessionID)
	q += filter + ` GROUP BY m.model, m.provider ORDER BY COUNT(*) DESC, m.model, m.provider`
	args := append([]any{sinceMS}, fargs...)
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("usage report: %w", err)
	}
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		var timedOut, genMS, costCount int64
		if err := rows.Scan(&r.Model, &r.Provider, &r.Calls, &r.InTok, &r.CacheTok, &r.OutTok, &timedOut, &genMS, &costCount, &r.CostUSD); err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("usage report: %w", err)
		}
		if genMS > 0 {
			r.TokPerSec = float64(timedOut) / (float64(genMS) / 1000)
		}
		r.CostKnown = costCount > 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, fmt.Errorf("usage report: %w", err)
	}
	// Close the rows before the next query: the pool has one connection.
	rows.Close()

	cq := `SELECT COUNT(*) FROM compactions c WHERE c.created_at >= ?`
	cfilter, cfargs := usageScope("c.session_id", "c.created_at", sessionID)
	cq += cfilter
	cargs := append([]any{sinceMS}, cfargs...)
	var compactions int
	if err := d.QueryRowContext(ctx, cq, cargs...).Scan(&compactions); err != nil {
		return nil, 0, fmt.Errorf("usage report: %w", err)
	}
	return out, compactions, nil
}

// usageScope returns the WHERE conditions that the two report queries share, and their args
// in the order of the placeholders. idCol and timeCol name the session ID and the time of
// the row.
//
// The NOT EXISTS condition skips the fork copies. It has no parent_id condition, because
// /delete of the original clears parent_id of the fork. A row with no session row passes it.
// The hidden kinds are not in the rule. They never get copies, and then a small step back
// of the clock cannot hide their calls.
func usageScope(idCol, timeCol, sessionID string) (string, []any) {
	kinds := make([]any, len(sessions.HiddenChildKinds))
	for i, k := range sessions.HiddenChildKinds {
		kinds[i] = k
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(kinds)), ", ")
	q := ` AND NOT EXISTS (SELECT 1 FROM sessions s
		WHERE s.id = ` + idCol + `
		AND s.kind NOT IN (` + marks + `)
		AND s.started_at > ` + timeCol + `)`
	args := append([]any{}, kinds...)
	if sessionID == "" {
		return q, args
	}
	// UNION drops the IDs that the walk already has, so a cycle in parent_id stops. The walk
	// starts from the session ID also when the session has no row.
	q += ` AND ` + idCol + ` IN (
		WITH RECURSIVE scope(id) AS (
			SELECT ?
			UNION
			SELECT s.id FROM sessions s JOIN scope ON s.parent_id = scope.id
			WHERE s.kind IN (` + marks + `)
		)
		SELECT id FROM scope)`
	args = append(args, sessionID)
	args = append(args, kinds...)
	return q, args
}

// noValue is the cell text of a value that is not known.
const noValue = "—"

// FormatUsage returns the rows as a fixed-width table with the columns model, calls,
// in tok, cached, hit, out tok, tok/s, and cost. A summary line with the total cache and the
// total hit rate follows the table.
func FormatUsage(rows []UsageRow) string {
	if len(rows) == 0 {
		return "No model calls in this range."
	}
	table := [][]string{{"model", "calls", "in tok", "cached", "hit", "out tok", "tok/s", "cost"}}
	var cache, in int
	for _, r := range rows {
		tps := noValue
		if r.TokPerSec > 0 {
			tps = strconv.FormatFloat(r.TokPerSec, 'f', 1, 64)
		}
		cost := noValue
		if r.CostKnown {
			cost = humanCost(r.CostUSD)
		}
		table = append(table, []string{r.Model, strconv.Itoa(r.Calls), HumanTokens(r.InTok),
			HumanTokens(r.CacheTok), hitText(r.CacheTok, r.InTok, noValue), HumanTokens(r.OutTok), tps, cost})
		cache, in = cache+r.CacheTok, in+r.InTok
	}
	widths := make([]int, len(table[0]))
	for _, line := range table {
		for i, cell := range line {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	var b strings.Builder
	for n, line := range table {
		if n > 0 {
			b.WriteByte('\n')
		}
		for i, cell := range line {
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell))
			// The model column aligns left and the number columns align right.
			if i == 0 {
				b.WriteString(cell)
				b.WriteString(pad)
			} else {
				b.WriteString("  ")
				b.WriteString(pad)
				b.WriteString(cell)
			}
		}
	}
	b.WriteByte('\n')
	b.WriteString(cacheLine(cache, in, HumanTokens, noValue))
	return b.String()
}

// noCell is the cell text of the rich and the narrow tables for a value that is not known:
// an unknown price, or a hit rate with no input.
const noCell = "–"

// hitText returns the hit rate with a "%" suffix, or empty when the input is 0.
func hitText(cache, in int, empty string) string {
	pct, ok := HitRate(cache, in)
	if !ok {
		return empty
	}
	return strconv.Itoa(pct) + "%"
}

// cacheLine returns the summary line with the cache tokens and the hit rate. It has no
// padding.
func cacheLine(cache, in int, tokens func(int) string, empty string) string {
	return "cache " + tokens(cache) + " · hit " + hitText(cache, in, empty)
}

// FormatUsageMarkdown returns the rows as a GitHub Flavored Markdown table with a bold Total
// row. The model column shows the short model names. The cells escape the Markdown markers,
// so a model name shows literally.
func FormatUsageMarkdown(rows []UsageRow) string {
	names := shortNames(rows)
	var b strings.Builder
	b.WriteString("| Model | Calls | In | Cached | Hit | Out | Cost |\n|:--|--:|--:|--:|--:|--:|--:|\n")
	var calls, in, cache, out int
	for i, r := range rows {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s |\n", escapeCell(names[i]), r.Calls,
			tableTokens(r.InTok), tableTokens(r.CacheTok), hitText(r.CacheTok, r.InTok, noCell),
			tableTokens(r.OutTok), escapeCell(rowCost(r)))
		calls, in, cache, out = calls+r.Calls, in+r.InTok, cache+r.CacheTok, out+r.OutTok
	}
	fmt.Fprintf(&b, "| **Total** | **%d** | **%s** | **%s** | **%s** | **%s** | **%s** |", calls,
		tableTokens(in), tableTokens(cache), hitText(cache, in, noCell), tableTokens(out),
		escapeCell(totalCost(rows)))
	return b.String()
}

// The column widths of FormatUsageNarrow. The sum is 34, so the table fits on a phone. The
// cost cell has room for "$123.45+" and one space before it, because the "all" range can
// reach such totals.
const (
	narrowModel  = 10
	narrowCalls  = 7
	narrowTokens = 8
	narrowCost   = 9
)

// FormatUsageNarrow returns a fixed-width table, 34 characters wide, with a total row. The
// columns are model, calls, tokens (in plus out), and cost. A long model name ends with "…".
// A summary line with the total cache and the total hit rate follows the table. It is 34
// characters or fewer and has no padding.
func FormatUsageNarrow(rows []UsageRow) string {
	names := shortNames(rows)
	lines := []string{narrowLine("model", "calls", "tokens", "cost")}
	var calls, tokens, in, cache int
	for i, r := range rows {
		lines = append(lines, narrowLine(names[i], strconv.Itoa(r.Calls), tableTokens(r.InTok+r.OutTok), rowCost(r)))
		calls, tokens = calls+r.Calls, tokens+r.InTok+r.OutTok
		in, cache = in+r.InTok, cache+r.CacheTok
	}
	lines = append(lines, strings.Repeat("─", narrowModel+narrowCalls+narrowTokens+narrowCost),
		narrowLine("total", strconv.Itoa(calls), tableTokens(tokens), totalCost(rows)),
		cacheLine(cache, in, tableTokens, noCell))
	return strings.Join(lines, "\n")
}

// narrowLine returns one line of FormatUsageNarrow. The model aligns left, and the other
// cells align right.
func narrowLine(model, calls, tokens, cost string) string {
	if utf8.RuneCountInString(model) > narrowModel {
		model = string([]rune(model)[:narrowModel-1]) + "…"
	}
	return padRight(model, narrowModel) + padLeft(calls, narrowCalls) + padLeft(tokens, narrowTokens) + padLeft(cost, narrowCost)
}

func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-utf8.RuneCountInString(s)))
}

func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(0, w-utf8.RuneCountInString(s))) + s
}

// shortNames returns the model name of each row without the provider prefix, for example
// "claude-sonnet-5" for "anthropic/claude-sonnet-5". When two rows have the same short name,
// those rows keep the full name.
func shortNames(rows []UsageRow) []string {
	names := make([]string, len(rows))
	count := map[string]int{}
	for i, r := range rows {
		names[i] = r.Model[strings.LastIndex(r.Model, "/")+1:]
		count[names[i]]++
	}
	for i, r := range rows {
		if count[names[i]] > 1 {
			names[i] = r.Model
		}
	}
	return names
}

// markdownCell escapes the characters that break a GitHub Flavored Markdown table cell or
// start a Markdown marker.
var markdownCell = strings.NewReplacer(`\`, `\\`, `|`, `\|`, `*`, `\*`, `_`, `\_`)

func escapeCell(s string) string { return markdownCell.Replace(s) }

// tableTokens returns n with one decimal and a "k", "M", or "B" suffix, for example "412.3k".
func tableTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 999_950:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	case n < 999_950_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	default:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	}
}

// rowCost returns the cost cell of one row of the rich and the narrow tables.
func rowCost(r UsageRow) string {
	if r.CostKnown {
		return humanCost(r.CostUSD)
	}
	return noCell
}

// totalCost returns the total cost of the rows. It adds only the known prices, and it ends
// with "+" when a row has no known price. With no known price at all, it is noCell.
func totalCost(rows []UsageRow) string {
	var sum float64
	priced, unknown := false, false
	for _, r := range rows {
		if r.CostKnown {
			sum += r.CostUSD
			priced = true
		} else {
			unknown = true
		}
	}
	switch {
	case priced && unknown:
		return humanCost(sum) + "+"
	case priced:
		return humanCost(sum)
	}
	return noCell
}

// HumanTokens returns n with a "k", "M", or "B" suffix and three significant digits at
// most, for example "188k" or "3.41M".
func HumanTokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	// 999,999 rounds to "1000k", so it shows as "1M". The M step has the same limit.
	if k := float64(n) / 1e3; k < 999.5 {
		return scaled(k) + "k"
	}
	if m := float64(n) / 1e6; m < 999.5 {
		return scaled(m) + "M"
	}
	return scaled(float64(n)/1e9) + "B"
}

// scaled returns v with three significant digits at most and no trailing zeros.
func scaled(v float64) string {
	prec := 0
	switch {
	case v < 9.995:
		prec = 2
	case v < 99.95:
		prec = 1
	}
	s := strconv.FormatFloat(v, 'f', prec, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// humanCost returns the cost in US dollars. A cost below one cent keeps four decimals, so
// that it does not show as $0.00.
func humanCost(usd float64) string {
	if usd > 0 && usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

// UsageRanges are the range names of UsageSince, in help order.
var UsageRanges = []string{"today", "7d", "30d", "all"}

// UsageSince returns the start of the named range. "today" starts at the local midnight of
// now. "7d" and "30d" start 7 and 30 days before now. "all" returns the zero time, which
// UsageReport reads as no lower limit. ok is false for other names.
func UsageSince(name string, now time.Time) (since time.Time, ok bool) {
	switch name {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), true
	case "7d":
		return now.AddDate(0, 0, -7), true
	case "30d":
		return now.AddDate(0, 0, -30), true
	case "all":
		return time.Time{}, true
	}
	return time.Time{}, false
}

// UsageRangeHead returns the words that name the range in a header, for example "7 days".
// It returns "" for a name that is not in UsageRanges.
func UsageRangeHead(name string) string {
	switch name {
	case "today":
		return "today"
	case "7d":
		return "7 days"
	case "30d":
		return "30 days"
	case "all":
		return "all time"
	}
	return ""
}
