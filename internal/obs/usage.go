package obs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/db"
)

// UsageRow is the usage of one model through one provider in a time range.
type UsageRow struct {
	Model string
	// Provider is the provider entry name of the calls.
	Provider string
	// InTok counts the input tokens and the cache read tokens.
	Calls, InTok, OutTok int
	// TokPerSec is 0 when the rows have no latency data.
	TokPerSec float64
	// CostUSD is the sum of the stored costs. It is valid only when CostKnown is true.
	CostUSD float64
	// CostKnown is true when one or more calls have a stored cost. Flat is true when the
	// provider has a flat-rate plan.
	CostKnown, Flat bool
}

// UsageReport returns the usage of each model since the time, from the stored assistant
// messages. A sessionID that is not empty limits the report to that session. flat holds
// the names of the providers with a flat-rate plan. The second value is the number of
// compactions in the same scope.
//
// The report reads the stored cost_usd and never prices old rows again.
func UsageReport(ctx context.Context, d *db.DB, since time.Time, sessionID string, flat map[string]bool) ([]UsageRow, int, error) {
	var sinceMS int64
	if !since.IsZero() {
		sinceMS = since.UnixMilli()
	}
	// tok/s uses only the calls that have latency data, and it excludes the time to the
	// first token.
	q := `SELECT model, provider, COUNT(*),
		SUM(input_tokens + cache_read_tokens), SUM(output_tokens),
		SUM(CASE WHEN latency_ms > latency_first_ms THEN output_tokens ELSE 0 END),
		SUM(CASE WHEN latency_ms > latency_first_ms THEN latency_ms - latency_first_ms ELSE 0 END),
		COUNT(cost_usd), COALESCE(SUM(cost_usd), 0)
		FROM messages WHERE role = 'assistant' AND model != '' AND timestamp >= ?`
	args := []any{sinceMS}
	if sessionID != "" {
		q += ` AND session_id = ?`
		args = append(args, sessionID)
	}
	q += ` GROUP BY model, provider ORDER BY COUNT(*) DESC, model, provider`
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("usage report: %w", err)
	}
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		var timedOut, genMS, costCount int64
		if err := rows.Scan(&r.Model, &r.Provider, &r.Calls, &r.InTok, &r.OutTok, &timedOut, &genMS, &costCount, &r.CostUSD); err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("usage report: %w", err)
		}
		if genMS > 0 {
			r.TokPerSec = float64(timedOut) / (float64(genMS) / 1000)
		}
		r.CostKnown = costCount > 0
		r.Flat = flat[r.Provider]
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, fmt.Errorf("usage report: %w", err)
	}
	// Close the rows before the next query: the pool has one connection.
	rows.Close()

	cq := `SELECT COUNT(*) FROM compactions WHERE created_at >= ?`
	cargs := []any{sinceMS}
	if sessionID != "" {
		cq += ` AND session_id = ?`
		cargs = append(cargs, sessionID)
	}
	var compactions int
	if err := d.QueryRowContext(ctx, cq, cargs...).Scan(&compactions); err != nil {
		return nil, 0, fmt.Errorf("usage report: %w", err)
	}
	return out, compactions, nil
}

// noValue is the cell text of a value that is not known.
const noValue = "—"

