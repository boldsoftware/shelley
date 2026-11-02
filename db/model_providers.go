package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"shelley.exe.dev/db/generated"
)

var ErrProviderConflict = errors.New("model provider changed")

type ProviderModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type ModelProvider struct {
	ProviderID   string
	ProviderType string
	Endpoint     string
	APIKey       string
	DisplayName  string
	Models       []ProviderModel
	Version      int64
}

func modelProviderFromRow(row generated.ModelProvider) (ModelProvider, error) {
	p := ModelProvider{
		ProviderID: row.ProviderID, ProviderType: row.ProviderType, Endpoint: row.Endpoint,
		APIKey: row.ApiKey, DisplayName: row.DisplayName, Version: row.Version,
	}
	if err := json.Unmarshal([]byte(row.Models), &p.Models); err != nil {
		return p, fmt.Errorf("decode provider models: %w", err)
	}
	return p, nil
}

func (db *DB) GetModelProviders(ctx context.Context) ([]ModelProvider, error) {
	providers := []ModelProvider{}
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := generated.New(rx.Conn()).ListModelProviders(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			p, err := modelProviderFromRow(row)
			if err != nil {
				return err
			}
			providers = append(providers, p)
		}
		return nil
	})
	return providers, err
}

func (db *DB) GetModelProvider(ctx context.Context, id string) (*ModelProvider, error) {
	var p ModelProvider
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		row, err := generated.New(rx.Conn()).GetModelProvider(ctx, id)
		if err != nil {
			return err
		}
		p, err = modelProviderFromRow(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (db *DB) CreateModelProvider(ctx context.Context, p ModelProvider) error {
	raw, err := json.Marshal(p.Models)
	if err != nil {
		return err
	}
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return generated.New(tx.Conn()).CreateModelProvider(ctx, generated.CreateModelProviderParams{
			ProviderID: p.ProviderID, ProviderType: p.ProviderType, Endpoint: p.Endpoint,
			ApiKey: p.APIKey, DisplayName: p.DisplayName, Models: string(raw),
		})
	})
}

func (db *DB) UpdateModelProvider(ctx context.Context, p ModelProvider, expectedVersion int64) error {
	raw, err := json.Marshal(p.Models)
	if err != nil {
		return err
	}
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		n, err := q.UpdateModelProvider(ctx, generated.UpdateModelProviderParams{
			ProviderType: p.ProviderType, Endpoint: p.Endpoint, ApiKey: p.APIKey, DisplayName: p.DisplayName,
			Models: string(raw), ProviderID: p.ProviderID, Version: expectedVersion,
		})
		if err != nil || n != 0 {
			return err
		}
		if _, err := q.GetModelProvider(ctx, p.ProviderID); err != nil {
			return err
		}
		return ErrProviderConflict
	})
}

func (db *DB) DeleteModelProvider(ctx context.Context, id string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return rowsChanged(generated.New(tx.Conn()).DeleteModelProvider(ctx, id))
	})
}
