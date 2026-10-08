package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// Settings are the user-adjustable configuration of a conversation: its
// model, reasoning, tools, and system prompt. A profile is a named Settings. A
// conversation keeps its own copy, in its model column and options (see
// conversationSettings), so editing a profile never changes a conversation.
type Settings struct {
	// Model is a model id; "" in a profile means the server's default model.
	Model string `json:"model"`
	// ThinkingLevel is a reasoning level; "" means the model's default.
	ThinkingLevel string `json:"thinking_level"`
	// ToolOverrides maps tool names to "on" or "off". Unlisted tools use
	// their defaults.
	ToolOverrides map[string]string `json:"tool_overrides"`
	// CompactNudgeTokens is where the agent is first told its context size
	// while compact_in_place is on; 0 means the default.
	CompactNudgeTokens int `json:"compact_nudge_tokens"`
	// SystemPrompt is the text/template of the system prompt; "" means the
	// built-in one.
	SystemPrompt string `json:"system_prompt"`
}

// ConversationSettings are a conversation's settings and the profile they
// came from. The profile is a label: the settings may since have been changed,
// or the profile edited.
type ConversationSettings struct {
	Profile string `json:"profile"`
	Settings
}

func conversationSettings(conv generated.Conversation) ConversationSettings {
	opts := db.ParseConversationOptions(conv.ConversationOptions)
	return ConversationSettings{
		Profile: opts.Profile,
		Settings: Settings{
			Model:              derefString(conv.Model),
			ThinkingLevel:      opts.ThinkingLevel,
			ToolOverrides:      orEmpty(opts.ToolOverrides),
			CompactNudgeTokens: opts.CompactNudgeTokens,
			SystemPrompt:       opts.SystemPrompt,
		},
	}
}

// orEmpty returns m, or an empty map if it is nil, so that it encodes as {}.
func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (st Settings) equal(o Settings) bool {
	return st.Model == o.Model && st.ThinkingLevel == o.ThinkingLevel &&
		maps.Equal(st.ToolOverrides, o.ToolOverrides) &&
		st.CompactNudgeTokens == o.CompactNudgeTokens && st.SystemPrompt == o.SystemPrompt
}

// applyTo stores cs, except the model, in opts.
func (cs ConversationSettings) applyTo(opts *db.ConversationOptions) {
	opts.Profile = cs.Profile
	opts.ThinkingLevel = cs.ThinkingLevel
	opts.ToolOverrides = cs.ToolOverrides
	opts.CompactNudgeTokens = cs.CompactNudgeTokens
	opts.SystemPrompt = cs.SystemPrompt
}

// validate reports what's wrong with st, or "". modelList is the catalog. An
// empty st.Model passes, and so does an unavailable one unless requireReady.
func (st Settings) validate(modelList []ModelInfo, requireReady bool) string {
	if st.Model != "" && requireReady && !isReadyModel(st.Model, modelList) {
		return unsupportedModelMessage(st.Model, modelList)
	}
	if msg := validateConversationOptions(db.ConversationOptions{
		ThinkingLevel:      st.ThinkingLevel,
		ToolOverrides:      st.ToolOverrides,
		CompactNudgeTokens: st.CompactNudgeTokens,
		SystemPrompt:       st.SystemPrompt,
	}); msg != "" {
		return msg
	}
	return validateModelReasoningLevel(findModelInfo(st.Model, modelList), st.ThinkingLevel)
}

// SettingsChange changes a conversation's settings: the body of POST
// /api/conversation/<id>/settings, and the settings in a new conversation's
// conversation_options. Absent fields keep their values. Profile, if present,
// first replaces all settings with the named profile's; the other fields then
// override those.
type SettingsChange struct {
	Profile            *string           `json:"profile"`
	Model              *string           `json:"model"`
	ThinkingLevel      *string           `json:"thinking_level"`
	ToolOverrides      map[string]string `json:"tool_overrides"` // nil keeps; {} clears
	CompactNudgeTokens *int              `json:"compact_nudge_tokens"`
	SystemPrompt       *string           `json:"system_prompt"`
}

