package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"shelley.exe.dev/featureflags"
)

// featureFlagBool resolves the effective boolean value of a registered flag,
// applying any persisted override on top of the flag's registered default. It
// is the server-side counterpart to the UI's useFeatureFlag composable, for
// features (like the favicon) that are decided while rendering on the server.
// On any error, or when the override/default is not a boolean, it falls back
// to the flag's default (and finally to false).
//
// Note this only consults the DB-backed override, not the UI's per-tab
// `ff:<name>` localStorage override (which is client-only and used for E2E
// isolation): a server-rendered feature must be toggled via a DB override.
func (s *Server) featureFlagBool(ctx context.Context, flag featureflags.Flag) bool {
	def, _ := flag.Default.(bool)
	overrides, err := s.db.GetAllFeatureFlagOverrides(ctx)
	if err != nil {
		s.logger.Error("Failed to get feature flag overrides", "error", err, "flag", flag.Name)
		return def
	}
	raw, ok := overrides[flag.Name]
	if !ok {
		return def
	}
	var v bool
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return def
	}
	return v
}

// FeatureFlagDTO is the API shape: the static registry entry plus an optional
// override (raw JSON string). Unknown overrides in the DB are silently skipped.
type FeatureFlagDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Default     any    `json:"default"`
	// Override is the JSON-encoded override value, or nil if no override is set.
	// Sent as a json.RawMessage so the client receives the value (not a string).
	Override *json.RawMessage `json:"override,omitempty"`
}

func (s *Server) handleGetFeatureFlags(w http.ResponseWriter, r *http.Request) {
	overrides, err := s.db.GetAllFeatureFlagOverrides(r.Context())
	if err != nil {
		s.logger.Error("Failed to get feature flag overrides", "error", err)
		http.Error(w, fmt.Sprintf("Failed to get feature flags: %v", err), http.StatusInternalServerError)
		return
	}
	flags := featureflags.All()
	out := make([]FeatureFlagDTO, 0, len(flags))
	for _, f := range flags {
		dto := FeatureFlagDTO{Name: f.Name, Description: f.Description, Default: f.Default}
		if raw, ok := overrides[f.Name]; ok {
			m := json.RawMessage(raw)
			dto.Override = &m
		}
		out = append(out, dto)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) handleSetFeatureFlag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !featureflags.Known(req.Name) {
		http.Error(w, fmt.Sprintf("Unknown feature flag: %s", req.Name), http.StatusBadRequest)
		return
	}
	if len(req.Value) == 0 {
		http.Error(w, "value is required (use DELETE to clear)", http.StatusBadRequest)
		return
	}
	// Validate the bytes are valid JSON by round-tripping through any.
	var dummy any
	if err := json.Unmarshal(req.Value, &dummy); err != nil {
		http.Error(w, fmt.Sprintf("value must be valid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if err := s.db.SetFeatureFlagOverride(r.Context(), req.Name, string(req.Value)); err != nil {
		s.logger.Error("Failed to set feature flag", "error", err, "name", req.Name)
		http.Error(w, fmt.Sprintf("Failed to set feature flag: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteFeatureFlag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	// Allow deletion of unknown names too (cleans up stale rows).
	if err := s.db.DeleteFeatureFlagOverride(r.Context(), req.Name); err != nil {
		s.logger.Error("Failed to delete feature flag", "error", err, "name", req.Name)
		http.Error(w, fmt.Sprintf("Failed to delete feature flag: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
