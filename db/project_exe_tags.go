package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"shelley.exe.dev/db/generated"
)

// ProjectExeTag is project's exe.dev tag, and whether it is saved: else
// the tag SaveProjectExeTag would save, base or, if another project has
// that, base-2, base-3, ….
func (db *DB) ProjectExeTag(ctx context.Context, project, base string) (tag string, saved bool, err error) {
	err = db.Queries(ctx, func(q *generated.Queries) error {
		tag, saved, err = projectExeTag(ctx, q, project, base)
		return err
	})
	return tag, saved, err
}

// SaveProjectExeTag saves project's exe.dev tag, unless it has one, and
// returns it.
func (db *DB) SaveProjectExeTag(ctx context.Context, project, base string) (tag string, err error) {
	err = db.QueriesTx(ctx, func(q *generated.Queries) error {
		var saved bool
		if tag, saved, err = projectExeTag(ctx, q, project, base); err != nil || saved {
			return err
		}
		return q.CreateProjectExeTag(ctx, generated.CreateProjectExeTagParams{Profile: project, Tag: tag})
	})
	return tag, err
}

func projectExeTag(ctx context.Context, q *generated.Queries, project, base string) (string, bool, error) {
	tag, err := q.GetProjectExeTag(ctx, project)
	if err == nil {
		return tag, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	tag = base
	for i := 2; ; i++ {
		taken, err := q.ProjectExeTagTaken(ctx, tag)
		if err != nil || !taken {
			return tag, false, err
		}
		tag = fmt.Sprintf("%s-%d", base, i)
	}
}