// overlay returns c with the fields present in o replacing its own.
func (c SettingsChange) overlay(o SettingsChange) SettingsChange {
	if o.Profile != nil {
		c.Profile = o.Profile
	}
	if o.Model != nil {
		c.Model = o.Model
	}
	if o.ThinkingLevel != nil {
		c.ThinkingLevel = o.ThinkingLevel
	}
	if o.ToolOverrides != nil {
		c.ToolOverrides = o.ToolOverrides
	}
	if o.CompactNudgeTokens != nil {
		c.CompactNudgeTokens = o.CompactNudgeTokens
	}
	if o.SystemPrompt != nil {
		c.SystemPrompt = o.SystemPrompt
	}
	return c
}

// storedSettingsChange is the change a draft's stored options make: those
// set to other than their zero values.
func storedSettingsChange(model string, opts db.ConversationOptions) SettingsChange {
	var c SettingsChange
	if opts.Profile != "" {
		c.Profile = &opts.Profile
	}
	if model != "" {
		c.Model = &model
	}
	if opts.ThinkingLevel != "" {
		c.ThinkingLevel = &opts.ThinkingLevel
	}
	c.ToolOverrides = opts.ToolOverrides
	if opts.CompactNudgeTokens != 0 {
		c.CompactNudgeTokens = &opts.CompactNudgeTokens
	}
	if opts.SystemPrompt != "" {
		c.SystemPrompt = &opts.SystemPrompt
	}
	return c
}

// ChatOptions is conversation_options in a chat request. Its settings count
// where present (see SettingsChange), so that "" can override a profile's
// value; the model is the request's, not the options'.
type ChatOptions struct {
	db.ConversationOptions
	Change SettingsChange `json:"-"`
}

func (o *ChatOptions) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &o.ConversationOptions); err != nil {
		return err
	}
	if err := json.Unmarshal(b, &o.Change); err != nil {
		return err
	}
	o.Change.Model = nil
	return nil
}

// invalidSettings is an error the client can fix; its text says how.
type invalidSettings string

func (e invalidSettings) Error() string { return string(e) }

var errSettingsUnavailable = errors.New("this conversation's settings can't be changed")

// resolveSettings returns cur changed by change (see SettingsChange), checked.
// A model of "" means the server's default. A reasoning level carried over to
// a model that lacks it rounds to one the model has. Problems the client can
// fix are invalidSettings errors.
func (s *Server) resolveSettings(ctx context.Context, cur ConversationSettings, change SettingsChange, modelList []ModelInfo) (ConversationSettings, error) {
	next := cur
	if change.Profile != nil {
		p, err := s.getProfile(ctx, *change.Profile)
		if errors.Is(err, sql.ErrNoRows) {
			return cur, invalidSettings(fmt.Sprintf("profile %q not found", *change.Profile))
		}
		if err != nil {
			return cur, err
		}
		next = ConversationSettings{Profile: p.Name, Settings: p.Settings}
	}
	if change.Model != nil {
		next.Model = *change.Model
	}
	if next.Model == "" {
		next.Model = s.effectiveDefaultModel(modelList)
	}
	if change.ThinkingLevel != nil {
		next.ThinkingLevel = *change.ThinkingLevel
	} else {
		next.ThinkingLevel, _ = roundModelReasoningLevel(findModelInfo(next.Model, modelList), next.ThinkingLevel)
	}
	if change.ToolOverrides != nil {
		next.ToolOverrides = change.ToolOverrides
	}
	next.ToolOverrides = orEmpty(next.ToolOverrides)
	if change.CompactNudgeTokens != nil {
		next.CompactNudgeTokens = *change.CompactNudgeTokens
	}
	if change.SystemPrompt != nil {
		next.SystemPrompt = *change.SystemPrompt
	}
	// Switching a conversation's model needs a ready one, as /model does; a
	// new conversation's needs only to be constructible.
	msg := next.validate(modelList, cur.Model != "" && next.Model != cur.Model)
	if msg == "" {
		if _, err := s.llmManager.GetService(next.Model); err != nil {
			msg = unsupportedModelMessage(next.Model, modelList)
		}
	}
	if msg != "" {
		if change.Profile != nil && change.Model == nil {
			msg = fmt.Sprintf("profile %q: %s", next.Profile, msg)
		}
		return cur, invalidSettings(msg)
	}
	return next, nil
}

