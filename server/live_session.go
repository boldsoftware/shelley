package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const maxLiveOfferBytes = 128 << 10

func liveSessionEndpoint() string {
	if endpoint := os.Getenv("SHELLEY_LIVE_SESSION_URL"); endpoint != "" {
		return endpoint
	}
	// The attached LLM integration authenticates this VM and uses exe.dev's
	// managed key. An unrelated OPENAI_API_KEY must not make voice bill the
	// user; direct OpenAI is available only with an explicit endpoint override.
	return "https://llm.int.exe.xyz/v1/live/sessions"
}

func liveSessionAPIKey(endpoint string) string {
	if endpoint == "https://api.openai.com/v1/live/sessions" {
		return os.Getenv("OPENAI_API_KEY")
	}
	return ""
}

// liveInstructions follows OpenAI's GPT-Live prompt template: Live owns the
// spoken conversation and decides when to delegate; the client delegation
// handler (Shelley) does the work.
const liveInstructions = `You are the live voice of Shelley, a coding agent working in this conversation. Speak warmly and naturally, at an unhurried pace. Be clear and direct, not overly cheerful. If the user is frustrated, acknowledge it briefly and focus on the next helpful step.

Backchannel policy: Use moderate backchannels. Acknowledge naturally without competing with the main response.

Interruption policy: Stop speaking when the user interrupts. Listen to what they say.

Delegation policy:
Backend: Shelley's backend agent does the coding, runs tools and commands, reads and changes files, and investigates facts about the project and machine.

Delegate to the backend when:
- The request needs coding, a tool, a command, or factual investigation.
- A correction changes work already requested.

Do not delegate when:
- The user is greeting you or making small talk.
- You need a brief clarification to understand the request.
- You can answer from this conversation or a still-current backend result.

Delegate before giving an answer that depends on backend work. Say briefly what you are handing off, then wait. Do not invent or guess results, progress or completion; report only what the backend returns.`

func (s *Server) handleLiveSession(w http.ResponseWriter, r *http.Request, conversationID string) {
	conversation, err := s.db.GetConversationByID(r.Context(), conversationID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}

	var offer struct {
		SDP string `json:"sdp"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLiveOfferBytes)).Decode(&offer); err != nil {
		status := http.StatusBadRequest
		if _, ok := err.(*http.MaxBytesError); ok {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "Invalid SDP offer", status)
		return
	}
	if !strings.HasPrefix(offer.SDP, "v=0") {
		http.Error(w, "Invalid SDP offer", http.StatusBadRequest)
		return
	}

	history, err := s.liveContext(r.Context(), conversationID)
	if err != nil {
		s.internalError(w, "Load Live context", err)
		return
	}
	payload, err := json.Marshal(map[string]any{
		"session": map[string]any{
			"model":        "gpt-live-1",
			"instructions": liveInstructions + " " + s.liveSituation(conversation) + " Recent conversation is in the session history.",
			"input":        history,
			"delegation":   map[string]string{"type": "client"},
		},
		"transport": map[string]string{"type": "webrtc", "sdp": offer.SDP},
	})
	if err != nil {
		s.internalError(w, "Encode Live session", err)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.liveSessionURL, bytes.NewReader(payload))
	if err != nil {
		s.internalError(w, "Create Live session request", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if s.liveSessionKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.liveSessionKey)
	}
	response, err := s.liveSessionClient.Do(req)
	if err != nil {
		s.logger.Error("Live session creation failed", "error", err)
		http.Error(w, "Live session provider unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		s.logger.Error("Live session provider rejected request", "status", response.StatusCode)
		http.Error(w, fmt.Sprintf("Live session provider rejected request (%d)", response.StatusCode), http.StatusBadGateway)
		return
	}
	var result struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Transport struct {
			Type string `json:"type"`
			SDP  string `json:"sdp"`
		} `json:"transport"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil ||
		result.Session.ID == "" || result.Transport.SDP == "" {
		http.Error(w, "Invalid Live session response", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(result); err != nil {
		s.logger.Error("Write Live session response", "error", err)
	}
}
