package skills

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEntry adds a lock entry and its skills/<name> folder for tests. It
// sets InstallPath to name and ContentHash and Files to the hash of files.
func writeEntry(t *testing.T, skillsDir, name string, e LockEntry, files map[string]string) {
	t.Helper()
	dir := filepath.Join(skillsDir, name)
	writeTree(t, dir, files)
	hash, list, err := ContentHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.InstallPath = name
	e.ContentHash = hash
	e.Files = list
	if err := UpdateLock(skillsDir, func(l *LockFile) error {
		l.Installed[name] = e
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// gitFinder returns a findGit function that gives g and counts its calls.
func gitFinder(g Git, calls *int) func() (Git, error) {
	return func() (Git, error) {
		*calls++
		return g, nil
	}
}

// rowsByName returns the rows of Check in a map by name.
func rowsByName(rows []CheckRow) map[string]CheckRow {
	m := map[string]CheckRow{}
	for _, r := range rows {
		m[r.Name] = r
	}
	return m
}

func TestCheckGitStatuses(t *testing.T) {
	url, first, second := makeRemote(t, "trunk")
	g := testGit(t)
	skillsDir := t.TempDir()
	files := map[string]string{"SKILL.md": skillMD("x")}
	writeEntry(t, skillsDir, "old", LockEntry{Source: "git", URL: url, Commit: first}, files)
	writeEntry(t, skillsDir, "cur", LockEntry{Source: "git", URL: url, Commit: second}, files)
	writeEntry(t, skillsDir, "pin", LockEntry{Source: "git", URL: url, Ref: first, Commit: first}, files)
	writeEntry(t, skillsDir, "gone", LockEntry{Source: "git", URL: filepath.Join(t.TempDir(), "missing"), Commit: first}, files)

	calls := 0
	rows, err := Check(context.Background(), skillsDir, gitFinder(g, &calls), "")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("findGit calls = %d, want 1", calls)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if got := strings.Join(names, ","); got != "cur,gone,old,pin" {
		t.Errorf("row order = %s, want cur,gone,old,pin", got)
	}
	m := rowsByName(rows)

	old := m["old"]
	if old.Status != StatusUpdate || old.Installed != first[:7] || old.Latest != second[:7] || !old.Update || old.Failed || old.Modified {
		t.Errorf("old = %+v", old)
	}
	cur := m["cur"]
	if cur.Status != StatusUpToDate || cur.Installed != second[:7] || cur.Latest != second[:7] || cur.Update || cur.Failed {
		t.Errorf("cur = %+v", cur)
	}
	pin := m["pin"]
	if pin.Status != StatusPinned || pin.Latest != "--ref "+first[:7] || pin.Installed != first[:7] || pin.Update || pin.Failed {
		t.Errorf("pin = %+v", pin)
	}
	gone := m["gone"]
	if gone.Status != StatusCannotCheck || !gone.Failed || gone.Update || gone.Latest == "" {
		t.Errorf("gone = %+v", gone)
	}
	if strings.Contains(gone.Latest, "\n") {
		t.Errorf("gone.Latest has a newline: %q", gone.Latest)
	}
}

func TestCheckPinnedMakesNoRemoteCall(t *testing.T) {
	skillsDir := t.TempDir()
	sha := strings.Repeat("ab", 20)
	writeEntry(t, skillsDir, "pin", LockEntry{Source: "git", URL: "https://example.invalid/x.git", Ref: sha, Commit: sha},
		map[string]string{"SKILL.md": skillMD("pin")})
	calls := 0
	rows, err := Check(context.Background(), skillsDir, func() (Git, error) {
		calls++
		return Git{}, ErrNoGit
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("findGit calls = %d, want 0", calls)
	}
	if len(rows) != 1 || rows[0].Status != StatusPinned || rows[0].Latest != "--ref abababa" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestCheckModifiedLocally(t *testing.T) {
	url, _, second := makeRemote(t, "main")
	g := testGit(t)
	skillsDir := t.TempDir()
	writeEntry(t, skillsDir, "pdf", LockEntry{Source: "git", URL: url, Commit: second},
		map[string]string{"SKILL.md": skillMD("pdf")})
	writeTree(t, filepath.Join(skillsDir, "pdf"), map[string]string{"SKILL.md": skillMD("pdf") + "edit\n"})

	calls := 0
	rows, err := Check(context.Background(), skillsDir, gitFinder(g, &calls), "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if r.Status != "up to date, modified locally" || !r.Modified || r.Update || r.Failed {
		t.Errorf("row = %+v", r)
	}
}

func TestCheckFolderMissing(t *testing.T) {
	skillsDir := t.TempDir()
	writeEntry(t, skillsDir, "pdf", LockEntry{Source: "git", URL: "https://example.invalid/x.git", Commit: strings.Repeat("a", 40)},
		map[string]string{"SKILL.md": skillMD("pdf")})
	if err := os.RemoveAll(filepath.Join(skillsDir, "pdf")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	rows, err := Check(context.Background(), skillsDir, func() (Git, error) {
		calls++
		return Git{}, ErrNoGit
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if r.Status != StatusCannotCheck || r.Latest != "folder missing" || !r.Failed {
		t.Errorf("row = %+v", r)
	}
	if calls != 0 {
		t.Errorf("findGit calls = %d, want 0", calls)
	}
}

func TestCheckLocalSource(t *testing.T) {
	skillsDir := t.TempDir()
	src := t.TempDir()
	files := map[string]string{"SKILL.md": skillMD("mine"), "notes.md": "n\n"}
	writeTree(t, filepath.Join(src, "skills", "mine"), files)
	writeEntry(t, skillsDir, "mine", LockEntry{Source: "local", URL: src, SkillPath: "skills/mine"}, files)

	check := func() CheckRow {
		t.Helper()
		rows, err := Check(context.Background(), skillsDir, nil, "mine")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %+v", rows)
		}
		return rows[0]
	}

	r := check()
	if r.Status != StatusUpToDate || r.Installed != "(local)" || r.Latest != "same" || r.Update || r.Failed {
		t.Errorf("unchanged = %+v", r)
	}

	writeTree(t, filepath.Join(src, "skills", "mine"), map[string]string{"notes.md": "changed\n"})
	r = check()
	if r.Status != StatusSourceChanged || r.Latest != "hash differs" || !r.Update || r.Failed || r.Modified {
		t.Errorf("edited = %+v", r)
	}

	if err := os.RemoveAll(filepath.Join(src, "skills")); err != nil {
		t.Fatal(err)
	}
	r = check()
	if r.Status != StatusCannotCheck || r.Latest != "source folder missing" || !r.Failed || r.Update {
		t.Errorf("deleted = %+v", r)
	}
}

func TestCheckWritesNothing(t *testing.T) {
	skillsDir := t.TempDir()
	src := t.TempDir()
	files := map[string]string{"SKILL.md": skillMD("mine")}
	writeTree(t, src, files)
	writeEntry(t, skillsDir, "mine", LockEntry{Source: "local", URL: src}, files)
	before, err := os.ReadFile(lockPath(skillsDir))
	if err != nil {
		t.Fatal(err)
	}
	beforeHash, _, err := ContentHash(skillsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Check(context.Background(), skillsDir, nil, ""); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(lockPath(skillsDir))
	if err != nil {
		t.Fatal(err)
	}
	afterHash, _, err := ContentHash(skillsDir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || beforeHash != afterHash {
		t.Error("Check changed the skills folder")
	}
}

func TestCheckExitCode(t *testing.T) {
	cases := []struct {
		rows []CheckRow
		want int
	}{
		{[]CheckRow{}, 0},
		{nil, 0},
		{[]CheckRow{{Update: true}}, 1},
		{[]CheckRow{{Failed: true}, {Failed: true}}, 2},
		{[]CheckRow{{Failed: true}, {}}, 0},
		{[]CheckRow{{Failed: true}, {Update: true}}, 1},
	}
	for i, c := range cases {
		if got := CheckExitCode(c.rows); got != c.want {
			t.Errorf("case %d: CheckExitCode = %d, want %d", i, got, c.want)
		}
	}
}

func TestFormatCheck(t *testing.T) {
	var b bytes.Buffer
	FormatCheck(&b, nil)
	if got, want := b.String(), "No installed skills. Install one with: tether skills install <source>\n"; got != want {
		t.Errorf("empty = %q, want %q", got, want)
	}

	b.Reset()
	FormatCheck(&b, []CheckRow{
		{Name: "pdf", Status: StatusUpdate, Installed: "3f2a9c1", Latest: "8d04e2b", Update: true},
		{Name: "bad", Status: StatusCannotCheck, Latest: "x\x1b[31my", Failed: true},
	})
	out := b.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "NAME") || !strings.Contains(lines[0], "STATUS") || !strings.Contains(lines[0], "LATEST") {
		t.Errorf("header = %q", lines[0])
	}
	if last := lines[len(lines)-1]; last != "Run tether skills update to install the updates." {
		t.Errorf("last line = %q", last)
	}
	if !strings.Contains(out, "pdf") || !strings.Contains(out, "8d04e2b") {
		t.Errorf("out = %q", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("out has a control character: %q", out)
	}

	b.Reset()
	FormatCheck(&b, []CheckRow{{Name: "pdf", Status: StatusUpToDate, Installed: "3f2a9c1", Latest: "3f2a9c1"}})
	if strings.Contains(b.String(), "Run tether skills update") {
		t.Errorf("no update, but the hint shows: %q", b.String())
	}
}

func TestCheckUnknownName(t *testing.T) {
	skillsDir := t.TempDir()
	_, err := Check(context.Background(), skillsDir, nil, "nope")
	if err == nil || err.Error() != `"nope" is not in the lockfile` {
		t.Errorf("err = %v", err)
	}
}

func TestCheckNoGit(t *testing.T) {
	skillsDir := t.TempDir()
	src := t.TempDir()
	files := map[string]string{"SKILL.md": skillMD("mine")}
	writeTree(t, src, files)
	writeEntry(t, skillsDir, "mine", LockEntry{Source: "local", URL: src}, files)
	writeEntry(t, skillsDir, "a", LockEntry{Source: "git", URL: "https://example.invalid/a.git", Commit: strings.Repeat("a", 40)}, files)
	writeEntry(t, skillsDir, "b", LockEntry{Source: "git", URL: "https://example.invalid/b.git", Commit: strings.Repeat("b", 40)}, files)

	calls := 0
	rows, err := Check(context.Background(), skillsDir, func() (Git, error) {
		calls++
		return Git{}, ErrNoGit
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("findGit calls = %d, want 1", calls)
	}
	m := rowsByName(rows)
	for _, n := range []string{"a", "b"} {
		if r := m[n]; r.Status != StatusCannotCheck || !r.Failed || r.Latest == "" {
			t.Errorf("%s = %+v", n, r)
		}
	}
	if r := m["mine"]; r.Status != StatusUpToDate || r.Failed {
		t.Errorf("mine = %+v", r)
	}
	if got := CheckExitCode(rows); got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
}