// newConversationSettings resolves the settings a new conversation starts
// with: change applied to the default profile, unless it names another.
func (s *Server) newConversationSettings(ctx context.Context, change SettingsChange, modelList []ModelInfo) (ConversationSettings, error) {
	if change.Profile == nil {
		p, err := s.getDefaultProfile(ctx)
		if err != nil {
			return ConversationSettings{}, err
		}
		change.Profile = &p.Name
	}
	return s.resolveSettings(ctx, ConversationSettings{}, change, modelList)
}

// changeSettings applies change to the conversation (see ApplySettings) and
// returns the settings now in effect.
func (s *Server) changeSettings(ctx context.Context, manager *ConversationManager, change SettingsChange) (ConversationSettings, error) {
	modelList := s.getModelList()
	settings, err := manager.ApplySettings(ctx, modelList, func(cur ConversationSettings) (ConversationSettings, error) {
		return s.resolveSettings(ctx, cur, change, modelList)
	})
	// Messages queued behind an interrupted turn go out now.
	manager.drainQueueIfIdle(s)
	return settings, err
}

// handleGetConversationSettings handles GET /api/conversation/<id>/settings.
func (s *Server) handleGetConversationSettings(w http.ResponseWriter, r *http.Request, conversationID string) {
	conv, err := s.db.GetConversationByID(r.Context(), conversationID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, conversationSettings(*conv))
}

