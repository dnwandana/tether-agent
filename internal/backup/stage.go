package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dnwandana/tether-agent/internal/db"
)

// manifestLimit is the largest manifest.json that stage reads.
const manifestLimit = 1 << 20

// staged is an archive that stage unpacked and verified in a temp folder of the data dir.
type staged struct {
	Dir      string
	Manifest Manifest
	Stats    DBStats
}

// stage unpacks the archive in r into a new temp folder in dataDir and verifies it. The folder
// is in the data dir, so the swap can move each item with one rename. On each error stage
// removes the folder, so the data dir does not change.
func stage(ctx context.Context, r io.Reader, dataDir string) (*staged, error) {
	dir, err := os.MkdirTemp(dataDir, ".restore-tmp-")
	if err != nil {
		return nil, fmt.Errorf("create the stage folder: %w", err)
	}
	s, err := stageInto(ctx, r, dir)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	return s, nil
}

// stageInto does the work of stage in the folder dir.
func stageInto(ctx context.Context, r io.Reader, dir string) (*staged, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, notGzipTar(err)
	}
	tr := tar.NewReader(zr)
	m, err := readManifest(tr)
	if err != nil {
		return nil, err
	}
	want := make(map[string]FileEntry, len(m.Files))
	for _, f := range m.Files {
		want[f.Path] = f
	}
	seen := make(map[string]bool, len(m.Files))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		// A reader with GODEBUG tarinsecurepath=0 gives ErrInsecurePath with the header.
		// checkEntryName gives the clear error for that name.
		if err != nil && !(errors.Is(err, tar.ErrInsecurePath) && hdr != nil) {
			return nil, notGzipTar(err)
		}
		clean, err := checkEntryName(hdr.Name)
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			return nil, fmt.Errorf("the archive entry %s is a link or a special file. The archive is not safe", hdr.Name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(clean))
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return nil, fmt.Errorf("create the stage folder %s: %w", clean, err)
			}
			continue
		}
		fe, ok := want[clean]
		if !ok || seen[clean] {
			return nil, fmt.Errorf("%s is not in the manifest. The archive is damaged", clean)
		}
		seen[clean] = true
		if err := stageFile(tr, dst, fe, hdr.Mode); err != nil {
			return nil, err
		}
	}
	for _, f := range m.Files {
		if !seen[f.Path] {
			return nil, fmt.Errorf("%s is missing from the archive. The archive is damaged", f.Path)
		}
	}
	// Verify the DB after the sha256 checks, because the open changes the file header.
	stats, err := verifyStagedDB(ctx, dir)
	if err != nil {
		return nil, err
	}
	return &staged{Dir: dir, Manifest: m, Stats: stats}, nil
}

// readManifest reads and checks the first tar entry. These checks run before stage writes a
// file, so a foreign or newer archive writes nothing.
func readManifest(tr *tar.Reader) (Manifest, error) {
	var m Manifest
	hdr, err := tr.Next()
	if err != nil && err != io.EOF {
		return m, notGzipTar(err)
	}
	if err == io.EOF || hdr.Name != ManifestName || hdr.Typeflag != tar.TypeReg {
		return m, errors.New("the archive has no manifest.json as its first entry. The archive is not from tether backup")
	}
	data, err := io.ReadAll(io.LimitReader(tr, manifestLimit+1))
	if err != nil {
		return m, notGzipTar(err)
	}
	if len(data) > manifestLimit {
		return m, fmt.Errorf("manifest.json in the archive is larger than %d bytes. The archive is damaged", manifestLimit)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("read manifest.json in the archive: %w", err)
	}
	if m.Format != FormatVersion {
		return m, fmt.Errorf("the archive has format %d, and this binary knows format %d. Run tether update first", m.Format, FormatVersion)
	}
	if latest := db.LatestVersion(); m.SchemaVersion > latest {
		return m, schemaTooNew(m.SchemaVersion, latest)
	}
	hasDB := false
	paths := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		clean, err := checkEntryName(f.Path)
		if err != nil {
			return m, err
		}
		if clean != f.Path || clean == ManifestName || paths[clean] || f.Size < 0 {
			return m, fmt.Errorf("%s does not match the manifest. The archive is damaged", f.Path)
		}
		paths[clean] = true
		hasDB = hasDB || clean == "state.db"
	}
	if !hasDB {
		return m, errors.New("the archive has no state.db. The archive is not complete")
	}
	return m, nil
}

// stageFile copies the body of the current tar entry to dst and compares it with fe. It reads
// at most one byte more than the manifest size, so a large entry cannot fill the disk. The
// mode keeps only the owner bits, which clears the setuid, setgid, and sticky bits.
func stageFile(tr *tar.Reader, dst string, fe FileEntry, mode int64) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("create the stage folder for %s: %w", fe.Path, err)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(mode&0o700))
	if err != nil {
		return fmt.Errorf("create %s in the stage folder: %w", fe.Path, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(tr, fe.Size+1))
	if cerr := f.Close(); err == nil && cerr != nil {
		return fmt.Errorf("write %s in the stage folder: %w", fe.Path, cerr)
	}
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			return fmt.Errorf("write %s in the stage folder: %w", fe.Path, err)
		}
		return notGzipTar(err)
	}
	if n != fe.Size || hex.EncodeToString(h.Sum(nil)) != fe.SHA256 {
		return fmt.Errorf("%s does not match the manifest. The archive is damaged", fe.Path)
	}
	return nil
}

// verifyStagedDB checks the staged state.db and reads its counts. Then it removes the files
// that the open made, so the swap moves only names from the allow list.
func verifyStagedDB(ctx context.Context, dir string) (DBStats, error) {
	path := filepath.Join(dir, "state.db")
	d, err := db.OpenExisting(path)
	if err != nil {
		return DBStats{}, fmt.Errorf("open state.db in the archive: %w", err)
	}
	stats, err := func() (DBStats, error) {
		if err := checkDB(ctx, d); err != nil {
			var ie *integrityError
			if errors.As(err, &ie) {
				return DBStats{}, fmt.Errorf("state.db in the archive failed the integrity check: %s", ie.Line)
			}
			return DBStats{}, fmt.Errorf("state.db in the archive failed the integrity check: %v", err)
		}
		s, err := readStats(ctx, d)
		if err != nil {
			return DBStats{}, fmt.Errorf("read state.db in the archive: %w", err)
		}
		return s, nil
	}()
	if cerr := d.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("close state.db in the archive: %w", cerr)
	}
	if err != nil {
		return DBStats{}, err
	}
	// The manifest can disagree with the file, so check the version in the file too.
	if latest := db.LatestVersion(); stats.SchemaVersion > latest {
		return DBStats{}, schemaTooNew(stats.SchemaVersion, latest)
	}
	for _, name := range []string{db.LockPath(path), path + "-wal", path + "-shm"} {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return DBStats{}, fmt.Errorf("remove %s from the stage folder: %w", filepath.Base(name), err)
		}
	}
	return stats, nil
}

// notGzipTar returns the error for a stream that gzip or tar cannot read.
func notGzipTar(err error) error {
	return fmt.Errorf("the archive is not a gzip tar file: %v", err)
}

// schemaTooNew returns the error for a backup from a newer binary.
func schemaTooNew(have, latest int) error {
	return fmt.Errorf("the backup has schema %d, and this binary knows schema %d. Run tether update first", have, latest)
}
