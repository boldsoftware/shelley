package db

import (
	"context"
	"database/sql"
	"errors"

	"shelley.exe.dev/db/generated"
)

// ErrDefaultVersion means a write named a version of a default that isn't
// its current one: it changed meanwhile.
var ErrDefaultVersion = errors.New("the default has changed: read it again")

// ListDefaults returns the defaults whose keys start with prefix, by key.
func (db *DB) ListDefaults(ctx context.Context, prefix string) ([]generated.Default, error) {
	return db.QueriesRes(ctx, func(q *generated.Queries) ([]generated.Default, error) {
		return q.ListDefaults(ctx, prefix)
	})
}

// GetDefault returns the default key; sql.ErrNoRows if there is none.
func (db *DB) GetDefault(ctx context.Context, key string) (generated.Default, error) {
	return db.QueriesRes(ctx, func(q *generated.Queries) (generated.Default, error) {
		return getDefault(ctx, q, key)
	})
}

// getDefault is key's default, sql.ErrNoRows if there is none (or it was
// deleted).
func getDefault(ctx context.Context, q *generated.Queries, key string) (generated.Default, error) {
	d, err := q.GetDefault(ctx, key)
	if err == nil && d.Value == nil {
		return d, sql.ErrNoRows
	}
	return d, err
}

// PutDefault sets key to value (JSON) and returns the default. If version
// is set, it must be key's current version (0: no such key), else
// ErrDefaultVersion. Setting the value a default has makes no new version.
func (db *DB) PutDefault(ctx context.Context, key, value string, version *int64) (generated.Default, error) {
	return db.QueriesTxRes(ctx, func(q *generated.Queries) (generated.Default, error) {
		cur, err := q.GetDefault(ctx, key)
		row := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return cur, err
		}
		exists := row && cur.Value != nil
		if version != nil && *version != currentVersion(cur, exists) {
			return cur, ErrDefaultVersion
		}
		switch {
		case !row:
			return q.InsertDefault(ctx, generated.InsertDefaultParams{Key: key, Value: &value})
		case exists && *cur.Value == value:
			return cur, nil
		default:
			return q.UpdateDefault(ctx, generated.UpdateDefaultParams{Key: key, Value: &value})
		}
	})
}

// currentVersion is the version a compare-and-swap names for d: 0 if it
// doesn't exist.
func currentVersion(d generated.Default, exists bool) int64 {
	if !exists {
		return 0
	}
	return d.Version
}

// DeleteDefault deletes key, reporting whether it existed. If version is
// set, it must be key's current version, else ErrDefaultVersion. The row
// stays, with no value, so that the key's versions never repeat.
func (db *DB) DeleteDefault(ctx context.Context, key string, version *int64) (bool, error) {
	return db.QueriesTxRes(ctx, func(q *generated.Queries) (bool, error) {
		cur, err := getDefault(ctx, q, key)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if version != nil && *version != currentVersion(cur, exists) {
			return false, ErrDefaultVersion
		}
		if !exists {
			return false, nil
		}
		_, err = q.UpdateDefault(ctx, generated.UpdateDefaultParams{Key: key})
		return err == nil, err
	})
}
