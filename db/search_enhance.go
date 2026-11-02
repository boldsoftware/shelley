package db

import (
	"context"

	"shelley.exe.dev/db/generated"
)

func (db *DB) SearchEnhancementModel(ctx context.Context, email string) (string, error) {
	var model string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		model, err = generated.New(rx.Conn()).SearchEnhancementModel(ctx, email)
		return err
	})
	return model, err
}

type SearchMessageContext = generated.SearchMessageContextRow

func (db *DB) SearchMessageContext(ctx context.Context, ids []string) ([]SearchMessageContext, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var result []SearchMessageContext
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		result, err = generated.New(rx.Conn()).SearchMessageContext(ctx, ids)
		return err
	})
	return result, err
}
