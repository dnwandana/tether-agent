package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// rename moves one file or folder. Tests replace it to make a rename fail.
var rename = os.Rename

// errUndoFailed tells that swap could not put each file back after an error. Then the stage
// folder and the pre-restore folder must stay, so the owner can move the files by hand.
var errUndoFailed = errors.New("the undo failed")

// liveNames returns the names of the live data dir that swap moves out for the top item name.
// The old state.db-wal and state.db-shm go with the old state.db, so an old WAL never stays
// next to a new DB. state.db.lock never moves, because Restore holds the lock on it.
func liveNames(name string) []string {
	if name == "state.db" {
		return []string{"state.db", "state.db-wal", "state.db-shm"}
	}
	return []string{name}
}

// swap moves the live top items of dataDir to preDir, and then moves the staged top items of
// stageDir into dataDir. Each item moves with one rename, because the three folders are on the
// same file system. On an error swap undoes the completed renames in reverse order. moved is
// true when at least one live item stays in preDir. On a new host preDir is empty at the end,
// so swap removes it.
func swap(dataDir, stageDir, preDir string) (moved bool, err error) {
	if err := os.Mkdir(preDir, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, fmt.Errorf("%s exists. Move it away or wait one second, and then try again", preDir)
		}
		return false, fmt.Errorf("create the folder %s: %w", preDir, err)
	}

	type move struct{ from, to string }
	var done []move
	do := func(from, to string) (bool, error) {
		if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		if err := rename(from, to); err != nil {
			return false, err
		}
		done = append(done, move{from, to})
		return true, nil
	}

	err = func() error {
		for _, name := range topItems {
			for _, n := range liveNames(name) {
				ok, err := do(filepath.Join(dataDir, n), filepath.Join(preDir, n))
				if err != nil {
					return err
				}
				moved = moved || ok
			}
		}
		for _, name := range topItems {
			if _, err := do(filepath.Join(stageDir, name), filepath.Join(dataDir, name)); err != nil {
				return err
			}
		}
		return nil
	}()
	if err == nil {
		if !moved {
			if rerr := os.Remove(preDir); rerr != nil {
				return false, fmt.Errorf("remove the empty folder %s: %w", preDir, rerr)
			}
		}
		return moved, nil
	}

	var undoErrs []error
	for i := len(done) - 1; i >= 0; i-- {
		if uerr := rename(done[i].to, done[i].from); uerr != nil {
			undoErrs = append(undoErrs, uerr)
		}
	}
	if len(undoErrs) > 0 {
		return true, fmt.Errorf("move the files between %s and %s: %v. %w: %v. The old files are in %s, and the new files are in %s. Move them back by hand",
			preDir, stageDir, err, errUndoFailed, errors.Join(undoErrs...), preDir, stageDir)
	}
	if rerr := os.Remove(preDir); rerr != nil {
		return false, fmt.Errorf("move the files between %s and %s: %v. The undo is complete, but remove the folder %s by hand: %v",
			preDir, stageDir, err, preDir, rerr)
	}
	return false, fmt.Errorf("move the files between %s and %s: %w. The undo is complete, and the data dir did not change",
		preDir, stageDir, err)
}
