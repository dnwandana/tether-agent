package backup

import (
	"context"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/db"
)

const keyLikeSample = "sk-proj-abc123def456ghi789jkl012"

func TestKeyLike(t *testing.T) {
	if !keyLike("my key is " + keyLikeSample) {
		t.Fatal("a text with a key must count")
	}
	if keyLike("hello snapshot") {
		t.Fatal("a plain text must not count")
	}
	if keyLike(strings.Repeat("a", 70<<10) + " " + keyLikeSample) {
		t.Fatal("the pass reads only the first 64 KB")
	}
}

func TestCountKeyLikeCellsStopsAtLimit(t *testing.T) {
	_, live := newDataDir(t)
	for range 3 {
		if _, err := live.Exec("INSERT INTO messages(session_id, role, content) VALUES('s1', 'user', ?)",
			"key "+keyLikeSample); err != nil {
			t.Fatal(err)
		}
	}
	n, err := scanCells(ctx, live, 2)
	if err != nil || n != 2 {
		t.Fatalf("limit 2: n = %d, err = %v", n, err)
	}
	n, err = scanCells(ctx, live, KeyLikeMax)
	if err != nil || n != 3 {
		t.Fatalf("limit max: n = %d, err = %v", n, err)
	}
}

func TestCountKeyLikeFilesSkipsBinary(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.md", "token: ghp_abc123def456ghi789jkl")
	write(t, dir, "b.bin", "\x00"+keyLikeSample)
	write(t, dir, "c.md", "no key here")
	items := []item{
		{Rel: "a.md", Src: dir + "/a.md"},
		{Rel: "b.bin", Src: dir + "/b.bin"},
		{Rel: "c.md", Src: dir + "/c.md"},
		{Rel: "d", Src: dir, Dir: true},
	}
	n, err := scanFiles(items, KeyLikeMax)
	if err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
}

// A scan that reads more than its byte budget stops, and it reports that the count is partial.
func TestScanKeyLikeCellsStopsAtByteBudget(t *testing.T) {
	_, live := newDataDir(t)
	cell := "key " + keyLikeSample
	for range 5 {
		if _, err := live.Exec("INSERT INTO messages(session_id, role, content) VALUES('s1', 'user', ?)", cell); err != nil {
			t.Fatal(err)
		}
	}
	n, partial, err := scanKeyLikeCells(ctx, live, KeyLikeMax, newKeyLikeBudget(int64(len(cell))*2))
	if err != nil || !partial || n != 2 {
		t.Fatalf("small budget: n = %d, partial = %v, err = %v", n, partial, err)
	}
	n, partial, err = scanKeyLikeCells(ctx, live, KeyLikeMax, newKeyLikeBudget(KeyLikeScanBytes))
	if err != nil || partial || n != 5 {
		t.Fatalf("large budget: n = %d, partial = %v, err = %v", n, partial, err)
	}
}

func TestScanKeyLikeFilesStopsAtByteBudget(t *testing.T) {
	dir := t.TempDir()
	body := "token: ghp_abc123def456ghi789jkl"
	var items []item
	for _, name := range []string{"a.md", "b.md", "c.md", "d.md"} {
		write(t, dir, name, body)
		items = append(items, item{Rel: name, Src: dir + "/" + name})
	}
	n, partial, err := scanKeyLikeFiles(items, KeyLikeMax, newKeyLikeBudget(int64(len(body))*2))
	if err != nil || !partial || n != 2 {
		t.Fatalf("small budget: n = %d, partial = %v, err = %v", n, partial, err)
	}
	// The cells and the files share one budget.
	b := newKeyLikeBudget(int64(len(body)) * 5)
	n, partial, err = scanKeyLikeFiles(items, KeyLikeMax, b)
	if err != nil || partial || n != 4 {
		t.Fatalf("enough budget: n = %d, partial = %v, err = %v", n, partial, err)
	}
	if b.left <= 0 {
		t.Fatal("the budget must be above 0 after four reads")
	}
}

func TestCreateMarksPartialKeyScan(t *testing.T) {
	old := keyLikeScanLimit
	keyLikeScanLimit = 1
	t.Cleanup(func() { keyLikeScanLimit = old })
	dir, _ := newDataDir(t)
	write(t, dir, "memory/memory.md", "memo\n")
	write(t, dir, "skills/web/SKILL.md", "# web\n")
	res, err := Create(ctx, createOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Manifest.Redacted.KeyLikePartial {
		t.Fatalf("redacted = %+v, want KeyLikePartial", res.Manifest.Redacted)
	}
	keyLikeScanLimit = old
	dir, _ = newDataDir(t)
	if res, err = Create(ctx, createOpts(dir)); err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Redacted.KeyLikePartial {
		t.Fatal("a full scan must not be partial")
	}
}

// scanCells and scanFiles run the key-like pass with a full budget and drop the partial
// flag.
func scanCells(ctx context.Context, d *db.DB, limit int) (int, error) {
	n, _, err := scanKeyLikeCells(ctx, d, limit, newKeyLikeBudget(KeyLikeScanBytes))
	return n, err
}

func scanFiles(items []item, limit int) (int, error) {
	n, _, err := scanKeyLikeFiles(items, limit, newKeyLikeBudget(KeyLikeScanBytes))
	return n, err
}
