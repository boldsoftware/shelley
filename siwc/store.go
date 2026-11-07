package siwc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// Account is one ChatGPT registration on this host: an issued client ID, the
// verified identity it was registered to, and its current tokens. Signing out
// clears the tokens but keeps the registration, so signing in again reuses it.
type Account struct {
	ID           string    `json:"-"` // local, opaque; the row key
	Email        string    `json:"email,omitempty"`
	Name         string    `json:"name,omitempty"`
	Subject      string    `json:"subject,omitempty"` // empty until the first sign-in completes
	ClientID     string    `json:"client_id"`
	IDToken      string    `json:"id_token,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
	// EarliestRefresh is when OpenAI will next accept a refresh.
	EarliestRefresh time.Time `json:"earliest_refresh_at,omitzero"`
	Scopes          []string  `json:"scopes,omitempty"`
	Models          []Model   `json:"models,omitempty"`
}

// Model is a model the account's ChatGPT plan offers.
type Model struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// SignedIn reports whether the account has tokens to use.
func (a *Account) SignedIn() bool { return a.IDToken != "" }

// PlanUsage reports whether the user granted use of their ChatGPT plan.
func (a *Account) PlanUsage() bool {
	return a.SignedIn() && a.RefreshToken != "" && slices.Contains(a.Scopes, PlanScope)
}

func (a *Account) signOut() {
	a.IDToken, a.AccessToken, a.RefreshToken, a.TokenType = "", "", "", ""
	a.ExpiresAt, a.EarliestRefresh, a.Scopes, a.Models = time.Time{}, time.Time{}, nil, nil
}

// Store keeps the host ID and accounts in Shelley's database.
type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store { return &Store{db: database} }

var errNoAccount = errors.New("no such ChatGPT account")

// hostIDSetting is the settings key holding this host's ext_agent_host_id.
const hostIDSetting = "chatgpt_host_id"

// HostID returns this host's ext_agent_host_id, creating it on first use.
// It is random, so it identifies nothing about the user.
func (s *Store) HostID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
		err := q.SetSettingIfAbsent(ctx, generated.SetSettingIfAbsentParams{Key: hostIDSetting, Value: "urn:uuid:" + uuid.NewString()})
		if err != nil {
			return err
		}
		id, err = q.GetSetting(ctx, hostIDSetting)
		return err
	})
	if err == nil && !strings.HasPrefix(id, "urn:uuid:") {
		err = fmt.Errorf("invalid ChatGPT host ID %q", id)
	}
	return id, err
}

func newAccountID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func decodeAccount(row generated.ChatgptAccount) (*Account, error) {
	var a Account
	if err := json.Unmarshal([]byte(row.Account), &a); err != nil {
		return nil, fmt.Errorf("ChatGPT account %s: %w", row.AccountID, err)
	}
	a.ID = row.AccountID
	return &a, nil
}

func (s *Store) Get(ctx context.Context, id string) (*Account, error) {
	var row generated.ChatgptAccount
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		row, err = q.GetChatGPTAccount(ctx, id)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoAccount
	}
	if err != nil {
		return nil, err
	}
	return decodeAccount(row)
}

// List returns all accounts in registration order.
func (s *Store) List(ctx context.Context) ([]*Account, error) {
	var rows []generated.ChatgptAccount
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		rows, err = q.ListChatGPTAccounts(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	accounts := make([]*Account, 0, len(rows))
	for _, row := range rows {
		a, err := decodeAccount(row)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// Put saves a in one write, so its tokens are replaced together.
func (s *Store) Put(ctx context.Context, a *Account) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return s.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpsertChatGPTAccount(ctx, generated.UpsertChatGPTAccountParams{AccountID: a.ID, Account: string(data)})
	})
}

func (s *Store) Delete(ctx context.Context, id string) error {
	var n int64
	err := s.db.QueriesTx(ctx, func(q *generated.Queries) (err error) {
		n, err = q.DeleteChatGPTAccount(ctx, id)
		return err
	})
	if err == nil && n == 0 {
		err = errNoAccount
	}
	return err
}
