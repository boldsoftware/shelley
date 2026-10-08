package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"shelley.exe.dev/db/generated"
)

// Profile is a named Settings. New conversations start from the profile they
// name, or the default one; existing ones can switch profiles (see
// SettingsChange).
type Profile struct {
	Name string `json:"name"`
	// Default marks the profile new conversations use when they name none.
	// Exactly one profile is the default.
	Default bool `json:"default"`
	Settings
}

func (s *Server) registerProfileRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/profiles", s.handleListProfiles)
	api.HandleFunc("POST /api/profiles", s.handleCreateProfile)
	api.HandleFunc("PUT /api/profiles/{name}", s.handleUpdateProfile)
	api.HandleFunc("POST /api/profiles/{name}/default", s.handleSetDefaultProfile)
	api.HandleFunc("DELETE /api/profiles/{name}", s.handleDeleteProfile)
	api.HandleFunc("GET /api/system-prompt", s.handleGetSystemPrompt)
	api.HandleFunc("POST /api/system-prompt/check", s.handleCheckSystemPrompt)
}

func profileFromRow(row generated.Profile) (Profile, error) {
	p := Profile{Name: row.Name, Default: row.IsDefault}
	if err := json.Unmarshal([]byte(row.Settings), &p.Settings); err != nil {
		return p, fmt.Errorf("profile %q: %w", row.Name, err)
	}
	p.ToolOverrides = orEmpty(p.ToolOverrides)
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
	out := make([]Profile, 0, len(rows))
	for _, row := range rows {
		p, err := profileFromRow(row)
		if err != nil {
			s.internalError(w, "Failed to list profiles", err)
			return
		}
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

// profileNameProblem says what's wrong with a profile name, or "". Names are
// path segments in the API.
func profileNameProblem(name string) string {
	switch {
	case name == "":
		return "name is required"
	case len(name) > 64:
		return "name is longer than 64 bytes"
	case strings.Contains(name, "/") || strings.ContainsFunc(name, unicode.IsControl):
		return "name can't contain / or control characters"
	case name == "." || name == "..":
		return "name can't be . or .." // they don't survive in a URL path
	}
	return ""
}

func encodeSettings(st Settings) string {
	b, err := json.Marshal(st)
	if err != nil {
		panic(err) // Settings always marshals.
	}
	return string(b)
}

// handleCreateProfile handles POST /api/profiles: {name, ...settings}.
func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Settings
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if msg := profileNameProblem(body.Name); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if msg := body.Settings.validate(s.getModelList(), true); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	var row generated.Profile
	err := s.db.QueriesTx(r.Context(), func(q *generated.Queries) (err error) {
		row, err = q.CreateProfile(r.Context(), generated.CreateProfileParams{Name: body.Name, Settings: encodeSettings(body.Settings)})
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
// profile's new settings. Its model needn't be available if it is unchanged.
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	var st Settings
	if !decodeStrict(w, r, &st) {
		return
	}
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
	if msg := st.validate(s.getModelList(), st.Model != existing.Model); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	var row generated.Profile
	err = s.db.QueriesTx(r.Context(), func(q *generated.Queries) (err error) {
		row, err = q.UpdateProfile(r.Context(), generated.UpdateProfileParams{Name: name, Settings: encodeSettings(st)})
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

// handleSetDefaultProfile handles POST /api/profiles/{name}/default: the
// profile becomes the default, in place of the previous one.
func (s *Server) handleSetDefaultProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
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
// profile can't be deleted. Conversations keep their settings, and the name
// as a label.
func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var n int64
	err := s.db.QueriesTx(r.Context(), func(q *generated.Queries) (err error) {
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
