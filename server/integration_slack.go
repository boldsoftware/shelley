package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/exeenv"
)

func (s *Server) handleTestSlack(w http.ResponseWriter, r *http.Request) {
	if !isExeDev() || testing.Testing() {
		http.Error(w, "Slack integration is unavailable", http.StatusServiceUnavailable)
		return
	}
	env, err := exeenv.Current()
	if err != nil {
		http.Error(w, "Cannot determine exe.dev environment", http.StatusInternalServerError)
		return
	}
	s.handleTestSlackIn(w, r, env, reflectionHTTPClient())
}

func (s *Server) handleTestSlackIn(w http.ResponseWriter, r *http.Request, env exeenv.Environment, client *http.Client) {
	if s.predictableOnly {
		http.Error(w, "Slack integration is unavailable", http.StatusServiceUnavailable)
		return
	}
	var input struct {
		Name    string `json:"name"`
		Team    bool   `json:"team"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		http.Error(w, "Invalid Slack test message", http.StatusBadRequest)
		return
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Name == "" || input.Message == "" || utf8.RuneCountInString(input.Message) > 200 {
		http.Error(w, "Select a Slack hook and enter a message of 1–200 characters", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	attached, _, err := listAttachedIntegrations(ctx, client, env)
	if err != nil {
		http.Error(w, "Cannot verify attached Slack hooks", http.StatusBadGateway)
		return
	}
	if !slices.ContainsFunc(attached, func(ig reflectionIntegration) bool {
		return ig.Type == "slack" && ig.Name == input.Name && ig.Team == input.Team
	}) {
		http.Error(w, "Slack hook is no longer attached to this VM", http.StatusConflict)
		return
	}
	body, err := json.Marshal(map[string]string{"text": input.Message})
	if err != nil {
		http.Error(w, "Cannot prepare Slack message", http.StatusInternalServerError)
		return
	}
	endpoint := env.IntegrationURL(input.Name, input.Team)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "Cannot prepare Slack request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Slack request failed. Check Slack before retrying.", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	result, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK || err != nil || strings.TrimSpace(string(result)) != "ok" {
		if err == nil && slackBotConfirmed(ctx, client, endpoint) {
			http.Error(w, "This is a Slack bot. This test only supports incoming webhooks.", http.StatusUnprocessableEntity)
			return
		}
		http.Error(w, fmt.Sprintf("Slack did not confirm delivery (HTTP %d). Check Slack before retrying.", resp.StatusCode), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Diagnose a failed webhook test without sending another message.
func slackBotConfirmed(ctx context.Context, client *http.Client, endpoint string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/auth.test", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var identity struct {
		OK    bool   `json:"ok"`
		BotID string `json:"bot_id"`
	}
	return resp.StatusCode == http.StatusOK &&
		json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&identity) == nil &&
		identity.OK && identity.BotID != ""
}