// handleConversationSettings handles POST /api/conversation/<id>/settings.
// It responds with the settings now in effect. A running turn is stopped
// first, as with /model, unless only the profile label changes.
func (s *Server) handleConversationSettings(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()
	var change SettingsChange
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&change); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	switch {
	case conv.Archived:
		http.Error(w, "conversation is archived", http.StatusConflict)
		return
	case conv.IsDraft:
		// A draft's settings travel with its first send.
		http.Error(w, "conversation is still a draft", http.StatusConflict)
		return
	}
	userEmail := r.Header.Get("X-ExeDev-Email")
	ctx = contextWithUserEmail(ctx, userEmail)
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, userEmail)
	if err != nil {
		s.internalError(w, "Failed to get conversation manager", err, "conversationID", conversationID)
		return
	}
	settings, err := s.changeSettings(ctx, manager, change)
	var invalid invalidSettings
	switch {
	case errors.As(err, &invalid):
		http.Error(w, invalid.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, errSettingsUnavailable), errors.Is(err, errAgentWorking):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		s.internalError(w, "Failed to apply settings", err, "conversationID", conversationID)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// ApplySettings changes the conversation's settings for subsequent turns:
// change gets the current ones and returns new ones, or an error to abort. It
// returns the settings in effect. Unless only the profile label changes, it
// stops a running turn first, keeping queued messages for the caller to
// drain, and drops the loop so the next turn is built from the new settings. It renders a new system prompt if the
// template or the tools changed (the latest system prompt is the one in
// effect; see compactedContext), and records a marker that says what changed.
// models name the models and their default reasoning in the marker.
func (cm *ConversationManager) ApplySettings(ctx context.Context, models []ModelInfo, change func(ConversationSettings) (ConversationSettings, error)) (ConversationSettings, error) {
	cm.modelSettingsMu.Lock()
	defer cm.modelSettingsMu.Unlock()

	conv, err := cm.db.GetConversationByID(ctx, cm.conversationID)
	if err != nil {
		return ConversationSettings{}, err
	}
	opts := db.ParseConversationOptions(conv.ConversationOptions)
	from := conversationSettings(*conv)
	to, err := change(from)
	if err != nil {
		return from, err
	}
	to.ToolOverrides = orEmpty(to.ToolOverrides)
	if to.Profile == from.Profile && to.equal(from.Settings) {
		return from, nil
	}
	switch {
	case to.SystemPrompt != from.SystemPrompt && cm.role != roleTopLevel:
		return from, fmt.Errorf("%w: only top-level conversations have custom system prompts", errSettingsUnavailable)
	case cm.role == roleBtwReader && (to.Profile != from.Profile || !maps.Equal(to.ToolOverrides, from.ToolOverrides) ||
		to.CompactNudgeTokens != from.CompactNudgeTokens):
		// Its loop has a fixed toolset and prompt; see ensureLoop.
		return from, fmt.Errorf("%w: a /btw conversation can only change its model and reasoning", errSettingsUnavailable)
	}
	// From here on, a cancelled request mustn't leave the change half made.
	ctx = context.WithoutCancel(ctx)
	marker, regenerate, reload := settingsMarker(opts, from, to, models)
	if marker != nil {
		marker.Previous = &from
	}
	// The manual Compact in Place button turns the tool on without nudges.
	// Configuring tools or the nudge yourself brings them back.
	keepNudgesOff := opts.DisableCompactNudges &&
		maps.Equal(to.ToolOverrides, from.ToolOverrides) && to.CompactNudgeTokens == from.CompactNudgeTokens
	reload = reload || opts.DisableCompactNudges != keepNudgesOff
	save := func() error {
		saved, _, err := cm.db.ModifyConversationOptions(ctx, cm.conversationID, func(o *db.ConversationOptions) bool {
			to.applyTo(o)
			o.DisableCompactNudges = keepNudgesOff
			return true
		})
		if err != nil {
			return err
		}
		cm.mu.Lock()
		cm.conversationOptions = saved
		cm.mu.Unlock()
		return nil
	}

	if !reload {
		// Only the label or the spelling changed; the loop has nothing to
		// pick up.
		if err := save(); err != nil {
			return from, err
		}
		return to, cm.recordSettingsChange(ctx, nil, marker)
	}

	// Render first, so that a template that fails here changes nothing.
	var prompt renderedSystemPrompt
	if regenerate {
		next := opts
		to.applyTo(&next)
		if prompt, err = cm.renderSystemPrompt(ctx, next); err != nil {
			return from, invalidSettings("Invalid system_prompt: " + err.Error())
		}
	}

	// Stop a running turn the way the cancel button does, so it records its
	// end and clears agent_working, rather than with a bare loop reset. Unlike
	// the button, keep the queue: it goes out on the new settings, once the
	// caller drains it.
	cm.mu.Lock()
	cm.changingSettings = true
	cm.mu.Unlock()
	defer func() {
		cm.mu.Lock()
		cm.changingSettings = false
		cm.mu.Unlock()
	}()
	if cm.IsAgentWorking() {
		if err := cm.cancelConversation(ctx, false, ""); err != nil {
			return from, fmt.Errorf("failed to cancel active turn before settings change: %w", err)
		}
	}

	// Under the loop lifecycle lock, so no turn starts on the old settings,
	// or before the marker.
	var recordErr error
	err = cm.resetLoopAfter(true, func() error {
		if cm.IsAgentWorking() {
			// A turn started after the cancel; don't kill it unannounced.
			return errAgentWorking
		}
		if err := save(); err != nil {
			return err
		}
		// The options are saved; from here on the loop must be reset
		// whatever fails.
		if recordErr = cm.db.ForceUpdateConversationModel(ctx, cm.conversationID, to.Model); recordErr != nil {
			return nil
		}
		var stored *generated.Message
		if regenerate {
			if stored, recordErr = cm.storeSystemPrompt(ctx, prompt); recordErr != nil {
				return nil
			}
		}
		recordErr = cm.recordSettingsChange(ctx, stored, marker)
		return nil
	})
	if err != nil {
		return from, err
	}
	return to, recordErr
}

// recordSettingsChange publishes a settings change: the new system prompt,
// if any, then the marker, if any; or else the conversation, whose options
// changed.
func (cm *ConversationManager) recordSettingsChange(ctx context.Context, prompt *generated.Message, marker *ModelChangeUserData) error {
	// Publish in sequence order: the prompt was stored before the marker.
	if prompt != nil {
		if err := cm.publishCreated(ctx, prompt); err != nil {
			return err
		}
	}
	if marker != nil {
		return cm.recordModelChangeMarker(ctx, *marker)
	}
	conv, err := cm.db.GetConversationByID(ctx, cm.conversationID)
	if err != nil {
		return err
	}
	cm.broadcastStream(StreamResponse{Conversation: conv})
	return nil
}

// settingsMarker describes the change from from to to, or returns nil if
// nothing the agent or user sees changes. reload reports whether the agent
// sees a change, so the loop has to be rebuilt: anything but the profile
// label changed. regenerate reports whether the system prompt has to be
// rendered again: its template or the tools listed with it changed. opts are
// the conversation's options before the change.
func settingsMarker(opts db.ConversationOptions, from, to ConversationSettings, models []ModelInfo) (marker *ModelChangeUserData, regenerate, reload bool) {
	m := ModelChangeUserData{}
	var parts []string
	if from.Profile != to.Profile {
		m.ProfileTo = to.Profile
		parts = append(parts, fmt.Sprintf("profile changed to %s", to.Profile))
	}
	labelParts := len(parts)
	if from.Model != to.Model {
		m.From, m.To = from.Model, to.Model
		m.FromDisplay, m.ToDisplay = modelDisplayName(from.Model, models), modelDisplayName(to.Model, models)
		parts = append(parts, fmt.Sprintf("model changed from %s to %s", m.FromDisplay, m.ToDisplay))
	}
	// The levels in effect: an unset one is its model's default, which only
	// some models name.
	reasoning := func(st ConversationSettings) string {
		if st.ThinkingLevel != "" {
			return st.ThinkingLevel
		}
		if info := findModelInfo(st.Model, models); info != nil && info.DefaultReasoningLevel != "" {
			return info.DefaultReasoningLevel
		}
		return "model's default"
	}
	if was, is := reasoning(from), reasoning(to); was != is {
		m.ReasoningFrom, m.ReasoningTo = was, is
		parts = append(parts, fmt.Sprintf("reasoning changed from %s to %s", m.ReasoningFrom, m.ReasoningTo))
	}
	for _, t := range claudetool.ToolRegistry {
		was := claudetool.IsToolEnabled(t.Name, from.ToolOverrides, opts.DisableAllTools)
		is := claudetool.IsToolEnabled(t.Name, to.ToolOverrides, opts.DisableAllTools)
		switch {
		case is && !was:
			m.ToolsOn = append(m.ToolsOn, t.Name)
		case was && !is:
			m.ToolsOff = append(m.ToolsOff, t.Name)
		}
	}
	if len(m.ToolsOn) > 0 {
		parts = append(parts, "tools turned on: "+strings.Join(m.ToolsOn, ", "))
	}
	if len(m.ToolsOff) > 0 {
		parts = append(parts, "tools turned off: "+strings.Join(m.ToolsOff, ", "))
	}
	compacting := claudetool.IsToolEnabled(claudetool.CompactInPlaceName, to.ToolOverrides, opts.DisableAllTools)
	if compacting && compactNudgeTokens(from.CompactNudgeTokens) != compactNudgeTokens(to.CompactNudgeTokens) {
		m.CompactNudgeTokens = compactNudgeTokens(to.CompactNudgeTokens)
		parts = append(parts, fmt.Sprintf("compaction nudge set to %dk tokens", m.CompactNudgeTokens/1000))
	}
	if from.SystemPrompt != to.SystemPrompt {
		m.SystemPromptChanged = true
		parts = append(parts, "system prompt changed")
	}
	if len(parts) == 0 {
		// Only the spelling changed, e.g. an explicit "off" for a tool that is
		// off by default.
		return nil, false, false
	}
	text := strings.Join(parts, "; ")
	m.Text = strings.ToUpper(text[:1]) + text[1:] + "."
	return &m, m.SystemPromptChanged || len(m.ToolsOn)+len(m.ToolsOff) > 0, len(parts) > labelParts
}

func compactNudgeTokens(n int) int {
	if n == 0 {
		return defaultCompactNudgeTokens
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
