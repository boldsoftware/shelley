package server

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// Profile is a named Settings. New conversations start from the profile they
// name, or the default one; existing ones can switch profiles (see
// SettingsChange).
type Profile struct {
	Name string `json:"name"`
	// Default marks the profile new conversations use when they name none.
	// Exactly one profile is the default.
	Default    bool `json:"default"`
	Builtin    bool `json:"builtin"`
	UserEdited bool `json:"user_edited"`
	// Project marks a project's profile (see db.Project). Conversations in
	// it are in the project; they don't switch profiles, but move.
	Project bool `json:"project"`
	Settings
	// VoicePrompt is how the profile's voice calls speak; "" means no
	// persona. It belongs to the profile, not its Settings: conversations
	// don't copy it, and a call reads the current one.
	VoicePrompt string `json:"voice_prompt"`
}

// storedProfile is a profile's settings column: its Settings and voice prompt.
type storedProfile struct {
	Settings
	VoicePrompt string `json:"voice_prompt,omitempty"`
}

const maxVoicePromptRunes = 4000

// voicePromptProblem says what's wrong with a trimmed voice prompt, or "".
func voicePromptProblem(prompt string) string {
	if utf8.RuneCountInString(prompt) > maxVoicePromptRunes {
		return fmt.Sprintf("voice prompt is longer than %d characters", maxVoicePromptRunes)
	}
	return ""
}

func (s *Server) registerProfileRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/profiles", s.handleListProfiles)
	api.HandleFunc("POST /api/profiles", s.handleCreateProfile)
	api.HandleFunc("PUT /api/profiles/{name}", s.handleUpdateProfile)
	api.HandleFunc("POST /api/profiles/voice-prompt/generate", s.handleGenerateVoicePrompt)
	api.HandleFunc("POST /api/profiles/{name}/default", s.handleSetDefaultProfile)
	api.HandleFunc("POST /api/profiles/{name}/restore", s.handleRestoreProfile)
	api.HandleFunc("DELETE /api/profiles/{name}", s.handleDeleteProfile)
	api.HandleFunc("GET /api/system-prompt", s.handleGetSystemPrompt)
	api.HandleFunc("POST /api/system-prompt/check", s.handleCheckSystemPrompt)
}

func profileFromRow(row generated.Profile) (Profile, error) {
	p := Profile{Name: row.Name, Default: row.IsDefault, Builtin: row.Builtin, UserEdited: row.UserEdited}
	var stored storedProfile
	if err := json.Unmarshal([]byte(row.Settings), &stored); err != nil {
		return p, fmt.Errorf("profile %q: %w", row.Name, err)
	}
	stored.ToolOverrides = orEmpty(stored.ToolOverrides)
	p.Settings = stored.Settings
	p.VoicePrompt = stored.VoicePrompt
	return p, nil
}

func (s *Server) getProfile(ctx context.Context, name string) (Profile, error) {
	var row generated.Profile
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		row, err = q.GetProfile(ctx, name)
		return err
	})
	if err != nil {
		return Profile{}, err
	}
	return profileFromRow(row)
}

func (s *Server) getDefaultProfile(ctx context.Context) (Profile, error) {
	var row generated.Profile
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		row, err = q.GetDefaultProfile(ctx)
		return err
	})
	if err != nil {
		return Profile{}, err
	}
	return profileFromRow(row)
}

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	var rows []generated.Profile
	err := s.db.Queries(r.Context(), func(q *generated.Queries) (err error) {
		rows, err = q.ListProfiles(r.Context())
		return err
	})
	if err != nil {
		s.internalError(w, "Failed to list profiles", err)
		return
	}
	projects, err := s.db.ListProjects(r.Context())
	if err != nil {
		s.internalError(w, "Failed to list profiles", err)
		return
	}
	out := make([]Profile, 0, len(rows))
	for _, row := range rows {
		p, err := profileFromRow(row)
		if err != nil {
			s.internalError(w, "Failed to list profiles", err)
			return
		}
		p.Project = slices.ContainsFunc(projects, func(project db.Project) bool { return project.Name == p.Name })
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// decodeStrict reads r's JSON body into v, rejecting unknown fields. It
// writes an error and returns false if it can't.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func encodeProfile(st Settings, voicePrompt string) string {
	b, err := json.Marshal(storedProfile{Settings: st, VoicePrompt: voicePrompt})
	if err != nil {
		panic(err) // Settings always marshals.
	}
	return string(b)
}

