package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// KVGet returns the value of key. ok is false when the key has no value.
func (d *DB) KVGet(ctx context.Context, key string) (value string, ok bool, err error) {
	err = d.QueryRowContext(ctx, "SELECT value FROM kv WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: read kv %q: %w", key, err)
	}
	return value, true, nil
}

// KVSet stores value under key. It replaces an earlier value.
func (d *DB) KVSet(ctx context.Context, key, value string) error {
	_, err := d.ExecContext(ctx,
		"INSERT INTO kv(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	if err != nil {
		return fmt.Errorf("db: write kv %q: %w", key, err)
	}
	return nil
}
