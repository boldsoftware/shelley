package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

const (
	liveVoiceSettingKey = "live_voice"
	defaultLiveVoice    = "marin"
)

// liveVoices are OpenAI's documented GPT-Live output voices:
// https://developers.openai.com/api/docs/guides/live-conversations#voice-options
var liveVoices = []string{
	"alloy", "ash", "ballad", "beacon", "bossa", "brise",
	"calandria", "cedar", "cinder", "compas", "coral", "delta",
	"destello", "echo", "flitz", "gleam", "halo", "harema",
	"hornero", "juni", "luciernaga", "marin", "meridian", "nira",
	"noeul", "nuri", "pearl", "quartz", "ripple", "sage",
	"shida", "shimmer", "sillage", "stone", "tempo", "verse",
	"vesper", "willow",
}

func liveVoiceProblem(voice string) string {
	if voice != "" && !slices.Contains(liveVoices, voice) {
		return fmt.Sprintf("unknown voice %q", voice)
	}
	return ""
}

func (s *Server) currentLiveVoice(ctx context.Context) (string, error) {
	voice, err := s.db.GetSetting(ctx, liveVoiceSettingKey)
	if err != nil {
		return "", fmt.Errorf("get live voice: %w", err)
	}
	if voice == "" {
		return defaultLiveVoice, nil
	}
	return voice, nil
}

type liveVoiceResponse struct {
	Voice  string   `json:"voice"`
	Voices []string `json:"voices"`
}

// handleGetLiveVoice handles GET /api/live-voice.
func (s *Server) handleGetLiveVoice(w http.ResponseWriter, r *http.Request) {
	voice, err := s.currentLiveVoice(r.Context())
	if err != nil {
		s.internalError(w, "Failed to get live voice", err)
		return
	}
	writeJSON(w, http.StatusOK, liveVoiceResponse{Voice: voice, Voices: liveVoices})
}

// handleSetLiveVoice handles PUT /api/live-voice.
func (s *Server) handleSetLiveVoice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Voice string `json:"voice"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	voice := strings.TrimSpace(req.Voice)
	if msg := liveVoiceProblem(voice); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	var err error
	if voice == "" || voice == defaultLiveVoice {
		err = s.db.DeleteSetting(r.Context(), liveVoiceSettingKey)
	} else {
		err = s.db.SetSetting(r.Context(), liveVoiceSettingKey, voice)
	}
	if err != nil {
		s.internalError(w, "Failed to set live voice", err)
		return
	}
	s.handleGetLiveVoice(w, r)
}
