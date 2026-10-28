package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// archiveReadHeaders reads a gzip tar stream. It returns the headers in order
// and the bytes of the first entry.
func archiveReadHeaders(t *testing.T, data []byte) ([]*tar.Header, []byte) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var hdrs []*tar.Header
	var first []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(hdrs) == 0 {
			if first, err = io.ReadAll(tr); err != nil {
				t.Fatal(err)
			}
		}
		hdrs = append(hdrs, h)
	}
	if err := zr.Close(); err != nil {
		t.Fatal(err)
	}
	return hdrs, first
}

// archiveTestItems makes a data dir with state.db and scripts/run.sh, and
// returns the items for them.
func archiveTestItems(t *testing.T) (string, []item) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "state.db", "db")
	write(t, dir, "scripts/run.sh", "#!/bin/sh")
	if err := os.Chmod(filepath.Join(dir, "scripts/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	items := []item{
		{Rel: "state.db", Src: filepath.Join(dir, "state.db"), Mode: 0o600},
		{Rel: "scripts", Dir: true, Mode: fs.ModeDir | 0o700},
		{Rel: "scripts/run.sh", Src: filepath.Join(dir, "scripts/run.sh"), Mode: 0o755},
	}
	return dir, items
}

func TestWriteArchiveManifestFirst(t *testing.T) {
	_, items := archiveTestItems(t)
	files, err := fileEntries(items)
	if err != nil || len(files) != 2 || files[1].Mode != "0755" || files[0].Size != 2 {
		t.Fatal(files, err)
	}
	created := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	m := Manifest{Format: FormatVersion, CreatedAt: created, Files: files}
	var buf bytes.Buffer
	if err := writeArchive(&buf, m, items); err != nil {
		t.Fatal(err)
	}
	hdrs, raw := archiveReadHeaders(t, buf.Bytes())
	if len(hdrs) != 4 || hdrs[0].Name != ManifestName {
		t.Fatal(hdrs)
	}
	if hdrs[0].Mode != 0o600 || hdrs[0].Typeflag != tar.TypeReg {
		t.Fatal(hdrs[0])
	}
	names := []string{hdrs[1].Name, hdrs[2].Name, hdrs[3].Name}
	if !slices.Equal(names, []string{"state.db", "scripts/", "scripts/run.sh"}) {
		t.Fatal(names)
	}
	if hdrs[2].Typeflag != tar.TypeDir || hdrs[3].Mode != 0o755 {
		t.Fatal(hdrs[2], hdrs[3])
	}
	if hdrs[1].Size != files[0].Size || hdrs[3].Size != files[1].Size {
		t.Fatal(hdrs[1].Size, hdrs[3].Size)
	}
	for _, h := range hdrs {
		if !h.ModTime.Equal(created) || h.Uname != "" || h.Gname != "" {
			t.Fatal(h)
		}
	}
	var got Manifest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Format != FormatVersion || len(got.Files) != 2 || got.Files[1] != files[1] {
		t.Fatal(got)
	}
	if !bytes.Contains(raw, []byte("\n  \"format\": 1")) {
		t.Fatalf("manifest is not indented: %s", raw)
	}
}

func TestWriteArchiveFileChangedSize(t *testing.T) {
	dir, items := archiveTestItems(t)
	files, err := fileEntries(items)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "state.db", "a longer db")
	err = writeArchive(io.Discard, Manifest{Format: FormatVersion, Files: files}, items)
	if err == nil {
		t.Fatal("want an error for a file that changed size")
	}
}

func TestWriteArchiveWriteError(t *testing.T) {
	_, items := archiveTestItems(t)
	files, err := fileEntries(items)
	if err != nil {
		t.Fatal(err)
	}
	err = writeArchive(archiveFailWriter{}, Manifest{Format: FormatVersion, Files: files}, items)
	if err == nil {
		t.Fatal("want an error from the writer")
	}
}

// archiveFailWriter fails each write.
type archiveFailWriter struct{}

func (archiveFailWriter) Write([]byte) (int, error) { return 0, io.ErrShortWrite }

func TestFileEntriesMissingFile(t *testing.T) {
	items := []item{{Rel: "state.db", Src: filepath.Join(t.TempDir(), "none"), Mode: 0o600}}
	if _, err := fileEntries(items); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

func TestHashFile(t *testing.T) {
	sum, n, err := hashFile(writeTmp(t, "abc"))
	if err != nil || n != 3 || sum != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal(sum, n, err)
	}
}
