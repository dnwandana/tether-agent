package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"slices"
	"strings"
	"testing"
)

// tarGz returns a .tar.gz that holds files as regular entries with mode 0o755, in sorted name order.
func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		body := files[name]
		hdr := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBinaryFindsRootEntry(t *testing.T) {
	a := tarGz(t, map[string][]byte{"LICENSE": []byte("mit"), "tether": []byte("ELF"), "README.md": []byte("#")})
	got, err := ExtractBinary(a, MaxBinaryBytes)
	if err != nil || string(got) != "ELF" {
		t.Fatal(got, err)
	}
}

func TestExtractBinaryAcceptsDotSlash(t *testing.T) {
	a := tarGz(t, map[string][]byte{"./tether": []byte("ELF")})
	got, err := ExtractBinary(a, MaxBinaryBytes)
	if err != nil || string(got) != "ELF" {
		t.Fatal(got, err)
	}
}

func TestExtractBinaryRejectsNested(t *testing.T) {
	a := tarGz(t, map[string][]byte{"dir/tether": []byte("ELF")})
	_, err := ExtractBinary(a, MaxBinaryBytes)
	if err == nil || err.Error() != "archive has no tether entry" {
		t.Fatal(err)
	}
}

func TestExtractBinaryRejectsSymlink(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "tether", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh", Mode: 0o777}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := ExtractBinary(buf.Bytes(), MaxBinaryBytes)
	if err == nil || err.Error() != "archive has no tether entry" {
		t.Fatal(err)
	}
}

func TestExtractBinaryLimit(t *testing.T) {
	a := tarGz(t, map[string][]byte{"tether": bytes.Repeat([]byte("x"), 10)})
	_, err := ExtractBinary(a, 5)
	if err == nil || err.Error() != "tether entry is 10 bytes, limit 5" {
		t.Fatal(err)
	}
}

func TestExtractBinaryBadGzip(t *testing.T) {
	_, err := ExtractBinary([]byte("not gzip"), MaxBinaryBytes)
	if err == nil || !strings.HasPrefix(err.Error(), "bad archive: ") {
		t.Fatal(err)
	}
}

func TestExtractBinaryTruncatedTar(t *testing.T) {
	a := tarGz(t, map[string][]byte{"tether": bytes.Repeat([]byte("x"), 4096)})
	// Decompress, cut the tar stream inside the entry body, and compress it again.
	zr, err := gzip.NewReader(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(zr); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw.Bytes()[:1024]); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = ExtractBinary(buf.Bytes(), MaxBinaryBytes)
	if err == nil || !strings.HasPrefix(err.Error(), "bad archive: ") {
		t.Fatal(err)
	}
}
