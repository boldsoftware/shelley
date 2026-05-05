package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/gem"
	"shelley.exe.dev/llm/oai"
)

// ModelAPI is the API representation of a model
type ModelAPI struct {
	ModelID         string `json:"model_id"`
	DisplayName     string `json:"display_name"`
	ProviderType    string `json:"provider_type"`
	Endpoint        string `json:"endpoint"`
	APIKey          string `json:"api_key"`
	ModelName       string `json:"model_name"`
	MaxTokens       int64  `json:"max_tokens"`
	Tags            string `json:"tags"`             // Comma-separated tags (e.g., "slug" for slug generation)
	ReasoningEffort string `json:"reasoning_effort"` // Free-form reasoning.effort for OpenAI Responses API; empty = default
}

// CreateModelRequest is the request body for creating a model
type CreateModelRequest struct {
	DisplayName     string `json:"display_name"`
	ProviderType    string `json:"provider_type"`
	Endpoint        string `json:"endpoint"`
	APIKey          string `json:"api_key"`
	ModelName       string `json:"model_name"`
	MaxTokens       int64  `json:"max_tokens"`
	Tags            string `json:"tags"`             // Comma-separated tags
	ReasoningEffort string `json:"reasoning_effort"` // Free-form reasoning.effort for OpenAI Responses API
}

// UpdateModelRequest is the request body for updating a model
type UpdateModelRequest struct {
	DisplayName     string `json:"display_name"`
	ProviderType    string `json:"provider_type"`
	Endpoint        string `json:"endpoint"`
	APIKey          string `json:"api_key"` // Empty string means keep existing
	ModelName       string `json:"model_name"`
	MaxTokens       int64  `json:"max_tokens"`
	Tags            string `json:"tags"`             // Comma-separated tags
	ReasoningEffort string `json:"reasoning_effort"` // Free-form reasoning.effort for OpenAI Responses API
}

// TestModelRequest is the request body for testing a model
type TestModelRequest struct {
	ModelID         string `json:"model_id,omitempty"` // If provided, use stored API key
	ProviderType    string `json:"provider_type"`
	Endpoint        string `json:"endpoint"`
	APIKey          string `json:"api_key"`
	ModelName       string `json:"model_name"`
	ReasoningEffort string `json:"reasoning_effort"`
}

func toModelAPI(m generated.Model) ModelAPI {
	return ModelAPI{
		ModelID:         m.ModelID,
		DisplayName:     m.DisplayName,
		ProviderType:    m.ProviderType,
		Endpoint:        m.Endpoint,
		APIKey:          m.ApiKey,
		ModelName:       m.ModelName,
		MaxTokens:       m.MaxTokens,
		Tags:            m.Tags,
		ReasoningEffort: m.ReasoningEffort,
	}
}

