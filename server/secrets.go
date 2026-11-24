package server

import (
	"cmp"
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
	"unicode/utf16"
	"unicode/utf8"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/featureflags"
	"shelley.exe.dev/secretdetect"
)

// Secrets (docs/secrets.md): the chat API takes secrets out of the user's
// messages and stores them here for secretLifetime, leaving a marker that
// tells the agent to read one with `shelley secret ID` (client/secret.go);
// every tool result is redacted back to markers.

// FlagSecrets turns on taking secrets out of messages. Stored secrets stay
// readable, manageable and redacted with it off.
var FlagSecrets = featureflags.Register(featureflags.Flag{
	Name:        "secrets",
	Description: "Capture secrets from chat input so they aren't sent to the LLM provider. The agent reads them with shelley secret; they expire after a day.",
	Default:     true,
})

// secretLifetime is how long a secret is kept.
const secretLifetime = 24 * time.Hour

// minSecretLen keeps short values, which redaction would find all over
// ordinary output, out of the store.
const minSecretLen = 6

var errNoSecret = errors.New("expired or was deleted")

var (
	secretIDPattern   = regexp.MustCompile(`^(?:` + secretdetect.IDPattern + `)$`)
	secretKindPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// secretStore is the secrets table, sealed with AES-256-GCM under the key
// in keyPath (created on first use), and a cache of the values for
// redaction.
type secretStore struct {
	db       *db.DB
	keyPath  string
	lifetime time.Duration // of new secrets

	mu       sync.Mutex
	aead     cipher.AEAD
	replacer *strings.Replacer // nil until loaded; reset by every change
	ids      map[string]string // by value, and JSON-escaped; loaded with replacer
	// replacerExpiry is when the first secret the replacer holds expires.
	replacerExpiry time.Time
}

func newSecretStore(database *db.DB, keyPath string) *secretStore {
	return &secretStore{db: database, keyPath: keyPath, lifetime: secretLifetime}
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
	ID             string
	Name           string
	Kind           string
	Value          string
	ConversationID string
}

func validSecretName(name string) bool {
	return name != "" && len(name) <= 100 && !strings.ContainsFunc(name, unicode.IsControl)
}

// validSecretValue keeps short values, which redaction would find all over
// ordinary output, and huge ones out of the store.
func validSecretValue(value string) error {
	switch {
	case len(value) < minSecretLen:
		return fmt.Errorf("%w: secrets must be at least %d bytes", db.ErrInvalid, minSecretLen)
	case len(value) > 64<<10:
		return fmt.Errorf("%w: secrets must be at most 64 KB", db.ErrInvalid)
	}
	return nil
}

func (st *secretStore) create(ctx context.Context, in newSecret) error {
	switch {
	case len(in.ID) > 64 || !secretIDPattern.MatchString(in.ID):
		return fmt.Errorf("%w: a secret ID is up to 64 lowercase letters, digits and single dashes", db.ErrInvalid)
	case len(in.Kind) > 59 || !secretKindPattern.MatchString(in.Kind):
		return fmt.Errorf("%w: a secret kind is up to 59 (its ID has five more) lowercase letters, digits and dashes", db.ErrInvalid)
	case !validSecretName(in.Name):
		return fmt.Errorf("%w: a secret name is 1 to 100 characters, without control characters", db.ErrInvalid)
	}
	if err := validSecretValue(in.Value); err != nil {
		return err
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
		if _, err := q.DeleteExpiredSecrets(ctx); err != nil {
			return err
		}
		if _, err := q.GetSealedSecret(ctx, in.ID); err == nil {
			return fmt.Errorf("%w: secret %s exists", db.ErrConflict, in.ID)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return q.CreateSecret(ctx, generated.CreateSecretParams{
			ID: in.ID, Name: in.Name, Kind: in.Kind, Sealed: sealed, ConversationID: conversationID,
			Lifetime: fmt.Sprintf("%+d seconds", int64(st.lifetime/time.Second)),
		})
	})
	st.replacer = nil
	return err
}

// Value is a secret's plaintext.
func (st *secretStore) Value(ctx context.Context, id string) (string, error) {
	var sealed []byte
	err := st.db.QueriesTx(ctx, func(q *generated.Queries) error {
		if _, err := q.DeleteExpiredSecrets(ctx); err != nil {
			return err
		}
		var err error
		sealed, err = q.GetSealedSecret(ctx, id)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%s %w", id, errNoSecret)
	}
	if err != nil {
		return "", err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.openLocked(id, sealed)
}

func (st *secretStore) delete(ctx context.Context, id string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.replacer = nil
	return st.db.QueriesTx(ctx, func(q *generated.Queries) error {
		n, err := q.DeleteSecret(ctx, id)
		if err == nil && n == 0 {
			err = fmt.Errorf("%s %w", id, errNoSecret)
		}
		return err
	})
}

// Redact implements claudetool.Secrets: it replaces each stored value, and
// its JSON-escaped form, with the secret's marker, longest first.
func (st *secretStore) Redact(ctx context.Context, s string) (string, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.loadLocked(ctx); err != nil {
		return "", err
	}
	return st.replacer.Replace(s), nil
}

// loadLocked loads the stored values into replacer and ids, unless they
// are loaded and none has expired since.
func (st *secretStore) loadLocked(ctx context.Context) error {
	if st.replacer != nil && time.Now().Before(st.replacerExpiry) {
		return nil
	}
	var rows []generated.ListSealedSecretsRow
	err := st.db.QueriesTx(ctx, func(q *generated.Queries) error {
		if _, err := q.DeleteExpiredSecrets(ctx); err != nil {
			return err
		}
		var err error
		rows, err = q.ListSealedSecrets(ctx)
		return err
	})
	if err != nil {
		return err
	}
	type pair struct{ old, new string }
	var pairs []pair
	ids := map[string]string{}
	expiry := time.Now().Add(st.lifetime)
	for _, row := range rows {
		value, err := st.openLocked(row.ID, row.Sealed)
		if err != nil {
			return err
		}
		if row.ExpiresAt.Before(expiry) {
			expiry = row.ExpiresAt
		}
		ids[value] = row.ID
		marker := secretdetect.Marker(row.ID)
		pairs = append(pairs, pair{value, marker})
		quoted, _ := json.Marshal(value)
		if escaped := string(quoted[1 : len(quoted)-1]); escaped != value {
			pairs = append(pairs, pair{escaped, marker})
			ids[escaped] = row.ID
		}
	}
	slices.SortStableFunc(pairs, func(a, b pair) int { return len(b.old) - len(a.old) })
	var args []string
	for _, p := range pairs {
		args = append(args, p.old, p.new)
	}
	st.replacer, st.ids, st.replacerExpiry = strings.NewReplacer(args...), ids, expiry
	return nil
}

// extraction says what extract takes out of a message besides the values
// already stored.
type extraction struct {
	// marksOnly takes only the user's own marks: the sender said what
	// secretdetect finds holds no secret.
	marksOnly bool
	// lenient leaves a mark that can't be stored as it is instead of
	// failing the message, for senders who would never see the error.
	lenient bool
}

// secretSpan is a span of a message extract takes out: a value stored
// already (ID set), or a finding, whose value may be stored too.
type secretSpan struct {
	secretdetect.Finding
	ID string
}

// spans returns what extract would take out of message, in order: where
// stored values appear, and what secretdetect finds (only the user's marks
// if x.marksOnly). Of spans that overlap, the longest (or a finding) wins,
// so a stored value keeps a token inside it, and a new secret a stored
// value.
func (st *secretStore) spans(ctx context.Context, message string, x extraction) ([]secretSpan, error) {
	st.mu.Lock()
	err := st.loadLocked(ctx)
	ids := st.ids // replaced, never changed, by a reload
	st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var all []secretSpan
	for _, f := range secretdetect.Detect(message) {
		id := ids[f.Value]
		// A finding extract would leave alone mustn't hide a stored value.
		if (x.marksOnly || validSecretValue(f.Value) != nil) && !f.Manual && id == "" {
			continue
		}
		all = append(all, secretSpan{f, id})
	}
	for value, id := range ids {
		for at := 0; ; {
			i := strings.Index(message[at:], value)
			if i < 0 {
				break
			}
			at += i + len(value)
			all = append(all, secretSpan{secretdetect.Finding{Start: at - len(value), End: at, Value: value}, id})
		}
	}
	slices.SortStableFunc(all, func(a, b secretSpan) int {
		return cmp.Or((b.End-b.Start)-(a.End-a.Start), a.Start-b.Start)
	})
	var out []secretSpan // by Start, without overlaps
	for _, sp := range all {
		i, _ := slices.BinarySearchFunc(out, sp.Start, func(o secretSpan, start int) int { return o.Start - start })
		if (i > 0 && out[i-1].End > sp.Start) || (i < len(out) && out[i].Start < sp.End) {
			continue
		}
		out = slices.Insert(out, i, sp)
	}
	return out, nil
}

// extract takes the secrets out of a message from the user (see spans):
// each becomes its marker, and a new one is stored, belonging to the
// conversation. A manual mark that can't be stored fails it unless x is
// lenient; another finding that can't is left alone.
func (st *secretStore) extract(ctx context.Context, conversationID, message string, x extraction) (string, error) {
	spans, err := st.spans(ctx, message, x)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	last := 0
	ids := map[string]string{} // stored by this message, by value
	for _, sp := range spans {
		id := cmp.Or(sp.ID, ids[sp.Value])
		if id == "" {
			if err := validSecretValue(sp.Value); err != nil {
				if sp.Manual && !x.lenient {
					return "", err
				}
				continue
			}
			if id, err = st.store(ctx, conversationID, sp.Finding); err != nil {
				return "", err
			}
			ids[sp.Value] = id
		}
		b.WriteString(message[last:sp.Start])
		b.WriteString(secretdetect.Marker(id))
		last = sp.End
	}
	b.WriteString(message[last:])
	return b.String(), nil
}

// store stores a finding under the ID a mark asks for or, if it's taken,
// too long or there is none, its kind and four random characters.
func (st *secretStore) store(ctx context.Context, conversationID string, f secretdetect.Finding) (string, error) {
	name := cmp.Or(f.Name, secretdetect.KindLabel(f.Kind))
	if len(name) > 100 {
		name = f.Kind
	}
	in := newSecret{Name: name, Kind: f.Kind, Value: f.Value, ConversationID: conversationID}
	if len(f.Name) <= 64 { // longer can't be an ID
		in.ID = f.Name
	}
	for range 5 {
		if in.ID == "" {
			in.ID = f.Kind + "-" + strings.ToLower(rand.Text()[:4])
		}
		err := st.create(ctx, in)
		if !errors.Is(err, db.ErrConflict) {
			return in.ID, err
		}
		in.ID = ""
	}
	return "", fmt.Errorf("no free ID for a %s secret", f.Kind)
}

// Secret is a stored secret as the secrets modal lists it: never its
// value.
type Secret struct {
	ID               string    `json:"id"`
	Kind             string    `json:"kind"`
	ConversationID   *string   `json:"conversation_id"`
	ConversationSlug *string   `json:"conversation_slug"`
	ExpiresAt        time.Time `json:"expires_at"`
}

func (st *secretStore) list(ctx context.Context) ([]Secret, error) {
	var rows []generated.ListSecretsRow
	err := st.db.QueriesTx(ctx, func(q *generated.Queries) error {
		if _, err := q.DeleteExpiredSecrets(ctx); err != nil {
			return err
		}
		var err error
		rows, err = q.ListSecrets(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Secret, len(rows))
	for i, r := range rows {
		out[i] = Secret{
			ID: r.ID, Kind: r.Kind, ConversationID: r.ConversationID,
			ConversationSlug: r.ConversationSlug, ExpiresAt: r.ExpiresAt,
		}
	}
	return out, nil
}

func (s *Server) registerSecretRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/secrets", s.handleListSecrets)
	api.HandleFunc("POST /api/secrets/detect", s.handleDetectSecrets)
	api.HandleFunc("DELETE /api/secrets/{id}", s.handleDeleteSecret)
	api.HandleFunc("POST /api/secrets/{id}/reveal", s.handleRevealSecret)
}

func (s *Server) secretHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNoSecret):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, db.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, db.ErrInvalid):
		http.Error(w, strings.TrimPrefix(err.Error(), db.ErrInvalid.Error()+": "), http.StatusBadRequest)
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

// SecretSpan is a secret the chat API would take out of a text, for the
// composer to mark. Start and End count UTF-16 code units, as JavaScript
// strings do.
type SecretSpan struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Manual marks the user's own {{secret=...}}, and Stored a value stored
	// already (Kind is empty unless secretdetect found it too):
	// keep_found_secrets keeps neither.
	Manual bool `json:"manual,omitempty"`
	Stored bool `json:"stored,omitempty"`
}

// handleDetectSecrets answers which spans of a draft the chat API would take
// out: none with the flag off. Its body is the user's text: neither it nor
// its errors are logged.
func (s *Server) handleDetectSecrets(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	spans := []SecretSpan{}
	on, err := s.flagOn(r.Context(), FlagSecrets)
	if err != nil {
		s.secretHTTPError(w, err)
		return
	}
	if on {
		found, err := s.secrets.spans(r.Context(), in.Text, extraction{})
		if err != nil {
			s.secretHTTPError(w, err)
			return
		}
		offsets := make([]int, 0, 2*len(found))
		for _, sp := range found {
			offsets = append(offsets, sp.Start, sp.End)
		}
		offsets = utf16Offsets(in.Text, offsets)
		for i, sp := range found {
			spans = append(spans, SecretSpan{
				Start: offsets[2*i], End: offsets[2*i+1],
				Kind: sp.Kind, Label: secretdetect.KindLabel(sp.Kind),
				Manual: sp.Manual, Stored: sp.ID != "",
			})
		}
	}
	writeJSON(w, http.StatusOK, spans)
}

// utf16Offsets converts ascending byte offsets in s to UTF-16 offsets.
func utf16Offsets(s string, offsets []int) []int {
	out := make([]int, len(offsets))
	units, pos := 0, 0
	for i, off := range offsets {
		for pos < off {
			r, size := utf8.DecodeRuneInString(s[pos:])
			units += utf16.RuneLen(r)
			pos += size
		}
		out[i] = units
	}
	return out
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.secrets.delete(r.Context(), r.PathValue("id")); err != nil {
		s.secretHTTPError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRevealSecret answers the secrets modal's Show and Copy, and
// `shelley secret ID`. POST, so nothing fetches it by accident.
func (s *Server) handleRevealSecret(w http.ResponseWriter, r *http.Request) {
	value, err := s.secrets.Value(r.Context(), r.PathValue("id"))
	if err != nil {
		s.secretHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"value": value})
}

// extractChatSecrets takes the secrets out of a message from the chat API,
// and replaces stored ones in the views' context that comes with it.
func (s *Server) extractChatSecrets(w http.ResponseWriter, r *http.Request, conversationID string, req *ChatRequest) bool {
	if !s.extractMessageSecrets(w, r, conversationID, &req.Message, req.KeepFoundSecrets) {
		return false
	}
	for i := range req.AppContext {
		for _, text := range []*string{&req.AppContext[i].Source, &req.AppContext[i].Text} {
			var err error
			if *text, err = s.secrets.Redact(r.Context(), *text); err != nil {
				s.secretHTTPError(w, err)
				return false
			}
		}
	}
	return true
}

// extractMessageSecrets takes the secrets out of a message (with the flag
// off, only values already stored). With skip, the sender said what the
// composer flagged holds no secret: only their marks go.
func (s *Server) extractMessageSecrets(w http.ResponseWriter, r *http.Request, conversationID string, message *string, skip bool) bool {
	ctx := r.Context()
	on, err := s.flagOn(ctx, FlagSecrets)
	if err == nil {
		if on {
			*message, err = s.secrets.extract(ctx, conversationID, *message, extraction{marksOnly: skip})
		} else {
			*message, err = s.secrets.Redact(ctx, *message)
		}
	}
	if err != nil {
		s.secretHTTPError(w, err)
		return false
	}
	return true
}

// extractChannelSecrets takes the secrets out of a message from a channel
// (see channels.go), as extractChatSecrets does for the chat API.
func (s *Server) extractChannelSecrets(ctx context.Context, conversationID, message string) (string, error) {
	on, err := s.flagOn(ctx, FlagSecrets)
	if err != nil {
		return "", err
	}
	if !on {
		return s.secrets.Redact(ctx, message)
	}
	return s.secrets.extract(ctx, conversationID, message, extraction{lenient: true})
}

// checkSecretMarks fails a message with a manual mark extractMessageSecrets
// would refuse, so the chat API refuses it before it makes anything.
func (s *Server) checkSecretMarks(ctx context.Context, message string) error {
	if on, err := s.flagOn(ctx, FlagSecrets); err != nil || !on {
		return err
	}
	for _, f := range secretdetect.Detect(message) {
		if f.Manual {
			if err := validSecretValue(f.Value); err != nil {
				return err
			}
		}
	}
	return nil
}
