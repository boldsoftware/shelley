package server

// defaults.go: GET/POST /api/defaults, a key-value store of JSON values
// that clients keep their settings in (exehome keeps its own here, a key
// per part, prefixed exehome.). Shelley itself never reads them. See
// API.md.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// defaultKey is what a key looks like.
var defaultKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// maxDefaultValue bounds a value, as compact JSON.
const maxDefaultValue = 1 << 20

// defaultEntry is a default as the API has it.
type defaultEntry struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Version   int64           `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// toDefaultEntry is d, which exists (has a value).
func toDefaultEntry(d generated.Default) defaultEntry {
	return defaultEntry{
		Key: d.Key, Value: json.RawMessage(*d.Value), Version: d.Version,
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(),
	}
}

// pathDefaultKey is the request's {key}; if it isn't a key, it answers 400
// and returns "".
func pathDefaultKey(w http.ResponseWriter, r *http.Request) string {
	key := r.PathValue("key")
	if !defaultKey.MatchString(key) {
		http.Error(w, "a key is 1-128 letters, digits, '.', '_' or '-', starting with a letter or digit", http.StatusBadRequest)
		return ""
	}
	return key
}

// GET /api/defaults?prefix=P → {"defaults": [entry, ...]}, by key.
func (s *Server) handleListDefaults(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.ListDefaults(r.Context(), r.URL.Query().Get("prefix"))
	if err != nil {
		s.internalError(w, "Failed to list defaults", err)
		return
	}
	out := make([]defaultEntry, len(rows))
	for i, d := range rows {
		out[i] = toDefaultEntry(d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"defaults": out})
}

// GET /api/defaults/{key} → entry, or 404.
func (s *Server) handleGetDefault(w http.ResponseWriter, r *http.Request) {
	key := pathDefaultKey(w, r)
	if key == "" {
		return
	}
	d, err := s.db.GetDefault(r.Context(), key)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such default", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to get default", err, "key", key)
		return
	}
	writeJSON(w, http.StatusOK, toDefaultEntry(d))
}

// POST /api/defaults/{key} {"value": JSON, "version"?: N} → entry. With
// version, the write happens only if the key is at that version (0: it
// doesn't exist); else 409.
func (s *Server) handlePostDefault(w http.ResponseWriter, r *http.Request) {
	key := pathDefaultKey(w, r)
	if key == "" {
		return
	}
	var req struct {
		Value   json.RawMessage `json:"value"`
		Version *int64          `json:"version"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*maxDefaultValue))
	err := dec.Decode(&req)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("more after the body")
	}
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "the value is larger than 1 MiB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Value == nil || string(req.Value) == "null" {
		http.Error(w, "value is required (DELETE removes a default)", http.StatusBadRequest)
		return
	}
	var value bytes.Buffer
	if err := json.Compact(&value, req.Value); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if value.Len() > maxDefaultValue {
		http.Error(w, "the value is larger than 1 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	d, err := s.db.PutDefault(r.Context(), key, value.String(), req.Version)
	if errors.Is(err, db.ErrDefaultVersion) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to set default", err, "key", key)
		return
	}
	writeJSON(w, http.StatusOK, toDefaultEntry(d))
}

// DELETE /api/defaults/{key}[?version=N] → 204, or 404. With version, only
// if the key is at that version; else 409.
func (s *Server) handleDeleteDefault(w http.ResponseWriter, r *http.Request) {
	key := pathDefaultKey(w, r)
	if key == "" {
		return
	}
	var version *int64
	if v := r.URL.Query().Get("version"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "version must be a number", http.StatusBadRequest)
			return
		}
		version = &n
	}
	existed, err := s.db.DeleteDefault(r.Context(), key, version)
	switch {
	case errors.Is(err, db.ErrDefaultVersion):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		s.internalError(w, "Failed to delete default", err, "key", key)
	case !existed:
		http.Error(w, "no such default", http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
