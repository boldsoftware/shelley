package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
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

// liveVoicePolicy follows OpenAI's GPT-Live prompt template: Live owns the
// spoken conversation and decides when to delegate; the client delegation
// handler (the backend) does the work. How Live sounds comes from the
// profile's voice prompt, if any (see liveInstructions).
const liveVoicePolicy = `Speak naturally, following any voice guidance for tone, pace and style. If the user is frustrated, acknowledge it briefly and focus on the next helpful step.

Follow the user's current language, tone or voice-style request for this call over any profile speaking defaults, without changing your identity or these policies. This is temporary speaking guidance, not backend work; delegate only if they explicitly ask to change their profile or do other work.

Backchannel policy: Use moderate backchannels. Acknowledge naturally without competing with the main response.

Interruption policy: Stop speaking when the user interrupts. Listen to what they say. When the user asks you to stop talking, stop; that alone is not a request for the backend. When they ask you to repeat yourself, repeat or rephrase what you said without delegating.

Delegation policy:
Backend: The backend agent for this conversation handles delegated work using its available tools.

You and the backend are one assistant to the user. They do not need to decide which part handles a request. Speak in your normal persona without front-end/backend jargon.

Answer yourself first. The session history is the typed conversation before this call: the user's messages and the backend's replies. It, the typed messages and replies reported to you and still-current backend results are yours to answer from: what was said, decided, done or reported, and how something described there works. Delegate only when the answer needs work, tools or facts you do not have.

"What's happening", "where are we", "any news", "what did you do" and the like are status questions, not small talk: answer with where things stand from what you have, the backend's latest reply and whether it is still working. If nothing is new, say so and still say where things stand.

Delegate to the backend when:
- The request needs work, tools or facts beyond this conversation.
- The user stops, cancels or corrects work already handed off; delegate the change itself, such as "stop the tests" or "use the other branch instead".
- The user answers a question from the backend, or states a preference or decision about its work, such as "yes, the second one" or "keep the message button". Only the backend can act on it: never say you will keep, change or do something yourself.

For a request that needs backend work, create an actual client delegation in this turn, not a promise to do it later. Delegations run in the background and never block you: delegate each new request, correction, addition or answer as soon as the user says it, even while earlier work is still running and before it reports back. Never hold something back until earlier work finishes, and never tell the user you are waiting on something instead of delegating what they just said. The backend receives delegations in order. Do not ask for permission again when the user has already asked you to investigate, check, change, run or fix something.

Do not delegate when:
- The user is greeting you or making small talk.
- You need a brief clarification to understand the request.
- You can answer from what you already have, as above.
- The user only asks you to stop talking or to repeat yourself.

Create the client delegation before making any handoff or progress claim. "I'll check" by itself does not send a task. Until the client confirms acceptance or queuing, do not say the request was sent, that you are checking it or that work has started.`

const (
	liveAcceptedPolicy     = " Once accepted, acknowledge briefly and keep listening; if queued, say it is waiting behind earlier work."
	liveQuietHandoffPolicy = " Delegate silently and keep listening. An accepted or queued handoff needs no spoken confirmation. Speak when there is a concrete development, a result, or a question that needs the caller."
	liveFailurePolicy      = ` If delivery fails, say it was not sent and ask the user to repeat it. Do not invent or guess results, progress or completion; report only what the backend returns.`
)

// liveGreeting makes Live speak first, leading with where the conversation
// stands. GPT-Live has no "respond now" event: a developer message closing
// session.input is how it is asked to open the call, and that only
// improves the odds that it does.
const liveGreeting = "The call just connected. Greet the user now with a brief hello in the language of the dialogue above, or else English. If there is dialogue above, add one sentence on where it stands: what the backend last reported and whether it is still working. Then pause and listen."

const liveProgressGreeting = "The call just connected. Say a brief hello in the dialogue’s language, or English, then listen. Do not recap history, announce a handoff or describe the backend. If the user is already speaking, answer them instead."

func livePaceInstruction(speakingPace int) string {
	return map[int]string{
		-2: "Speak very slowly and clearly, with generous pauses between thoughts.",
		-1: "Speak slowly and clearly, with comfortable pauses.",
		1:  "Speak briskly, with short pauses; stay clear.",
		2:  "Speak at a very fast but intelligible pace, with only essential pauses; keep every word clear.",
	}[speakingPace]
}