// FormatUsage returns the rows as a fixed-width table with the columns model, calls,
// in tok, out tok, tok/s, and cost.
func FormatUsage(rows []UsageRow) string {
	if len(rows) == 0 {
		return "No model calls in this range."
	}
	table := [][]string{{"model", "calls", "in tok", "out tok", "tok/s", "cost"}}
	for _, r := range rows {
		tps := noValue
		if r.TokPerSec > 0 {
			tps = strconv.FormatFloat(r.TokPerSec, 'f', 1, 64)
		}
		cost := noValue
		switch {
		case r.Flat:
			cost = "flat plan"
		case r.CostKnown:
			cost = humanCost(r.CostUSD)
		}
		table = append(table, []string{r.Model, strconv.Itoa(r.Calls), humanTokens(r.InTok), humanTokens(r.OutTok), tps, cost})
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
	return b.String()
}

// unknownCost is the cost cell of the rich and the narrow tables when the price is not
// known.
const unknownCost = "–"

// FormatUsageMarkdown returns the rows as a GitHub Flavored Markdown table with a bold Total
// row. The model column shows the short model names. The cells escape the Markdown markers,
// so a model name shows literally.
func FormatUsageMarkdown(rows []UsageRow) string {
	names := shortNames(rows)
	var b strings.Builder
	b.WriteString("| Model | Calls | In | Out | Cost |\n|:--|--:|--:|--:|--:|\n")
	var calls, in, out int
	for i, r := range rows {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n", escapeCell(names[i]), r.Calls,
			tableTokens(r.InTok), tableTokens(r.OutTok), escapeCell(rowCost(r)))
		calls, in, out = calls+r.Calls, in+r.InTok, out+r.OutTok
	}
	fmt.Fprintf(&b, "| **Total** | **%d** | **%s** | **%s** | **%s** |", calls,
		tableTokens(in), tableTokens(out), escapeCell(totalCost(rows)))
	return b.String()
}

// The column widths of FormatUsageNarrow. The sum is 34, so the table fits on a phone.
const (
	narrowModel  = 12
	narrowCalls  = 7
	narrowTokens = 8
	narrowCost   = 7
)

// FormatUsageNarrow returns a fixed-width table, 34 characters wide, with a total row. The
// columns are model, calls, tokens (in plus out), and cost. A long model name ends with "…".
func FormatUsageNarrow(rows []UsageRow) string {
	names := shortNames(rows)
	lines := []string{narrowLine("model", "calls", "tokens", "cost")}
	var calls, tokens int
	for i, r := range rows {
		lines = append(lines, narrowLine(names[i], strconv.Itoa(r.Calls), tableTokens(r.InTok+r.OutTok), rowCost(r)))
		calls, tokens = calls+r.Calls, tokens+r.InTok+r.OutTok
	}
	lines = append(lines, strings.Repeat("─", narrowModel+narrowCalls+narrowTokens+narrowCost),
		narrowLine("total", strconv.Itoa(calls), tableTokens(tokens), totalCost(rows)))
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

// tableTokens returns n with one decimal and a "k" or "M" suffix, for example "412.3k".
func tableTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 999_950:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
}

// rowCost returns the cost cell of one row of the rich and the narrow tables.
func rowCost(r UsageRow) string {
	switch {
	case r.Flat:
		return "flat"
	case r.CostKnown:
		return humanCost(r.CostUSD)
	}
	return unknownCost
}

// totalCost returns the total cost of the rows. It adds only the known prices, and it ends
// with "+" when a row has no known price. With no known price at all, it is "flat" when
// each row has a flat-rate plan, else unknownCost.
func totalCost(rows []UsageRow) string {
	var sum float64
	priced, unknown := false, false
	for _, r := range rows {
		switch {
		case r.Flat:
		case r.CostKnown:
			sum += r.CostUSD
			priced = true
		default:
			unknown = true
		}
	}
	switch {
	case priced && unknown:
		return humanCost(sum) + "+"
	case priced:
		return humanCost(sum)
	case unknown:
		return unknownCost
	}
	return "flat"
}

// humanTokens returns n with a "k" or "M" suffix and three significant digits at most,
// for example "188k" or "3.41M".
func humanTokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	// 999,999 rounds to "1000k", so it shows as "1M".
	if k := float64(n) / 1e3; k < 999.5 {
		return scaled(k) + "k"
	}
	return scaled(float64(n)/1e6) + "M"
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
var UsageRanges = []string{"today", "7d", "30d"}

// UsageSince returns the start of the named range. "today" starts at the local midnight of
// now. "7d" and "30d" start 7 and 30 days before now. ok is false for other names.
func UsageSince(name string, now time.Time) (since time.Time, ok bool) {
	switch name {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), true
	case "7d":
		return now.AddDate(0, 0, -7), true
	case "30d":
		return now.AddDate(0, 0, -30), true
	}
	return time.Time{}, false
}
