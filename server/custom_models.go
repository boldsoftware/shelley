package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/gem"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/models"
)

// ModelAPI is the API representation of a model
type ModelAPI struct {
	ModelID         string `json:"model_id"`
	DisplayName     string `json:"display_name"`
	ProviderType    string `json:"provider_type"`
	Endpoint        string `json:"endpoint"`
	HasAPIKey       bool   `json:"has_api_key"`
	ModelName       string `json:"model_name"`
	MaxTokens       int64  `json:"max_tokens"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// ReasoningReplay is "auto", "none", or "reasoning_content".
	ReasoningReplay         oai.ReasoningReplay `json:"reasoning_replay"`
	ResolvedReasoningReplay oai.ReasoningReplay `json:"resolved_reasoning_replay,omitempty"`
	// ImageSupport is one of "auto", "yes", or "no". "auto" is resolved
	// automatically from the model's endpoint and name.
	ImageSupport      string `json:"image_support"`
	ReasoningSupport  string `json:"reasoning_support"`
	ReasoningMap      string `json:"reasoning_map"`
	SupportsReasoning bool   `json:"supports_reasoning"`
	// SupportsImages is the resolved boolean that "image_support" evaluates
	// to for this model. It lets the UI show what "auto" resolves to.
	SupportsImages bool `json:"supports_images"`
}

// CreateModelRequest is the request body for creating a model.
type CreateModelRequest struct {
	DisplayName      string              `json:"display_name"`
	ProviderType     string              `json:"provider_type"`
	Endpoint         string              `json:"endpoint"`
	APIKey           string              `json:"api_key"`
	ModelName        string              `json:"model_name"`
	MaxTokens        int64               `json:"max_tokens"`
	ReasoningEffort  string              `json:"reasoning_effort,omitempty"`
	ReasoningReplay  oai.ReasoningReplay `json:"reasoning_replay,omitempty"`
	ImageSupport     string              `json:"image_support"`     // "auto"|"yes"|"no"; empty = "auto"
	ReasoningSupport string              `json:"reasoning_support"` // "auto"|"yes"|"no"; empty = "auto"
	ReasoningMap     string              `json:"reasoning_map"`     // JSON map of Shelley level to provider-supported level
}

// UpdateModelRequest is the request body for updating a model.
type UpdateModelRequest struct {
	DisplayName      string               `json:"display_name"`
	ProviderType     string               `json:"provider_type"`
	Endpoint         string               `json:"endpoint"`
	APIKey           string               `json:"api_key"` // Empty string means keep existing
	ModelName        string               `json:"model_name"`
	MaxTokens        *int64               `json:"max_tokens"`
	ReasoningEffort  *string              `json:"reasoning_effort,omitempty"`
	ReasoningReplay  *oai.ReasoningReplay `json:"reasoning_replay,omitempty"`
	ImageSupport     string               `json:"image_support"`     // "auto"|"yes"|"no"; empty preserves existing
	ReasoningSupport string               `json:"reasoning_support"` // "auto"|"yes"|"no"; empty preserves existing
	ReasoningMap     string               `json:"reasoning_map"`
}

// validImageSupport returns the canonical value or an error.
func validSupportSetting(field, v string) (string, error) {
	switch v {
	case "", "auto":
		return "auto", nil
	case "yes", "no":
		return v, nil
	default:
		return "", fmt.Errorf("%s must be one of 'auto', 'yes', 'no'; got %q", field, v)
	}
}

func validImageSupport(v string) (string, error) {
	return validSupportSetting("image_support", v)
}

func validReasoningSupport(v string) (string, error) {
	return validSupportSetting("reasoning_support", v)
}

func validReasoningReplay(v oai.ReasoningReplay) (oai.ReasoningReplay, error) {
	switch v {
	case "", oai.ReasoningReplayAuto:
		return oai.ReasoningReplayAuto, nil
	case oai.ReasoningReplayNone, oai.ReasoningReplayContent:
		return v, nil
	default:
		return "", fmt.Errorf("reasoning_replay must be one of 'auto', 'none', or 'reasoning_content'; got %q", v)
	}
}

func validReasoningMap(raw string) error {
	if raw == "" {
		return nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return fmt.Errorf("reasoning_map must be a JSON object: %w", err)
	}
	valid := map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	for from, to := range values {
		if !valid[from] || strings.TrimSpace(to) == "" {
			return fmt.Errorf("reasoning_map keys must use off, minimal, low, medium, high, xhigh, or max and values must be non-empty; got %q: %q", from, to)
		}
	}
	return nil
}

// customMaxTokens validates a requested output limit: negatives are rejected
// and zero is stored as-is, meaning the provider's own default applies.
func customMaxTokens(v int64) (int64, error) {
	if v < 0 {
		return 0, errors.New("max_tokens must not be negative")
	}
	return v, nil
}

type TestModelRequest struct {
	ModelID          string               `json:"model_id,omitempty"` // If provided, use stored API key
	ProviderType     string               `json:"provider_type"`
	Endpoint         string               `json:"endpoint"`
	APIKey           string               `json:"api_key"`
	ModelName        string               `json:"model_name"`
	MaxTokens        *int64               `json:"max_tokens"`
	ReasoningSupport string               `json:"reasoning_support"`
	ReasoningMap     string               `json:"reasoning_map"`
	ReasoningEffort  *string              `json:"reasoning_effort,omitempty"`
	ReasoningReplay  *oai.ReasoningReplay `json:"reasoning_replay,omitempty"`
}

func toModelAPI(m generated.Model) ModelAPI {
	reasoningReplay := oai.ReasoningReplay(m.ReasoningReplay)
	return ModelAPI{
		ModelID:                 m.ModelID,
		DisplayName:             m.DisplayName,
		ProviderType:            m.ProviderType,
		Endpoint:                m.Endpoint,
		HasAPIKey:               m.ApiKey != "",
		ModelName:               m.ModelName,
		MaxTokens:               m.MaxTokens,
		ReasoningEffort:         m.ReasoningEffort,
		ReasoningReplay:         reasoningReplay,
		ResolvedReasoningReplay: oai.ResolveReasoningReplay(m.Endpoint, m.ModelName, reasoningReplay),
		ImageSupport:            m.ImageSupport,
		ReasoningSupport:        m.ReasoningSupport,
		ReasoningMap:            m.ReasoningMap,
		SupportsReasoning:       models.ResolveSupportsReasoning(m.Endpoint, m.ModelName, m.ReasoningSupport),
		SupportsImages:          models.ResolveSupportsImages(m.Endpoint, m.ModelName, m.ImageSupport),
	}
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.db.GetModels(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get models: %v", err), http.StatusInternalServerError)
		return
	}

	apiModels := make([]ModelAPI, len(models))
	for i, model := range models {
		apiModels[i] = toModelAPI(model)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apiModels)
}

func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	var req CreateModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.DisplayName == "" || req.ProviderType == "" || req.Endpoint == "" || req.APIKey == "" || req.ModelName == "" {
		http.Error(w, "display_name, provider_type, endpoint, api_key, and model_name are required", http.StatusBadRequest)
		return
	}

	// Validate provider type
	if req.ProviderType != "anthropic" && req.ProviderType != "openai" && req.ProviderType != "openai-responses" && req.ProviderType != "gemini" {
		http.Error(w, "provider_type must be 'anthropic', 'openai', 'openai-responses', or 'gemini'", http.StatusBadRequest)
		return
	}

	// Generate a human-readable model ID derived from the endpoint and model name.
	modelID, err := s.generateUniqueModelID(r.Context(), req.Endpoint, req.ModelName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to generate model ID: %v", err), http.StatusInternalServerError)
		return
	}
	if req.MaxTokens, err = customMaxTokens(req.MaxTokens); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	imageSupport, err := validImageSupport(req.ImageSupport)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reasoningSupport, err := validReasoningSupport(req.ReasoningSupport)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validReasoningMap(req.ReasoningMap); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reasoningReplay, err := validReasoningReplay(req.ReasoningReplay)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	model, err := s.db.CreateModel(r.Context(), generated.CreateModelParams{
		ModelID:          modelID,
		DisplayName:      req.DisplayName,
		ProviderType:     req.ProviderType,
		Endpoint:         req.Endpoint,
		ApiKey:           req.APIKey,
		ModelName:        req.ModelName,
		MaxTokens:        req.MaxTokens,
		ReasoningEffort:  req.ReasoningEffort,
		ImageSupport:     imageSupport,
		ReasoningSupport: reasoningSupport,
		ReasoningMap:     req.ReasoningMap,
		ReasoningReplay:  string(reasoningReplay),
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create model: %v", err), http.StatusInternalServerError)
		return
	}

	// Refresh the model manager's cache
	if err := s.llmManager.RefreshCustomModels(); err != nil {
		s.logger.Warn("Failed to refresh custom models cache", "error", err)
	} else {
		s.notifyModelsChanged()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toModelAPI(*model))
}

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request, modelID string) {
	model, err := s.db.GetModel(r.Context(), modelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get model: %v", err), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toModelAPI(*model))
}

func (s *Server) handleUpdateModel(w http.ResponseWriter, r *http.Request, modelID string) {
	// First, get the existing model to get the current API key if not provided
	existing, err := s.db.GetModel(r.Context(), modelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Model not found: %v", err), http.StatusNotFound)
		return
	}

	var req UpdateModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Use existing API key if not provided
	apiKey := req.APIKey
	if apiKey == "" {
		apiKey = existing.ApiKey
	}

	// Empty support settings preserve the existing values; otherwise validate.
	imageSupport := existing.ImageSupport
	if req.ImageSupport != "" {
		v, err := validImageSupport(req.ImageSupport)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		imageSupport = v
	}
	reasoningSupport := existing.ReasoningSupport
	if req.ReasoningSupport != "" {
		v, err := validReasoningSupport(req.ReasoningSupport)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reasoningSupport = v
	}
	if err := validReasoningMap(req.ReasoningMap); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reasoningEffort := existing.ReasoningEffort
	if req.ReasoningEffort != nil {
		reasoningEffort = *req.ReasoningEffort
	}
	reasoningReplay := oai.ReasoningReplay(existing.ReasoningReplay)
	if req.ReasoningReplay != nil {
		reasoningReplay, err = validReasoningReplay(*req.ReasoningReplay)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	maxTokens := existing.MaxTokens
	if req.MaxTokens != nil {
		if maxTokens, err = customMaxTokens(*req.MaxTokens); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	model, err := s.db.UpdateModel(r.Context(), generated.UpdateModelParams{
		DisplayName:      req.DisplayName,
		ProviderType:     req.ProviderType,
		Endpoint:         req.Endpoint,
		ApiKey:           apiKey,
		ModelName:        req.ModelName,
		MaxTokens:        maxTokens,
		ReasoningEffort:  reasoningEffort,
		ImageSupport:     imageSupport,
		ReasoningSupport: reasoningSupport,
		ReasoningMap:     req.ReasoningMap,
		ReasoningReplay:  string(reasoningReplay),
		ModelID:          modelID,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update model: %v", err), http.StatusInternalServerError)
		return
	}

	// Refresh the model manager's cache
	if err := s.llmManager.RefreshCustomModels(); err != nil {
		s.logger.Warn("Failed to refresh custom models cache", "error", err)
	} else {
		s.notifyModelsChanged()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toModelAPI(*model))
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request, modelID string) {
	err := s.db.DeleteModel(r.Context(), modelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete model: %v", err), http.StatusInternalServerError)
		return
	}

	// Refresh the model manager's cache
	if err := s.llmManager.RefreshCustomModels(); err != nil {
		s.logger.Warn("Failed to refresh custom models cache", "error", err)
	} else {
		s.notifyModelsChanged()
	}

	w.WriteHeader(http.StatusNoContent)
}

// DuplicateModelRequest allows overriding fields when duplicating
type DuplicateModelRequest struct {
	DisplayName string `json:"display_name,omitempty"`
}

func (s *Server) handleDuplicateModel(w http.ResponseWriter, r *http.Request, modelID string) {
	// Get the source model (including API key)
	source, err := s.db.GetModel(r.Context(), modelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Source model not found: %v", err), http.StatusNotFound)
		return
	}

	// Parse optional overrides
	var req DuplicateModelRequest
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req) // Ignore errors - all fields optional
	}

	// Generate a new human-readable model ID. Since the duplicate shares the
	// source's endpoint and model name, this naturally gets a numeric suffix.
	newModelID, err := s.generateUniqueModelID(r.Context(), source.Endpoint, source.ModelName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to generate model ID: %v", err), http.StatusInternalServerError)
		return
	}

	// Use provided display name or generate one
	displayName := req.DisplayName
	if displayName == "" {
		displayName = source.DisplayName + " (copy)"
	}

	model, err := s.db.CreateModel(r.Context(), generated.CreateModelParams{
		ModelID:          newModelID,
		DisplayName:      displayName,
		ProviderType:     source.ProviderType,
		Endpoint:         source.Endpoint,
		ApiKey:           source.ApiKey, // Copy the API key!
		ModelName:        source.ModelName,
		MaxTokens:        source.MaxTokens,
		ReasoningEffort:  source.ReasoningEffort,
		ImageSupport:     source.ImageSupport,
		ReasoningSupport: source.ReasoningSupport,
		ReasoningMap:     source.ReasoningMap,
		ReasoningReplay:  source.ReasoningReplay,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to duplicate model: %v", err), http.StatusInternalServerError)
		return
	}

	// Refresh the model manager's cache
	if err := s.llmManager.RefreshCustomModels(); err != nil {
		s.logger.Warn("Failed to refresh custom models cache", "error", err)
	} else {
		s.notifyModelsChanged()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toModelAPI(*model))
}

func (s *Server) handleTestModel(w http.ResponseWriter, r *http.Request) {
	var req TestModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// A model ID supplies hidden legacy configuration and, when omitted by the
	// caller, its stored API key. The UI intentionally does not expose the
	// legacy reasoning_effort field, but Test must still mirror runtime.
	if req.ModelID != "" {
		model, err := s.db.GetModel(r.Context(), req.ModelID)
		if err != nil {
			http.Error(w, fmt.Sprintf("Model not found: %v", err), http.StatusNotFound)
			return
		}
		if req.APIKey == "" {
			req.APIKey = model.ApiKey
		}
		if req.ReasoningEffort == nil {
			req.ReasoningEffort = &model.ReasoningEffort
		}
		if req.MaxTokens == nil {
			req.MaxTokens = &model.MaxTokens
		}
		if req.ReasoningReplay == nil {
			reasoningReplay := oai.ReasoningReplay(model.ReasoningReplay)
			req.ReasoningReplay = &reasoningReplay
		}
	}

	if req.ProviderType == "" || req.Endpoint == "" || req.APIKey == "" || req.ModelName == "" {
		http.Error(w, "provider_type, endpoint, api_key, and model_name are required", http.StatusBadRequest)
		return
	}
	requestedMaxTokens := int64(0)
	if req.MaxTokens != nil {
		requestedMaxTokens = *req.MaxTokens
	}
	maxTokens, err := customMaxTokens(requestedMaxTokens)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	reasoningEffort := ""
	if req.ReasoningEffort != nil {
		reasoningEffort = *req.ReasoningEffort
	}
	reasoningReplay := oai.ReasoningReplayAuto
	if req.ReasoningReplay != nil {
		reasoningReplay, err = validReasoningReplay(*req.ReasoningReplay)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	// Create the appropriate service based on provider type
	var service llm.Service
	switch req.ProviderType {
	case "anthropic":
		service = &ant.Service{
			APIKey:        req.APIKey,
			URL:           req.Endpoint,
			Model:         req.ModelName,
			MaxTokens:     int(maxTokens),
			ThinkingLevel: llm.ThinkingLevelMedium,
		}
	case "openai":
		service = &oai.Service{
			APIKey:          req.APIKey,
			ModelURL:        req.Endpoint,
			MaxTokens:       int(maxTokens),
			ReasoningEffort: reasoningEffort,
			ReasoningReplay: reasoningReplay,
			Model: oai.Model{
				UserName:         "",
				ModelName:        req.ModelName,
				TextVerbosity:    "",
				URL:              req.Endpoint,
				APIKeyEnv:        "",
				IsReasoningModel: false,
				SupportsImages:   true,
			},
		}
	case "gemini":
		service = &gem.Service{
			APIKey:          req.APIKey,
			URL:             req.Endpoint,
			Model:           req.ModelName,
			MaxTokens:       int(maxTokens),
			ReasoningEffort: reasoningEffort,
		}
	case "openai-responses":
		service = &oai.ResponsesService{
			APIKey:    req.APIKey,
			MaxTokens: int(maxTokens),
			Model: oai.Model{
				UserName:         "",
				ModelName:        req.ModelName,
				TextVerbosity:    "",
				URL:              req.Endpoint,
				APIKeyEnv:        "",
				IsReasoningModel: false,
				SupportsImages:   true,
			},
			// Match createServiceFromModel so Test reflects real runtime behavior:
			// medium is the default when no explicit override is given.
			ThinkingLevel:   llm.ThinkingLevelMedium,
			ReasoningEffort: reasoningEffort,
			ReasoningReplay: reasoningReplay,
		}
	default:
		http.Error(w, "Invalid provider_type", http.StatusBadRequest)
		return
	}
	if _, err := validReasoningSupport(req.ReasoningSupport); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validReasoningMap(req.ReasoningMap); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	service = models.WrapReasoningConfig(service, req.Endpoint, req.ModelName, req.ReasoningSupport, req.ReasoningMap)

	// Send a simple test request
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	request := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Say 'test successful' in exactly two words."},
				},
			},
		},
	}

	response, err := service.Do(ctx, request)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("Test failed: %v", err),
		})
		return
	}

	// Check if we got a response with actual text content
	// (skip thinking blocks which may appear first)
	var responseText string
	for _, content := range response.Content {
		if content.Type == llm.ContentTypeText && content.Text != "" {
			responseText = content.Text
			break
		}
	}

	if responseText == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Test failed: empty response from model",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Test successful! Response: %s", responseText),
	})
}
