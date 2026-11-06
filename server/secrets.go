package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/featureflags"
)

// Secrets (docs/secrets.md): the composer takes secrets out of messages
// before they leave the browser and stores them here, leaving
// {{secret:ID}}; the write_secret tool puts a value in a file, and every
// tool result is redacted back to placeholders.

// FlagSecrets turns on capturing secrets and the write_secret tool.
// Stored secrets stay manageable, rendered and redacted with it off.
var FlagSecrets = featureflags.Register(featureflags.Flag{
	Name:        "secrets",
	Description: "Take secrets (API keys, tokens, {{secret=...}}) out of messages before they are sent, store them in Shelley, and let the agent write them to files unseen.",
	Default:     false,
})

// minSecretLen keeps short values, which redaction would find all over
// ordinary output, out of the store.
const minSecretLen = 6

var errNoSecret = errors.New("no secret")

var secretKindPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// secretStore is the secrets table, sealed with AES-256-GCM under the key
// in keyPath (created on first use), and a cache of the values for
// redaction.
type secretStore struct {
	db      *db.DB
	keyPath string
	flagOn  func(context.Context) (bool, error)
	logf    func(msg string, args ...any)

	mu       sync.Mutex
	aead     cipher.AEAD
	replacer *strings.Replacer // nil until loaded; reset by every change
}

func newSecretStore(database *db.DB, keyPath string, flagOn func(context.Context) (bool, error), logf func(string, ...any)) *secretStore {
	return &secretStore{db: database, keyPath: keyPath, flagOn: flagOn, logf: logf}
}

