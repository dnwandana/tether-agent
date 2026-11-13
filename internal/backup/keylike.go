package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/security"
)

// This file holds the key-like pass. The pass reads the copy of state.db and the copied
// files, and it counts the texts that look like a key. It changes no data. A hit can also
// be NAME=value text or a URL query parameter, because the pattern engine changes such text.

// KeyLikeMax is the count at which the key-like pass stops. The report prints a count
// at the limit as "at least 1000".
const KeyLikeMax = 1000

// keyLikeReadMax is the number of bytes of each cell and of each file that the pass reads.
const keyLikeReadMax = 64 << 10

// KeyLikeScanBytes is the total number of bytes that the key-like pass reads in cells and
// files. The pass is only a hint for the user, and a large state.db would add minutes with
// no progress output. At the limit the pass stops, and the count is "at least n".
const KeyLikeScanBytes = 64 << 20

// keyLikeScanLimit is the byte budget of one backup. Tests make it small.
var keyLikeScanLimit int64 = KeyLikeScanBytes

// keyLikeBudget is the number of bytes that the pass can still read. The cell scan and the
// file scan share one budget.
type keyLikeBudget struct{ left int64 }

func newKeyLikeBudget(n int64) *keyLikeBudget { return &keyLikeBudget{left: n} }

var keyLikeRedactor = security.NewRedactor(nil)

// keyLike returns true when the first keyLikeReadMax bytes of text hold a key-like value.
func keyLike(text string) bool {
	if len(text) > keyLikeReadMax {
		text = text[:keyLikeReadMax]
	}
	return keyLikeRedactor.Redact(text) != text
}

// scanKeyLikeCells counts the key-like cells of the free-text columns of d. It stops when
// the count reaches limit or when the budget is used up. partial is true when it stopped
// because of the budget with cells left. It closes the rows of each query before the next query.
func scanKeyLikeCells(ctx context.Context, d *db.DB, limit int, budget *keyLikeBudget) (count int, partial bool, err error) {
	tables := make([]string, 0, len(freeTextColumns))
	for tbl := range freeTextColumns {
		tables = append(tables, tbl)
	}
	slices.Sort(tables)
	for _, tbl := range tables {
		for _, col := range freeTextColumns[tbl] {
			if count >= limit {
				return count, false, nil
			}
			n, cut, err := scanKeyLikeColumn(ctx, d, tbl, col, limit-count, budget)
			if err != nil {
				return 0, false, fmt.Errorf("scan %s.%s in the DB copy: %w", tbl, col, err)
			}
			count += n
			if cut {
				return count, true, nil
			}
		}
	}
	return count, false, nil
}

// scanKeyLikeColumn counts the key-like cells of one column, up to limit. cut is true when
// the budget ended before the last cell.
func scanKeyLikeColumn(ctx context.Context, d *db.DB, tbl, col string, limit int, budget *keyLikeBudget) (n int, cut bool, err error) {
	q := fmt.Sprintf("SELECT substr(CAST(%q AS BLOB), 1, %d) FROM %q WHERE %q IS NOT NULL",
		col, keyLikeReadMax, tbl, col)
	rows, err := d.QueryContext(ctx, q)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		if budget.left <= 0 {
			return n, true, nil
		}
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return 0, false, err
		}
		budget.left -= int64(len(b))
		if keyLike(string(b)) {
			n++
			if n >= limit {
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	return n, false, nil
}

// scanKeyLikeFiles counts the text files in items that hold a key-like value. It skips
// folders and binary files. It stops when the count reaches limit or when the budget is
// used up. partial is true when it stopped because of the budget with files left.
func scanKeyLikeFiles(items []item, limit int, budget *keyLikeBudget) (count int, partial bool, err error) {
	for _, it := range items {
		if count >= limit {
			break
		}
		if it.Dir {
			continue
		}
		if budget.left <= 0 {
			return count, true, nil
		}
		hit, read, err := fileKeyLike(it.Src)
		if err != nil {
			return 0, false, err
		}
		budget.left -= int64(read)
		if hit {
			count++
		}
	}
	return count, false, nil
}

// fileKeyLike reads the first keyLikeReadMax bytes of path and tests them. It also returns
// the number of bytes that it read.
func fileKeyLike(path string) (hit bool, read int, err error) {
	f, err := openRegular(path)
	if err != nil {
		return false, 0, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, keyLikeReadMax))
	if err != nil && !errors.Is(err, io.EOF) {
		return false, 0, fmt.Errorf("read %s: %w", path, err)
	}
	if bytes.IndexByte(b[:min(len(b), binarySniffLen)], 0) >= 0 {
		return false, len(b), nil
	}
	return keyLike(string(b)), len(b), nil
}
