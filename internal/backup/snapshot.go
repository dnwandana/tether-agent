package backup

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dnwandana/tether-agent/internal/db"
)

// DBStats holds the schema version and the row counts of a state.db for the reports.
type DBStats struct {
	SchemaVersion, Messages, Sessions, CronJobs int
}

// snapshotDB copies the live database to dst with VACUUM INTO. VACUUM INTO reads in one read
// transaction, so the copy is one consistent snapshot that includes the commits that are still
// only in the WAL. A read transaction does not block a writer in WAL mode.
//
// The empty file keeps mode 0600, because VACUUM INTO creates a missing file with the umask
// mode. The path goes in as a bound parameter, so a quote in the path cannot change the SQL.
func snapshotDB(ctx context.Context, live *db.DB, dst string) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create the copy of state.db: %w", err)
	}
	if err := f.Close(); err != nil {
		return errors.Join(fmt.Errorf("create the copy of state.db: %w", err), os.Remove(dst))
	}
	if _, err := live.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return errors.Join(fmt.Errorf("copy state.db: %w", err), os.Remove(dst))
	}
	return nil
}

// checkDB finds damage in the database before backup puts it into an archive or restore puts
// it into the data dir. quick_check gives one "ok" row when it finds no problem.
func checkDB(ctx context.Context, d *db.DB) error {
	rows, err := d.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("check state.db: %w", err)
	}
	// Close the rows before the return, because the pool has only one connection.
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("check state.db: %w", err)
		}
		return errors.New("check state.db: the integrity check gave no result")
	}
	var first string
	if err := rows.Scan(&first); err != nil {
		return fmt.Errorf("check state.db: %w", err)
	}
	if first != "ok" {
		return &integrityError{Line: first}
	}
	return nil
}

// integrityError tells that quick_check found damage. Line is the first row of quick_check, so
// a caller can name the database in its own words.
type integrityError struct {
	Line string
}

func (e *integrityError) Error() string {
	return "state.db failed the integrity check: " + e.Line
}

// readStats reads the schema version and the row counts that the reports show.
func readStats(ctx context.Context, d *db.DB) (DBStats, error) {
	var s DBStats
	v, err := d.SchemaVersion(ctx)
	if err != nil {
		return DBStats{}, err
	}
	s.SchemaVersion = v
	counts := []struct {
		table string
		dst   *int
	}{
		{"messages", &s.Messages},
		{"sessions", &s.Sessions},
		{"cron_jobs", &s.CronJobs},
	}
	for _, c := range counts {
		// The table name comes from the fixed list above, not from input.
		if err := d.QueryRowContext(ctx, "SELECT count(*) FROM "+c.table).Scan(c.dst); err != nil {
			return DBStats{}, fmt.Errorf("count the rows in %s: %w", c.table, err)
		}
	}
	return s, nil
}
