package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
)

const (
	livePreferencesKey = "live_settings"
	defaultLiveVoice   = "marin"
)

// Voices explicitly documented for GPT-Live, including its default.
var liveVoices = []string{
	"beacon", "bossa", "brise", "cinder", "delta", "flitz", "gleam",
	"harema", "juni", "marin", "meridian", "nira", "noeul", "nuri",
	"quartz", "ripple", "shida", "sillage", "stone", "tempo", "vesper", "willow",
}

// livePreferences belong to the server instance, not to any profile or
// conversation. They are read once for each new call.
type livePreferences struct {
	Voice           string `json:"voice"`
	SpeakingPace    int    `json:"speaking_pace"`
	ProgressUpdates bool   `json:"progress_updates"`
}

func (p livePreferences) problem() string {
	if !slices.Contains(liveVoices, p.Voice) {
		return "invalid Live voice"
	}
	if p.SpeakingPace < -2 || p.SpeakingPace > 2 {
		return "invalid Live speaking pace"
	}
	return ""
}

func (s *Server) getLivePreferences(ctx context.Context) (livePreferences, error) {
	p := livePreferences{Voice: defaultLiveVoice}
	value, err := s.db.GetSetting(ctx, livePreferencesKey)
	if err != nil {
		return p, err
	}
	if value != "" {
		if err := json.Unmarshal([]byte(value), &p); err != nil {
			return p, fmt.Errorf("decode Live settings: %w", err)
		}
	}
	if msg := p.problem(); msg != "" {
		return p, fmt.Errorf("stored Live settings: %s", msg)
	}
	return p, nil
}

func (s *Server) registerLivePreferenceRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/live-settings", s.handleGetLivePreferences)
	api.HandleFunc("PUT /api/live-settings", s.handlePutLivePreferences)
	api.HandleFunc("GET /api/live-settings/options", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"voices": liveVoices,
		})
	})
}

func (s *Server) handleGetLivePreferences(w http.ResponseWriter, r *http.Request) {
	p, err := s.getLivePreferences(r.Context())
	if err != nil {
		s.internalError(w, "Load Live settings", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePutLivePreferences(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Voice           *string `json:"voice"`
		SpeakingPace    *int    `json:"speaking_pace"`
		ProgressUpdates *bool   `json:"progress_updates"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !decodeStrict(w, r, &input) {
		return
	}
	if input.Voice == nil || input.SpeakingPace == nil || input.ProgressUpdates == nil {
		http.Error(w, "voice, speaking_pace and progress_updates are required", http.StatusBadRequest)
		return
	}
	p := livePreferences{Voice: *input.Voice, SpeakingPace: *input.SpeakingPace, ProgressUpdates: *input.ProgressUpdates}
	if msg := p.problem(); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	value, err := json.Marshal(p)
	if err != nil {
		s.internalError(w, "Encode Live settings", err)
		return
	}
	if err := s.db.SetSetting(r.Context(), livePreferencesKey, string(value)); err != nil {
		s.internalError(w, "Save Live settings", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
