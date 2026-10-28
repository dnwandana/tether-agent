package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// stageTarEntry is one tar entry that a stage test writes.
type stageTarEntry struct {
	Name string
	Type byte
	Mode int64
	Body string
}

// stageManifestFor returns a correct manifest for the regular files in entries.
func stageManifestFor(entries []stageTarEntry) Manifest {
	m := Manifest{
		Format:        FormatVersion,
		TetherVersion: "test",
		CreatedAt:     time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
		SchemaVersion: db.LatestVersion(),
	}
	for _, e := range entries {
		if e.Type != tar.TypeReg {
			continue
		}
		sum := sha256.Sum256([]byte(e.Body))
		m.Files = append(m.Files, FileEntry{
			Path:   e.Name,
			Size:   int64(len(e.Body)),
			Mode:   modeString(fs.FileMode(e.Mode)),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	return m
}

// stageManifestEntry encodes m as the manifest.json entry.
func stageManifestEntry(t *testing.T, m Manifest) stageTarEntry {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return stageTarEntry{Name: ManifestName, Type: tar.TypeReg, Mode: 0o600, Body: string(data)}
}

// stageTarRaw writes entries as a gzip tar stream, in the given order.
func stageTarRaw(t *testing.T, entries ...stageTarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.Name, Typeflag: e.Type, Mode: e.Mode, ModTime: time.Unix(0, 0)}
		switch e.Type {
		case tar.TypeReg:
			hdr.Size = int64(len(e.Body))
		case tar.TypeSymlink, tar.TypeLink:
			hdr.Linkname = "/etc/passwd"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.Type == tar.TypeReg {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// stageTarGz writes entries as a gzip tar stream. When the first entry is not
// manifest.json, it puts a correct manifest first.
func stageTarGz(t *testing.T, entries ...stageTarEntry) []byte {
	t.Helper()
	if len(entries) == 0 || entries[0].Name != ManifestName {
		entries = append([]stageTarEntry{stageManifestEntry(t, stageManifestFor(entries))}, entries...)
	}
	return stageTarRaw(t, entries...)
}

// stageListTree returns the sorted slash paths under dir.
func stageListTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// stageValidDB returns the bytes of a closed state.db with one session and one message.
func stageValidDB(t *testing.T) []byte {
	t.Helper()
	dir, live := newDataDir(t)
	// The close moves the rows out of the WAL and into state.db.
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(readFile(t, filepath.Join(dir, "state.db")))
}

func TestStageRoundTrip(t *testing.T) {
	entries := []stageTarEntry{
		{Name: "state.db", Type: tar.TypeReg, Mode: 0o600, Body: string(stageValidDB(t))},
		{Name: "skills/", Type: tar.TypeDir, Mode: 0o700},
		{Name: "skills/a.md", Type: tar.TypeReg, Mode: 0o644, Body: "a"},
	}
	target := t.TempDir()
	s, err := stage(ctx, bytes.NewReader(stageTarGz(t, entries...)), target)
	if err != nil {
		t.Fatal(err)
	}
	if s.Stats.Messages != 1 || s.Manifest.SchemaVersion != db.LatestVersion() {
		t.Fatal(s)
	}
	if names := stageListTree(t, s.Dir); !slices.Equal(names, []string{"skills", "skills/a.md", "state.db"}) {
		t.Fatal(names)
	}
	if fi, _ := os.Stat(s.Dir); fi == nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatal(fi)
	}
	if filepath.Dir(s.Dir) != target {
		t.Fatal("the stage folder must be in the data dir")
	}
	if got := readFile(t, filepath.Join(s.Dir, "skills", "a.md")); got != "a" {
		t.Fatal(got)
	}
}

func TestStageRejects(t *testing.T) {
	dbBody := string(stageValidDB(t))
	stateDB := stageTarEntry{Name: "state.db", Type: tar.TypeReg, Mode: 0o600, Body: dbBody}
	skill := stageTarEntry{Name: "skills/a.md", Type: tar.TypeReg, Mode: 0o600, Body: "a"}

	valid := stageTarGz(t, stateDB, skill)
	withManifest := func(edit func(m *Manifest), entries ...stageTarEntry) []byte {
		m := stageManifestFor(entries)
		edit(&m)
		return stageTarRaw(t, append([]stageTarEntry{stageManifestEntry(t, m)}, entries...)...)
	}
	bad := func(name string, typ byte) []byte {
		return stageTarGz(t, stateDB, stageTarEntry{Name: name, Type: typ, Mode: 0o600, Body: "x"})
	}

	cases := []struct {
		name    string
		archive []byte
		want    []string
	}{
		{"not gzip", []byte("hello"), []string{"is not a gzip tar file"}},
		{"truncated", valid[:len(valid)/2], []string{"is not a gzip tar file", "does not match the manifest"}},
		{"manifest not first", stageTarRaw(t, stateDB, stageManifestEntry(t, stageManifestFor([]stageTarEntry{stateDB}))),
			[]string{"no manifest.json as its first entry"}},
		{"format 2", withManifest(func(m *Manifest) { m.Format = 2 }, stateDB),
			[]string{"format 2, and this binary knows format 1"}},
		{"schema too new", withManifest(func(m *Manifest) { m.SchemaVersion = db.LatestVersion() + 1 }, stateDB),
			[]string{"this binary knows schema"}},
		{"symlink", bad("skills/x", tar.TypeSymlink), []string{"is a link or a special file"}},
		{"hard link", bad("skills/x", tar.TypeLink), []string{"is a link or a special file"}},
		{"dot-dot", bad("../evil", tar.TypeReg), []string{"is outside the data folder"}},
		{"absolute", bad("/tmp/evil", tar.TypeReg), []string{"is outside the data folder"}},
		{"not allowed", bad(".env", tar.TypeReg), []string{"is not a tether data file"}},
		// The manifest is clean here, so the name check of the tar entry must stop the file.
		{"dot-dot entry only", stageTarRaw(t, stageManifestEntry(t, stageManifestFor([]stageTarEntry{stateDB})), stateDB,
			stageTarEntry{Name: "skills/../../evil", Type: tar.TypeReg, Mode: 0o600, Body: "x"}),
			[]string{"is outside the data folder"}},
		{"extra file", stageTarRaw(t, stageManifestEntry(t, stageManifestFor([]stageTarEntry{stateDB})), stateDB, skill),
			[]string{"is not in the manifest"}},
		{"bad hash", withManifest(func(m *Manifest) {
			sum := sha256.Sum256([]byte("b"))
			m.Files[1].SHA256 = hex.EncodeToString(sum[:])
		}, stateDB, skill), []string{"does not match the manifest"}},
		{"too large", withManifest(func(m *Manifest) {
			m.Files[1].Size = 1
			m.Files[1].SHA256 = strings.Repeat("0", 64)
		}, stateDB, stageTarEntry{Name: "skills/a.md", Type: tar.TypeReg, Mode: 0o600, Body: "ab"}),
			[]string{"does not match the manifest"}},
		{"missing", withManifest(func(m *Manifest) {
			m.Files = append(m.Files, FileEntry{Path: "skills/a.md", Size: 1, Mode: "0600", SHA256: strings.Repeat("0", 64)})
		}, stateDB), []string{"is missing from the archive"}},
		{"no state.db", stageTarGz(t, stageTarEntry{Name: "config.yaml", Type: tar.TypeReg, Mode: 0o600, Body: "a: 1\n"}),
			[]string{"has no state.db"}},
		{"corrupt db", stageTarGz(t, stageTarEntry{Name: "state.db", Type: tar.TypeReg, Mode: 0o600, Body: "not a database"}),
			[]string{"failed the integrity check"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := t.TempDir()
			write(t, target, "keep", "keep")
			s, err := stage(ctx, bytes.NewReader(c.archive), target)
			if err == nil {
				t.Fatal("stage must fail, and it staged", s.Dir)
			}
			if !slices.ContainsFunc(c.want, func(w string) bool { return strings.Contains(err.Error(), w) }) {
				t.Fatalf("error %q does not contain one of %q", err, c.want)
			}
			ents, rerr := os.ReadDir(target)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(ents) != 1 || ents[0].Name() != "keep" {
				var names []string
				for _, e := range ents {
					names = append(names, e.Name())
				}
				t.Fatal("the data dir changed:", names)
			}
		})
	}
}

func TestStageDropsSpecialBits(t *testing.T) {
	entries := []stageTarEntry{
		{Name: "state.db", Type: tar.TypeReg, Mode: 0o600, Body: string(stageValidDB(t))},
		{Name: "scripts/run.sh", Type: tar.TypeReg, Mode: 0o4755, Body: "#!/bin/sh\n"},
	}
	s, err := stage(ctx, bytes.NewReader(stageTarGz(t, entries...)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != 0o700 {
		t.Fatalf("the staged file has mode %v, want 0700", fi.Mode())
	}
	for _, d := range []string{s.Dir, filepath.Join(s.Dir, "scripts")} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 || fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
			t.Fatalf("the staged folder %s has mode %v, want 0700", d, fi.Mode())
		}
	}
}
