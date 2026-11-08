package modelproviders

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/models"
)

const (
	discoveryTimeout    = 30 * time.Second
	discoveryBodyLimit  = 4 << 20
	discoveryPageLimit  = 50
	discoveryModelLimit = 10000
)

func discoveryFailure(message string) error {
	return fmt.Errorf("%w: %s", ErrDiscovery, message)
}

func (m *Manager) discover(ctx context.Context, p db.ModelProvider) ([]db.ProviderModel, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	client := *m.httpc
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	base, _ := url.Parse(p.Endpoint) // candidate canonicalized this before discovery
	cursorParam := "after"
	if p.ProviderType == "anthropic" {
		cursorParam = "after_id"
	} else if p.ProviderType == "gemini" {
		cursorParam = "pageToken"
	}
	result := []db.ProviderModel{}
	seenIDs := make(map[string]bool)
	seenCursors := make(map[string]bool)
	cursor := ""
	bodyLeft := int64(discoveryBodyLimit)
	for page := 0; page < discoveryPageLimit; page++ {
		target := *base
		target.Path = strings.TrimRight(base.Path, "/") + "/models"
		target.RawPath = ""
		if cursor != "" {
			query := target.Query()
			query.Set(cursorParam, cursor)
			target.RawQuery = query.Encode()
		}
		items, hasMore, lastID, err := fetchModelPage(ctx, &client, p.ProviderType, target.String(), p.APIKey, &bodyLeft)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if seenIDs[item.ID] {
				continue
			}
			seenIDs[item.ID] = true
			if len(seenIDs) > discoveryModelLimit {
				return nil, discoveryFailure("model limit exceeded")
			}
			if models.ProviderModelEligible(p.Endpoint, item.ID) {
				result = append(result, item)
			}
		}
		if !hasMore {
			return result, nil
		}
		if lastID == "" || seenCursors[lastID] {
			return nil, discoveryFailure("invalid pagination cursor")
		}
		seenCursors[lastID] = true
		cursor = lastID
	}
	return nil, discoveryFailure("page limit exceeded")
}

func fetchModelPage(ctx context.Context, client *http.Client, providerType, target, key string, bodyLeft *int64) ([]db.ProviderModel, bool, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, false, "", discoveryFailure("request could not be created")
	}
	if providerType == "anthropic" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if providerType == "gemini" {
		req.Header.Set("x-goog-api-key", key)
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, "", discoveryFailure("discovery canceled or timed out")
		}
		return nil, false, "", discoveryFailure("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, "", discoveryFailure(fmt.Sprintf("provider returned HTTP %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, *bodyLeft+1))
	if err != nil {
		return nil, false, "", discoveryFailure("response could not be read")
	}
	if int64(len(body)) > *bodyLeft {
		return nil, false, "", discoveryFailure("response body limit exceeded")
	}
	*bodyLeft -= int64(len(body))

	if providerType == "gemini" {
		return parseGeminiModelPage(body)
	}

	var (
		data    json.RawMessage
		hasMore bool
		lastID  string
	)
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		if providerType == "anthropic" {
			return nil, false, "", discoveryFailure("provider response data must be an array")
		}
		data = trimmed
	} else {
		var envelope struct {
			Data    json.RawMessage `json:"data"`
			HasMore bool            `json:"has_more"`
			LastID  string          `json:"last_id"`
		}
		if json.Unmarshal(body, &envelope) != nil {
			return nil, false, "", discoveryFailure("malformed provider response")
		}
		data, hasMore, lastID = bytes.TrimSpace(envelope.Data), envelope.HasMore, envelope.LastID
	}
	if len(data) == 0 || data[0] != '[' {
		return nil, false, "", discoveryFailure("provider response data must be an array")
	}
	var entries []json.RawMessage
	if json.Unmarshal(data, &entries) != nil || entries == nil {
		return nil, false, "", discoveryFailure("provider response data must be an array")
	}
	items := make([]db.ProviderModel, 0, len(entries))
	for _, entry := range entries {
		var model struct {
			ID          string          `json:"id"`
			Name        json.RawMessage `json:"name"`
			DisplayName json.RawMessage `json:"display_name"`
		}
		if len(bytes.TrimSpace(entry)) == 0 || bytes.TrimSpace(entry)[0] != '{' || json.Unmarshal(entry, &model) != nil || strings.TrimSpace(model.ID) == "" {
			return nil, false, "", discoveryFailure("provider response contains an invalid model")
		}
		rawName := model.Name
		if providerType == "anthropic" {
			rawName = model.DisplayName
		}
		var name string
		_ = json.Unmarshal(rawName, &name)
		items = append(items, db.ProviderModel{ID: model.ID, Name: name})
	}
	return items, hasMore, lastID, nil
}

func parseGeminiModelPage(body []byte) ([]db.ProviderModel, bool, string, error) {
	var page struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
		NextPageToken string `json:"nextPageToken"`
	}
	if json.Unmarshal(body, &page) != nil || page.Models == nil {
		return nil, false, "", discoveryFailure("malformed Gemini model response")
	}
	items := make([]db.ProviderModel, 0, len(page.Models))
	for _, model := range page.Models {
		id, ok := strings.CutPrefix(model.Name, "models/")
		if !ok || strings.TrimSpace(id) == "" {
			return nil, false, "", discoveryFailure("provider response contains an invalid model")
		}
		if slices.Contains(model.SupportedGenerationMethods, "generateContent") {
			items = append(items, db.ProviderModel{ID: id, Name: model.DisplayName})
		}
	}
	return items, page.NextPageToken != "", page.NextPageToken, nil
}