// cipherLocked loads the key, creating it if create is set and there is
// none.
func (st *secretStore) cipherLocked(create bool) (cipher.AEAD, error) {
	if st.aead != nil {
		return st.aead, nil
	}
	key, err := os.ReadFile(st.keyPath)
	if errors.Is(err, fs.ErrNotExist) && create {
		key = make([]byte, 32)
		rand.Read(key)
		if err = os.MkdirAll(filepath.Dir(st.keyPath), 0o700); err == nil {
			err = writeNewFile(st.keyPath, key)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("secrets key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets key %s: %d bytes, want 32", st.keyPath, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	st.aead, err = cipher.NewGCM(block)
	return st.aead, err
}

func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// openLocked decrypts a sealed value; the ID is its additional data, so a
// value can't be moved to another row.
func (st *secretStore) openLocked(id string, sealed []byte) (string, error) {
	aead, err := st.cipherLocked(false)
	if err != nil {
		return "", err
	}
	n := aead.NonceSize()
	if len(sealed) < n {
		return "", fmt.Errorf("secret %s: sealed value too short", id)
	}
	plain, err := aead.Open(nil, sealed[:n], sealed[n:], []byte(id))
	if err != nil {
		return "", fmt.Errorf("secret %s: %w", id, err)
	}
	return string(plain), nil
}

type newSecret struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Value          string `json:"value"`
	ConversationID string `json:"conversation_id"`
}

func validSecretName(name string) bool {
	return name != "" && len(name) <= 100 && !strings.ContainsFunc(name, unicode.IsControl)
}

func (st *secretStore) create(ctx context.Context, in newSecret) error {
	switch {
	case len(in.ID) > 64 || !claudetool.SecretIDPattern.MatchString(in.ID):
		return fmt.Errorf("%w: a secret ID is up to 64 lowercase letters, digits and single dashes", db.ErrInvalid)
	case len(in.Kind) > 59 || !secretKindPattern.MatchString(in.Kind):
		return fmt.Errorf("%w: a secret kind is up to 59 (its ID has five more) lowercase letters, digits and dashes", db.ErrInvalid)
	case !validSecretName(in.Name):
		return fmt.Errorf("%w: a secret name is 1 to 100 characters, without control characters", db.ErrInvalid)
	case len(in.Value) < minSecretLen || len(in.Value) > 64<<10:
		return fmt.Errorf("%w: a secret is %d bytes to 64 KB", db.ErrInvalid, minSecretLen)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	aead, err := st.cipherLocked(true)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	sealed := aead.Seal(nonce, nonce, []byte(in.Value), []byte(in.ID))
	var conversationID *string
	if in.ConversationID != "" {
		conversationID = &in.ConversationID
	}
	err = st.db.QueriesTx(ctx, func(q *generated.Queries) error {
		if _, err := q.GetSealedSecret(ctx, in.ID); err == nil {
			return fmt.Errorf("%w: secret %s exists", db.ErrConflict, in.ID)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if conversationID != nil {
			if _, err := q.GetConversation(ctx, *conversationID); err != nil {
				return fmt.Errorf("conversation %s: %w", *conversationID, err)
			}
		}
		return q.CreateSecret(ctx, generated.CreateSecretParams{ID: in.ID, Name: in.Name, Kind: in.Kind, Sealed: sealed, ConversationID: conversationID})
	})
	st.replacer = nil
	return err
}

// Value implements claudetool.Secrets.
func (st *secretStore) Value(ctx context.Context, id string) (string, error) {
	var sealed []byte
	err := st.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		sealed, err = q.GetSealedSecret(ctx, id)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w %s: the user may have deleted it", errNoSecret, id)
	}
	if err != nil {
		return "", err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.openLocked(id, sealed)
}

func (st *secretStore) rename(ctx context.Context, id, name string) error {
	if !validSecretName(name) {
		return fmt.Errorf("%w: a secret name is 1 to 100 characters, without control characters", db.ErrInvalid)
	}
	return st.db.QueriesTx(ctx, func(q *generated.Queries) error {
		n, err := q.RenameSecret(ctx, generated.RenameSecretParams{Name: name, ID: id})
		if err == nil && n == 0 {
			err = sql.ErrNoRows
		}
		return err
	})
}

func (st *secretStore) delete(ctx context.Context, id string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.replacer = nil
	return st.db.QueriesTx(ctx, func(q *generated.Queries) error {
		n, err := q.DeleteSecret(ctx, id)
		if err == nil && n == 0 {
			err = sql.ErrNoRows
		}
		return err
	})
}

// RecordInjection implements claudetool.Secrets.
func (st *secretStore) RecordInjection(ctx context.Context, id, conversationID, machine, path string) error {
	var conv *string
	if conversationID != "" {
		conv = &conversationID
	}
	return st.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.RecordSecretInjection(ctx, generated.RecordSecretInjectionParams{SecretID: id, ConversationID: conv, Machine: machine, Path: path})
	})
}

// ToolEnabled implements claudetool.Secrets. A failure to read the flag is
// logged, and leaves the tool out.
func (st *secretStore) ToolEnabled(ctx context.Context) bool {
	on, err := st.flagOn(ctx)
	if err != nil {
		st.logf("read secrets flag", "error", err)
	}
	return on
}

// Redact implements claudetool.Secrets: it replaces each stored value, and
// its JSON-escaped form, with the secret's placeholder, longest first.
func (st *secretStore) Redact(ctx context.Context, s string) (string, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.replacer == nil {
		var rows []generated.ListSealedSecretsRow
		err := st.db.Queries(ctx, func(q *generated.Queries) error {
			var err error
			rows, err = q.ListSealedSecrets(ctx)
			return err
		})
		if err != nil {
			return "", err
		}
		type pair struct{ old, new string }
		var pairs []pair
		for _, row := range rows {
			value, err := st.openLocked(row.ID, row.Sealed)
			if err != nil {
				return "", err
			}
			placeholder := claudetool.SecretPlaceholder(row.ID)
			pairs = append(pairs, pair{value, placeholder})
			quoted, _ := json.Marshal(value)
			if escaped := string(quoted[1 : len(quoted)-1]); escaped != value {
				pairs = append(pairs, pair{escaped, placeholder})
			}
		}
		slices.SortStableFunc(pairs, func(a, b pair) int { return len(b.old) - len(a.old) })
		var args []string
		for _, p := range pairs {
			args = append(args, p.old, p.new)
		}
		st.replacer = strings.NewReplacer(args...)
	}
	return st.replacer.Replace(s), nil
}

// Secret is a stored secret as the management page sees it: never its
// value.
type Secret struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Kind             string            `json:"kind"`
	ConversationID   *string           `json:"conversation_id"`
	ConversationSlug *string           `json:"conversation_slug"`
	CreatedAt        time.Time         `json:"created_at"`
	Injections       []SecretInjection `json:"injections"`
}

// SecretInjection is one write_secret call that wrote a secret.
type SecretInjection struct {
	ConversationID *string   `json:"conversation_id"`
	Machine        string    `json:"machine"`
	Path           string    `json:"path"`
	CreatedAt      time.Time `json:"created_at"`
}

func (st *secretStore) list(ctx context.Context) ([]Secret, error) {
	var rows []generated.ListSecretsRow
	var injections []generated.ListSecretInjectionsRow
	err := st.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		if rows, err = q.ListSecrets(ctx); err != nil {
			return err
		}
		injections, err = q.ListSecretInjections(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	byID := map[string][]SecretInjection{}
	for _, in := range injections {
		byID[in.SecretID] = append(byID[in.SecretID], SecretInjection{ConversationID: in.ConversationID, Machine: in.Machine, Path: in.Path, CreatedAt: in.CreatedAt})
	}
	out := make([]Secret, len(rows))
	for i, r := range rows {
		out[i] = Secret{ID: r.ID, Name: r.Name, Kind: r.Kind, ConversationID: r.ConversationID, ConversationSlug: r.ConversationSlug, CreatedAt: r.CreatedAt, Injections: append([]SecretInjection{}, byID[r.ID]...)}
		if out[i].Injections == nil {
			out[i].Injections = []SecretInjection{}
		}
	}
	return out, nil
}

func (s *Server) registerSecretRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/secrets", s.handleListSecrets)
	api.HandleFunc("POST /api/secrets", s.handleCreateSecret)
	api.HandleFunc("PATCH /api/secrets/{id}", s.handleRenameSecret)
	api.HandleFunc("DELETE /api/secrets/{id}", s.handleDeleteSecret)
	api.HandleFunc("POST /api/secrets/{id}/reveal", s.handleRevealSecret)
}

func (s *Server) secretHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, errNoSecret):
		http.Error(w, "No such secret or conversation", http.StatusNotFound)
	case errors.Is(err, db.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, db.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		s.internalError(w, "Secret operation failed", err)
	}
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	secrets, err := s.secrets.list(r.Context())
	if err != nil {
		s.secretHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, secrets)
}