// liveInstructions are Live's instructions: who to speak as, from the
// profile's voice prompt if it has one, then the voice and delegation
// policies, then the state at the start of the call.
func liveInstructions(voicePrompt, situation string, speakingPace int, progressEnabled bool) string {
	var b strings.Builder
	b.WriteString("You are the live voice of the assistant in this conversation.")
	if voicePrompt != "" {
		b.WriteString(" Take your identity, role, language, tone and style from the profile voice guidance below, and never mention where it came from.")
	}
	b.WriteString(" Recent session history and the current status provide conversation context.")
	if voicePrompt != "" {
		b.WriteString(" Where the guidance conflicts with the policies after it, the policies win.\n\n<voice_guidance>\n" + voicePrompt + "\n</voice_guidance>")
	}
	if speakingPace != 0 {
		b.WriteString("\n\n" + livePaceInstruction(speakingPace) + " This overrides pace in the voice guidance, but follow the user's current voice-style requests for this call.")
	}
	handoffPolicy := liveAcceptedPolicy
	if progressEnabled {
		handoffPolicy = liveQuietHandoffPolicy
	}
	b.WriteString("\n\n" + liveVoicePolicy + handoffPolicy + liveFailurePolicy)
	if progressEnabled {
		b.WriteString("\n\n" + liveProgressPolicy)
	}
	b.WriteString("\n\nAs of the start of this call: " + situation +
		" This may have changed since. The session history holds part of the recent conversation, not all of it.")
	return b.String()
}

// liveProfile is the profile a call speaks as, read at call time so that
// edits apply to the next call: a conversation's own, or for a draft the
// client's selection or else the default. A deleted profile is none.
func (s *Server) liveProfile(ctx context.Context, conversation *generated.Conversation, selected string) (Profile, error) {
	name := db.ParseConversationOptions(conversation.ConversationOptions).Profile
	if conversation.IsDraft {
		if selected != "" {
			name = selected
		}
		if name == "" {
			return s.getDefaultProfile(ctx)
		}
	}
	if name == "" {
		return Profile{}, nil
	}
	p, err := s.getProfile(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, nil
	}
	return p, err
}

func (s *Server) handleLiveSession(w http.ResponseWriter, r *http.Request, conversationID string) {
	conversation, err := s.db.GetConversationByID(r.Context(), conversationID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	// Model, cwd and profile are a draft's current selection; a conversation
	// under way has its own.
	// The call's delegations deliver them with the draft's first message.
	var offer struct {
		SDP string `json:"sdp"`
		liveSettings
	}
	var options struct {
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLiveOfferBytes)).Decode(&offer); err != nil || !strings.HasPrefix(offer.SDP, "v=0") ||
		(offer.ConversationOptions != nil && json.Unmarshal(offer.ConversationOptions, &options) != nil) {
		http.Error(w, "Invalid SDP offer", http.StatusBadRequest)
		return
	}
	profile, err := s.liveProfile(r.Context(), conversation, options.Profile)
	if err != nil {
		s.internalError(w, "Load voice prompt", err)
		return
	}
	preferences, err := s.getLivePreferences(r.Context())
	if err != nil {
		s.internalError(w, "Load Live settings", err)
		return
	}
	history, lastSeq, err := s.liveContext(r.Context(), conversationID, conversation.AgentWorking && preferences.ProgressUpdates)
	if err != nil {
		s.internalError(w, "Load Live context", err)
		return
	}
	if conversation.IsDraft {
		conversation.Model = &offer.Model
		conversation.Cwd = &offer.Cwd
		if offer.Model == "" {
			conversation.Model = &profile.Model
		}
	}
	greeting := liveGreeting
	if preferences.ProgressUpdates {
		greeting = liveProgressGreeting
	}
	session := map[string]any{
		"model":        "gpt-live-1",
		"instructions": liveInstructions(profile.VoicePrompt, s.liveSituation(conversation), preferences.SpeakingPace, preferences.ProgressUpdates),
		"input":        append(history, liveHistoryItem{Type: "message", Role: "developer", Content: []liveHistoryText{{Type: "input_text", Text: greeting}}}),
		"delegation":   map[string]string{"type": "client"},
	}
	if preferences.Voice != defaultLiveVoice {
		session["audio"] = map[string]any{"output": map[string]string{"voice": preferences.Voice}}
	}
	payload, err := json.Marshal(map[string]any{
		"session":   session,
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
	// Only the session id and SDP answer reach the browser.
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

	if err := s.startLiveCall(r, conversationID, result.Session.ID, lastSeq, offer.liveSettings, preferences.ProgressUpdates); err != nil {
		s.internalError(w, "Failed to start the call", err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
