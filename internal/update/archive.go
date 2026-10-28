package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
)

const (
	// MaxBinaryBytes is the largest tether binary that ExtractBinary reads.
	MaxBinaryBytes int64 = 200 << 20
	// MaxArchiveBytes is the largest release archive that the update downloads.
	MaxArchiveBytes int64 = 100 << 20
)

// ExtractBinary returns the content of the regular file "tether" at the root of a .tar.gz archive.
// It keeps the result in memory and writes no file.
func ExtractBinary(archive []byte, maxBytes int64) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("bad archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("archive has no tether entry")
		}
		if err != nil {
			return nil, fmt.Errorf("bad archive: %w", err)
		}
		// A symlink or a nested "dir/tether" must not replace the binary.
		if hdr.Typeflag != tar.TypeReg || path.Clean(hdr.Name) != "tether" {
			continue
		}
		if hdr.Size > maxBytes {
			return nil, fmt.Errorf("tether entry is %d bytes, limit %d", hdr.Size, maxBytes)
		}
		// The limit stops the read even if the header gives a wrong size.
		b, err := io.ReadAll(io.LimitReader(tr, maxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("bad archive: %w", err)
		}
		if int64(len(b)) > maxBytes {
			return nil, fmt.Errorf("tether entry is larger than the limit of %d bytes", maxBytes)
		}
		return b, nil
	}
}
