package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// manifestMode is the tar mode of manifest.json.
const manifestMode = 0o600

// hashFile returns the lower-case hex sha256 of the file at path and its size.
func hashFile(path string) (sum string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err = io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// fileEntries returns one FileEntry for each item that is not a folder, in
// item order.
func fileEntries(items []item) ([]FileEntry, error) {
	var out []FileEntry
	for _, it := range items {
		if it.Dir {
			continue
		}
		sum, size, err := hashFile(it.Src)
		if err != nil {
			return nil, err
		}
		out = append(out, FileEntry{Path: it.Rel, Size: size, Mode: modeString(it.Mode), SHA256: sum})
	}
	return out, nil
}

// writeArchive writes m as manifest.json and then each item, as a gzip tar
// stream. It returns the first error. A file with a size that is not the
// manifest size is an error, so the archive always agrees with its manifest.
func writeArchive(w io.Writer, m Manifest, items []item) error {
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	err := writeEntries(tw, m, items)
	if cerr := tw.Close(); err == nil {
		err = cerr
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeEntries writes the manifest and the items to tw.
func writeEntries(tw *tar.Writer, m Manifest, items []item) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the manifest: %w", err)
	}
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     ManifestName,
		Mode:     manifestMode,
		Size:     int64(len(data)),
		ModTime:  m.CreatedAt,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write the manifest: %w", err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write the manifest: %w", err)
	}
	sizes := make(map[string]int64, len(m.Files))
	for _, f := range m.Files {
		sizes[f.Path] = f.Size
	}
	for _, it := range items {
		if it.Dir {
			hdr := &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     it.Rel + "/",
				Mode:     int64(it.Mode.Perm()),
				ModTime:  m.CreatedAt,
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return fmt.Errorf("write the folder %s: %w", it.Rel, err)
			}
			continue
		}
		want, ok := sizes[it.Rel]
		if !ok {
			return fmt.Errorf("the file %s is not in the manifest", it.Rel)
		}
		if err := writeFile(tw, it, want, m.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

// writeFile writes one regular file to tw. The header size comes from the
// open file, and it must be equal to want.
func writeFile(tw *tar.Writer, it item, want int64, modTime time.Time) error {
	f, err := os.Open(it.Src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("the file %s is not a regular file", it.Rel)
	}
	if st.Size() != want {
		return fmt.Errorf("the file %s changed size during the backup (%d bytes, the manifest has %d). Run the backup again", it.Rel, st.Size(), want)
	}
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     it.Rel,
		Mode:     int64(it.Mode.Perm()),
		Size:     st.Size(),
		ModTime:  modTime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write the file %s: %w", it.Rel, err)
	}
	n, err := io.CopyN(tw, f, st.Size())
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("the file %s changed size during the backup (%d bytes, the manifest has %d). Run the backup again", it.Rel, n, want)
	}
	if err != nil {
		return fmt.Errorf("write the file %s: %w", it.Rel, err)
	}
	return nil
}