// handleCreateProfile handles POST /api/profiles: {name, ...settings,
// voice_prompt}.
func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Settings
		VoicePrompt string `json:"voice_prompt"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if msg := db.ProfileNameProblem(body.Name); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	body.VoicePrompt = strings.TrimSpace(body.VoicePrompt)
	if msg := voicePromptProblem(body.VoicePrompt); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if msg := body.Settings.validate(s.getModelList(), true); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	var row generated.Profile
	err := s.db.QueriesTx(r.Context(), func(q *generated.Queries) (err error) {
		row, err = q.CreateProfile(r.Context(), generated.CreateProfileParams{Name: body.Name, Settings: encodeProfile(body.Settings, body.VoicePrompt)})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("profile %q already exists", body.Name), http.StatusConflict)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to create profile", err)
		return
	}
	created, err := profileFromRow(row)
	if err != nil {
		s.internalError(w, "Failed to create profile", err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// handleUpdateProfile handles PUT /api/profiles/{name}: the body is the
// profile's new settings, and optionally its voice prompt, which an absent
// voice_prompt keeps. Its model needn't be available if it is unchanged.
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Settings
		VoicePrompt *string `json:"voice_prompt"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	st := body.Settings
	name := r.PathValue("name")
	existing, err := s.getProfile(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("profile %q not found", name), http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to load profile", err)
		return
	}
	voicePrompt := existing.VoicePrompt
	if body.VoicePrompt != nil {
		voicePrompt = strings.TrimSpace(*body.VoicePrompt)
	}
	if msg := cmp.Or(voicePromptProblem(voicePrompt), st.validate(s.getModelList(), st.Model != existing.Model)); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if existing.Settings.equal(st) && existing.VoicePrompt == voicePrompt {
		writeJSON(w, http.StatusOK, existing)
		return
	}
	var row generated.Profile
	err = s.db.QueriesTx(r.Context(), func(q *generated.Queries) (err error) {
		row, err = q.UpdateProfile(r.Context(), generated.UpdateProfileParams{Name: name, Settings: encodeProfile(st, voicePrompt)})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("profile %q not found", name), http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to update profile", err)
		return
	}
	updated, err := profileFromRow(row)
	if err != nil {
		s.internalError(w, "Failed to update profile", err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleRestoreProfile replaces a built-in's settings with its shipped
// defaults without changing its default selection or existing conversations.
func (s *Server) handleRestoreProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	existing, err := s.getProfile(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("profile %q not found", name), http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to load profile", err)
		return
	}
	if !existing.Builtin {
		http.Error(w, "custom profiles have no defaults to restore", http.StatusBadRequest)
		return
	}
	row, err := s.db.RestoreBuiltinProfile(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("profile %q not found", name), http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to restore profile", err)
		return
	}
	restored, err := profileFromRow(*row)
	if err != nil {
		s.internalError(w, "Failed to restore profile", err)
		return
	}
	writeJSON(w, http.StatusOK, restored)
}

// handleSetDefaultProfile handles POST /api/profiles/{name}/default: the
// profile becomes the default, in place of the previous one. A project's
// profile can't: a conversation that names no profile is in no project.
func (s *Server) handleSetDefaultProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.db.GetProject(r.Context(), name); err == nil {
		http.Error(w, fmt.Sprintf("profile %q is a project's and can't be the default", name), http.StatusConflict)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.internalError(w, "Failed to load project", err)
		return
	}
	err := s.db.QueriesTx(r.Context(), func(q *generated.Queries) error {
		if err := q.ClearDefaultProfile(r.Context()); err != nil {
			return err
		}
		n, err := q.SetDefaultProfile(r.Context(), name)
		if err == nil && n == 0 {
			err = sql.ErrNoRows
		}
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("profile %q not found", name), http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to set the default profile", err)
		return
	}
	p, err := s.getProfile(r.Context(), name)
	if err != nil {
		s.internalError(w, "Failed to load profile", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handleDeleteProfile handles DELETE /api/profiles/{name}. The default
// profile can't be deleted, nor a project's, which deleting the project
// does. Conversations keep their settings, and the name as a label.
func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	_, err := s.db.GetProject(r.Context(), name)
	if err == nil {
		http.Error(w, fmt.Sprintf("profile %q is a project; delete the project instead", name), http.StatusConflict)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.internalError(w, "Failed to delete profile", err)
		return
	}
	var n int64
	err = s.db.QueriesTx(r.Context(), func(q *generated.Queries) (err error) {
		n, err = q.DeleteProfile(r.Context(), name)
		return err
	})
	if err != nil {
		s.internalError(w, "Failed to delete profile", err)
		return
	}
	if n == 0 {
		if _, err := s.getProfile(r.Context(), name); err == nil {
			http.Error(w, "the default profile can't be deleted", http.StatusConflict)
		} else {
			http.Error(w, fmt.Sprintf("profile %q not found", name), http.StatusNotFound)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetSystemPrompt handles GET /api/system-prompt: the built-in system
// prompt template, and the variables a template can use, for people writing
// their own.
func (s *Server) handleGetSystemPrompt(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"template":  systemPromptTemplate,
		"variables": systemPromptVariables,
	})
}

// handleCheckSystemPrompt handles POST /api/system-prompt/check: whether a
// template can be rendered, checked as a profile save checks it, so editors
// can say what's wrong as it's typed. {"error": null}, or the problem.
func (s *Server) handleCheckSystemPrompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Template string `json:"template"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]*TemplateProblem{"error": validateSystemPromptTemplate(body.Template)})
}