func (s *Server) handleCustomModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListModels(w, r)
	case http.MethodPost:
		s.handleCreateModel(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.db.GetModels(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get models: %v", err), http.StatusInternalServerError)
		return
	}

	apiModels := make([]ModelAPI, len(models))
	for i, m := range models {
		apiModels[i] = toModelAPI(m)
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

	// Generate model ID
	modelID := "custom-" + uuid.New().String()[:8]

	// Default max tokens
	if req.MaxTokens <= 0 {
		req.MaxTokens = 200000
	}

	model, err := s.db.CreateModel(r.Context(), generated.CreateModelParams{
		ModelID:         modelID,
		DisplayName:     req.DisplayName,
		ProviderType:    req.ProviderType,
		Endpoint:        req.Endpoint,
		ApiKey:          req.APIKey,
		ModelName:       req.ModelName,
		MaxTokens:       req.MaxTokens,
		Tags:            req.Tags,
		ReasoningEffort: req.ReasoningEffort,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create model: %v", err), http.StatusInternalServerError)
		return
	}

	// Refresh the model manager's cache
	if err := s.llmManager.RefreshCustomModels(); err != nil {
		s.logger.Warn("Failed to refresh custom models cache", "error", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toModelAPI(*model))
}

func (s *Server) handleCustomModel(w http.ResponseWriter, r *http.Request) {
	// Extract model ID from URL path: /api/custom-models/{id} or /api/custom-models/{id}/duplicate
	path := strings.TrimPrefix(r.URL.Path, "/api/custom-models/")
	if path == "" {
		http.Error(w, "Invalid model ID", http.StatusBadRequest)
		return
	}

	// Check for /duplicate suffix
	if strings.HasSuffix(path, "/duplicate") {
		modelID := strings.TrimSuffix(path, "/duplicate")
		if r.Method == http.MethodPost {
			s.handleDuplicateModel(w, r, modelID)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	if strings.Contains(path, "/") {
		http.Error(w, "Invalid model ID", http.StatusBadRequest)
		return
	}
	modelID := path

	switch r.Method {
	case http.MethodGet:
		s.handleGetModel(w, r, modelID)
	case http.MethodPut:
		s.handleUpdateModel(w, r, modelID)
	case http.MethodDelete:
		s.handleDeleteModel(w, r, modelID)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
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

	// Default max tokens
	if req.MaxTokens <= 0 {
		req.MaxTokens = 200000
	}

	model, err := s.db.UpdateModel(r.Context(), generated.UpdateModelParams{
		DisplayName:     req.DisplayName,
		ProviderType:    req.ProviderType,
		Endpoint:        req.Endpoint,
		ApiKey:          apiKey,
		ModelName:       req.ModelName,
		MaxTokens:       req.MaxTokens,
		Tags:            req.Tags,
		ReasoningEffort: req.ReasoningEffort,
		ModelID:         modelID,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update model: %v", err), http.StatusInternalServerError)
		return
	}

	// Refresh the model manager's cache
	if err := s.llmManager.RefreshCustomModels(); err != nil {
		s.logger.Warn("Failed to refresh custom models cache", "error", err)
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

	// Generate new model ID
	newModelID := "custom-" + uuid.New().String()[:8]

	// Use provided display name or generate one
	displayName := req.DisplayName
	if displayName == "" {
		displayName = source.DisplayName + " (copy)"
	}

	// Create the duplicate with the same API key
	model, err := s.db.CreateModel(r.Context(), generated.CreateModelParams{
		ModelID:         newModelID,
		DisplayName:     displayName,
		ProviderType:    source.ProviderType,
		Endpoint:        source.Endpoint,
		ApiKey:          source.ApiKey, // Copy the API key!
		ModelName:       source.ModelName,
		MaxTokens:       source.MaxTokens,
		Tags:            "", // Don't copy tags
		ReasoningEffort: source.ReasoningEffort,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to duplicate model: %v", err), http.StatusInternalServerError)
		return
	}

	// Refresh the model manager's cache
	if err := s.llmManager.RefreshCustomModels(); err != nil {
		s.logger.Warn("Failed to refresh custom models cache", "error", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toModelAPI(*model))
}

func (s *Server) handleTestModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TestModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// If model_id is provided and api_key is empty, look up the stored key
	if req.ModelID != "" && req.APIKey == "" {
		model, err := s.db.GetModel(r.Context(), req.ModelID)
		if err != nil {
			http.Error(w, fmt.Sprintf("Model not found: %v", err), http.StatusNotFound)
			return
		}
		req.APIKey = model.ApiKey
	}

	if req.ProviderType == "" || req.Endpoint == "" || req.APIKey == "" || req.ModelName == "" {
		http.Error(w, "provider_type, endpoint, api_key, and model_name are required", http.StatusBadRequest)
		return
	}

	// Create the appropriate service based on provider type
	var service llm.Service
	switch req.ProviderType {
	case "anthropic":
		service = &ant.Service{
			APIKey:        req.APIKey,
			URL:           req.Endpoint,
			Model:         req.ModelName,
			ThinkingLevel: llm.ThinkingLevelMedium,
		}
	case "openai":
		service = &oai.Service{
			APIKey:   req.APIKey,
			ModelURL: req.Endpoint,
			Model: oai.Model{
				ModelName: req.ModelName,
				URL:       req.Endpoint,
			},
		}
	case "gemini":
		service = &gem.Service{
			APIKey:        req.APIKey,
			URL:           req.Endpoint,
			Model:         req.ModelName,
			ThinkingLevel: llm.ThinkingLevelDynamic,
		}
	case "openai-responses":
		service = &oai.ResponsesService{
			APIKey: req.APIKey,
			Model: oai.Model{
				ModelName: req.ModelName,
				URL:       req.Endpoint,
			},
			// Match createServiceFromModel so Test reflects real runtime behavior:
			// medium is the default when no explicit override is given.
			ThinkingLevel:   llm.ThinkingLevelMedium,
			ReasoningEffort: req.ReasoningEffort,
		}
	default:
		http.Error(w, "Invalid provider_type", http.StatusBadRequest)
		return
	}

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
