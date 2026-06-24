package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const remoteModelsTimeout = 15 * time.Second

// DiscoverModelsRequest is the request body for listing models from a remote API.
type DiscoverModelsRequest struct {
	ModelID      string `json:"model_id,omitempty"` // If provided with empty api_key, use stored key
	ProviderType string `json:"provider_type"`
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"api_key"`
}

// RemoteModelOption is one model returned by a remote provider's models endpoint.
type RemoteModelOption struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

type anthropicModelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

type geminiModelsResponse struct {
	Models []struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	} `json:"models"`
}

func (s *Server) handleDiscoverModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req DiscoverModelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.ModelID != "" && req.APIKey == "" {
		model, err := s.db.GetModel(r.Context(), req.ModelID)
		if err != nil {
			http.Error(w, fmt.Sprintf("Model not found: %v", err), http.StatusNotFound)
			return
		}
		req.APIKey = model.ApiKey
	}

	if req.ProviderType == "" || req.Endpoint == "" || req.APIKey == "" {
		http.Error(w, "provider_type, endpoint, and api_key are required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), remoteModelsTimeout)
	defer cancel()

	models, err := discoverRemoteModels(ctx, http.DefaultClient, req.ProviderType, req.Endpoint, req.APIKey)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": err.Error(),
			"models":  []RemoteModelOption{},
		})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"models":  models,
	})
}

func discoverRemoteModels(ctx context.Context, httpc *http.Client, providerType, endpoint, apiKey string) ([]RemoteModelOption, error) {
	switch providerType {
	case "openai", "openai-responses":
		return discoverOpenAIModels(ctx, httpc, endpoint, apiKey)
	case "anthropic":
		return discoverAnthropicModels(ctx, httpc, endpoint, apiKey)
	case "gemini":
		return discoverGeminiModels(ctx, httpc, endpoint, apiKey)
	default:
		return nil, fmt.Errorf("unsupported provider_type %q", providerType)
	}
}

func discoverOpenAIModels(ctx context.Context, httpc *http.Client, endpoint, apiKey string) ([]RemoteModelOption, error) {
	modelsURL, err := modelsListURL(endpoint)
	if err != nil {
		return nil, err
	}
	body, status, err := getRemoteJSON(ctx, httpc, modelsURL, openAIAuthHeaders(apiKey))
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("models endpoint returned HTTP %d", status)
	}
	var resp openAIModelsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}
	return remoteModelsFromIDs(resp.Data, func(item struct {
		ID string `json:"id"`
	}) (string, string) {
		return item.ID, item.ID
	}), nil
}

func discoverAnthropicModels(ctx context.Context, httpc *http.Client, endpoint, apiKey string) ([]RemoteModelOption, error) {
	modelsURL, err := modelsListURL(endpoint)
	if err != nil {
		return nil, err
	}
	body, status, err := getRemoteJSON(ctx, httpc, modelsURL, anthropicAuthHeaders(apiKey))
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("models endpoint returned HTTP %d", status)
	}
	var resp anthropicModelsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}
	return remoteModelsFromIDs(resp.Data, func(item struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}) (string, string) {
		displayName := item.DisplayName
		if displayName == "" {
			displayName = item.ID
		}
		return item.ID, displayName
	}), nil
}

func discoverGeminiModels(ctx context.Context, httpc *http.Client, endpoint, apiKey string) ([]RemoteModelOption, error) {
	modelsURL, err := geminiModelsListURL(endpoint, apiKey)
	if err != nil {
		return nil, err
	}
	body, status, err := getRemoteJSON(ctx, httpc, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("models endpoint returned HTTP %d", status)
	}
	var resp geminiModelsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}
	var out []RemoteModelOption
	for _, model := range resp.Models {
		id := strings.TrimPrefix(model.Name, "models/")
		if id == "" {
			continue
		}
		displayName := model.DisplayName
		if displayName == "" {
			displayName = id
		}
		out = append(out, RemoteModelOption{ID: id, DisplayName: displayName})
	}
	return out, nil
}

func remoteModelsFromIDs[T any](items []T, pick func(T) (id, displayName string)) []RemoteModelOption {
	out := make([]RemoteModelOption, 0, len(items))
	for _, item := range items {
		id, displayName := pick(item)
		if id == "" {
			continue
		}
		out = append(out, RemoteModelOption{ID: id, DisplayName: displayName})
	}
	return out
}

func modelsListURL(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", fmt.Errorf("endpoint is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("endpoint must be an absolute URL")
	}

	path := strings.TrimSuffix(parsed.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
			break
		}
	}
	switch {
	case strings.HasSuffix(path, "/models"):
		// already a models endpoint
	case strings.HasSuffix(path, "/v1beta"):
		path += "/models"
	case strings.HasSuffix(path, "/v1"):
		path += "/models"
	case idx := strings.LastIndex(path, "/v1"); idx >= 0:
		path = path[:idx+3] + "/models"
	default:
		path += "/models"
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func geminiModelsListURL(endpoint, apiKey string) (string, error) {
	u, err := modelsListURL(endpoint)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	q := parsed.Query()
	q.Set("key", apiKey)
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

func openAIAuthHeaders(apiKey string) http.Header {
	h := make(http.Header)
	h.Set("Authorization", "Bearer "+apiKey)
	return h
}

func anthropicAuthHeaders(apiKey string) http.Header {
	h := make(http.Header)
	h.Set("x-api-key", apiKey)
	h.Set("anthropic-version", "2023-06-01")
	return h
}

func getRemoteJSON(ctx context.Context, httpc *http.Client, rawURL string, headers http.Header) ([]byte, int, error) {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, vals := range headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to reach models endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read models response: %w", err)
	}
	return body, resp.StatusCode, nil
}