// handleCreateSecret stores a secret the composer took out of a message.
// Its body holds the value: neither it nor its errors are logged.
func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	on, err := s.flagOn(r.Context(), FlagSecrets)
	if err != nil {
		s.secretHTTPError(w, err)
		return
	}
	if !on {
		http.Error(w, "The secrets feature flag is off", http.StatusForbidden)
		return
	}
	var in newSecret
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "Invalid secret JSON", http.StatusBadRequest)
		return
	}
	if err := s.secrets.create(r.Context(), in); err != nil {
		s.secretHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": in.ID})
}

func (s *Server) handleRenameSecret(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	if err := s.secrets.rename(r.Context(), r.PathValue("id"), strings.TrimSpace(in.Name)); err != nil {
		s.secretHTTPError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.secrets.delete(r.Context(), r.PathValue("id")); err != nil {
		s.secretHTTPError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRevealSecret answers the management page's reveal and copy, which
// the user confirms first. POST, so nothing fetches it by accident.
func (s *Server) handleRevealSecret(w http.ResponseWriter, r *http.Request) {
	value, err := s.secrets.Value(r.Context(), r.PathValue("id"))
	if err != nil {
		s.secretHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"value": value})
}

// redactChatMessage replaces stored secrets the composer didn't catch (e.g.
// one pasted again after a reload) in a message from the API.
func (s *Server) redactChatMessage(w http.ResponseWriter, r *http.Request, req *ChatRequest) bool {
	var err error
	if req.Message, err = s.secrets.Redact(r.Context(), req.Message); err != nil {
		s.secretHTTPError(w, err)
		return false
	}
	return true
}
