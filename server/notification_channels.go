package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/server/notifications"
)

// exeNotifySettingKey is the settings key controlling whether end-of-turn
// pushes to the exe.dev notify gateway are auto-configured. Enabled by default
// when the VM's "notify" integration is available; setting this to "false"
// turns it off.
const exeNotifySettingKey = "exe_notify"

// exeNotifyGatewayURL is the exe.dev push-notification gateway, reachable from
// inside the VM via the built-in "notify" integration. It is the same URL the
// iOS app registers as an end-of-turn hook, so auto-configuring it here
// collapses with the iOS registration into a single hook (one push).
const exeNotifyGatewayURL = "https://notify.int.exe.xyz/"

func (s *Server) handleTestExeNotify(w http.ResponseWriter, r *http.Request) {
	if !isExeDev() || testing.Testing() {
		http.Error(w, "Notify integration is unavailable", http.StatusServiceUnavailable)
		return
	}
	env, err := exeenv.Current()
	if err != nil {
		http.Error(w, "Cannot determine exe.dev environment", http.StatusInternalServerError)
		return
	}
	s.handleTestExeNotifyIn(w, r, env, reflectionHTTPClient())
}

func (s *Server) handleTestExeNotifyIn(w http.ResponseWriter, r *http.Request, env exeenv.Environment, client *http.Client) {
	if s.predictableOnly {
		http.Error(w, "Notify integration is unavailable", http.StatusServiceUnavailable)
		return
	}
	availabilityCtx, availabilityCancel := context.WithTimeout(r.Context(), 2*time.Second)
	available, err := hasNotifyIntegration(availabilityCtx, client, env)
	availabilityCancel()
	if err != nil || !available {
		http.Error(w, "Notify integration is unavailable", http.StatusServiceUnavailable)
		return
	}
	var input struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil {
		http.Error(w, "Invalid test message", http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(input.Message)
	if message == "" || utf8.RuneCountInString(message) > 200 {
		http.Error(w, "Test message must be 1–200 characters", http.StatusBadRequest)
		return
	}
	body, err := json.Marshal(map[string]string{"title": "Shelley test", "body": message})
	if err != nil {
		http.Error(w, "Cannot prepare notification", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.IntegrationURL("notify", false)+"/", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "Cannot prepare notification", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Notification gateway is unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "Notification gateway rejected the test", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type NotificationChannelAPI struct {
	ChannelID   string `json:"channel_id"`
	ChannelType string `json:"channel_type"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	Config      any    `json:"config"`
}

type CreateNotificationChannelRequest struct {
	ChannelType string `json:"channel_type"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	Config      any    `json:"config"`
}

type UpdateNotificationChannelRequest struct {
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	Config      any    `json:"config"`
}

type ConfigField struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Placeholder string   `json:"placeholder,omitempty"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type ChannelTypeInfo struct {
	Type         string        `json:"type"`
	Label        string        `json:"label"`
	ConfigFields []ConfigField `json:"config_fields"`
}

var channelTypeInfo = map[string]ChannelTypeInfo{
	"discord": {
		Type:  "discord",
		Label: "Discord Webhook",
		ConfigFields: []ConfigField{
			{Name: "webhook_url", Label: "Webhook URL", Type: "string", Required: true, Placeholder: "https://discord.com/api/webhooks/..."},
		},
	},
	"email": {
		Type:  "email",
		Label: "Email (exe.dev)",
		ConfigFields: []ConfigField{
			{Name: "to", Label: "Recipient Email", Type: "string", Required: true, Placeholder: "you@example.com"},
		},
	},
	"ntfy": {
		Type:  "ntfy",
		Label: "ntfy",
		ConfigFields: []ConfigField{
			{Name: "server", Label: "Server URL", Type: "string", Required: true, Placeholder: "https://ntfy.sh", Default: "https://ntfy.sh"},
			{Name: "topic", Label: "Topic", Type: "string", Required: true, Placeholder: "my-shelley-notifications"},
			{Name: "token", Label: "Access Token", Type: "password", Placeholder: "tk_...", Description: "Optional. For private topics, provide either an access token or username and password."},
			{Name: "username", Label: "Username", Type: "string", Description: "Optional. For private topics, use with password as an alternative to access token."},
			{Name: "password", Label: "Password", Type: "password", Description: "Optional. For private topics, use with username."},
			{Name: "done_priority", Label: "Done Priority", Type: "string", Required: true, Default: "default", Options: []string{"min", "low", "default", "high", "max"}},
			{Name: "error_priority", Label: "Error Priority", Type: "string", Required: true, Default: "high", Options: []string{"min", "low", "default", "high", "max"}},
		},
	},
}

func toNotificationChannelAPI(ch generated.NotificationChannel) NotificationChannelAPI {
	var config any
	if err := json.Unmarshal([]byte(ch.Config), &config); err != nil {
		config = map[string]any{}
	}
	return NotificationChannelAPI{
		ChannelID:   ch.ChannelID,
		ChannelType: ch.ChannelType,
		DisplayName: ch.DisplayName,
		Enabled:     ch.Enabled != 0,
		Config:      config,
	}
}

func (s *Server) handleNotificationChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListNotificationChannels(w, r)
	case http.MethodPost:
		s.handleCreateNotificationChannel(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleListNotificationChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.db.GetNotificationChannels(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get notification channels: %v", err), http.StatusInternalServerError)
		return
	}

	apiChannels := make([]NotificationChannelAPI, len(channels))
	for i, ch := range channels {
		apiChannels[i] = toNotificationChannelAPI(ch)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apiChannels)
}

func (s *Server) handleCreateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	var req CreateNotificationChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.ChannelType == "" || req.DisplayName == "" {
		http.Error(w, "channel_type and display_name are required", http.StatusBadRequest)
		return
	}

	// Validate channel type is registered
	registered := notifications.RegisteredTypes()
	found := false
	for _, t := range registered {
		if t == req.ChannelType {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, fmt.Sprintf("Unknown channel type: %q", req.ChannelType), http.StatusBadRequest)
		return
	}

	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid config: %v", err), http.StatusBadRequest)
		return
	}

	// Validate config by attempting to create the channel
	validationConfig := map[string]any{"type": req.ChannelType}
	var configMap map[string]any
	if err := json.Unmarshal(configJSON, &configMap); err == nil {
		for k, v := range configMap {
			validationConfig[k] = v
		}
	}
	if _, err := notifications.CreateFromConfig(validationConfig, s.logger); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	channelID := "notif-" + uuid.New().String()[:8]
	var enabled int64
	if req.Enabled {
		enabled = 1
	}

	ch, err := s.db.CreateNotificationChannel(r.Context(), generated.CreateNotificationChannelParams{
		ChannelID:   channelID,
		ChannelType: req.ChannelType,
		DisplayName: req.DisplayName,
		Enabled:     enabled,
		Config:      string(configJSON),
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create notification channel: %v", err), http.StatusInternalServerError)
		return
	}

	s.ReloadNotificationChannels()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toNotificationChannelAPI(*ch))
}

func (s *Server) handleNotificationChannel(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/notification-channels/")
	if path == "" {
		http.Error(w, "Invalid channel ID", http.StatusBadRequest)
		return
	}

	if strings.HasSuffix(path, "/test") {
		channelID := strings.TrimSuffix(path, "/test")
		if r.Method == http.MethodPost {
			s.handleTestNotificationChannel(w, r, channelID)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	if strings.Contains(path, "/") {
		http.Error(w, "Invalid channel ID", http.StatusBadRequest)
		return
	}
	channelID := path

	switch r.Method {
	case http.MethodGet:
		s.handleGetNotificationChannel(w, r, channelID)
	case http.MethodPut:
		s.handleUpdateNotificationChannel(w, r, channelID)
	case http.MethodDelete:
		s.handleDeleteNotificationChannel(w, r, channelID)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGetNotificationChannel(w http.ResponseWriter, r *http.Request, channelID string) {
	ch, err := s.db.GetNotificationChannel(r.Context(), channelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Channel not found: %v", err), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toNotificationChannelAPI(*ch))
}

func (s *Server) handleUpdateNotificationChannel(w http.ResponseWriter, r *http.Request, channelID string) {
	existing, err := s.db.GetNotificationChannel(r.Context(), channelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Channel not found: %v", err), http.StatusNotFound)
		return
	}

	var req UpdateNotificationChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid config: %v", err), http.StatusBadRequest)
		return
	}

	// Validate config by attempting to create the channel
	validationConfig := map[string]any{"type": existing.ChannelType}
	var configMap map[string]any
	if err := json.Unmarshal(configJSON, &configMap); err == nil {
		for k, v := range configMap {
			validationConfig[k] = v
		}
	}
	if _, err := notifications.CreateFromConfig(validationConfig, s.logger); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var enabled int64
	if req.Enabled {
		enabled = 1
	}

	ch, err := s.db.UpdateNotificationChannel(r.Context(), generated.UpdateNotificationChannelParams{
		DisplayName: req.DisplayName,
		Enabled:     enabled,
		Config:      string(configJSON),
		ChannelID:   channelID,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update notification channel: %v", err), http.StatusInternalServerError)
		return
	}

	s.ReloadNotificationChannels()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toNotificationChannelAPI(*ch))
}

func (s *Server) handleDeleteNotificationChannel(w http.ResponseWriter, r *http.Request, channelID string) {
	err := s.db.DeleteNotificationChannel(r.Context(), channelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete notification channel: %v", err), http.StatusInternalServerError)
		return
	}

	s.ReloadNotificationChannels()

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestNotificationChannel(w http.ResponseWriter, r *http.Request, channelID string) {
	dbCh, err := s.db.GetNotificationChannel(r.Context(), channelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Channel not found: %v", err), http.StatusNotFound)
		return
	}

	config := map[string]any{"type": dbCh.ChannelType}
	var extra map[string]any
	if err := json.Unmarshal([]byte(dbCh.Config), &extra); err == nil {
		for k, v := range extra {
			config[k] = v
		}
	}

	ch, err := notifications.CreateFromConfig(config, s.logger)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"message": fmt.Sprintf("Failed to create channel: %v", err),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	testEvent := notifications.Event{
		Type:      notifications.EventAgentDone,
		Timestamp: time.Now(),
		Payload: notifications.AgentDonePayload{
			Model:             "test",
			ConversationTitle: "test conversation",
			FinalResponse:     "This is a test notification to verify your channel is working.",
		},
	}

	if err := ch.Send(ctx, testEvent); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"message": fmt.Sprintf("Test failed: %v", err),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Test notification sent successfully",
	})
}

func (s *Server) handleNotificationChannelTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.getNotificationChannelTypes())
}

// getNotificationChannelTypes returns channel type metadata for the frontend.
func (s *Server) getNotificationChannelTypes() []ChannelTypeInfo {
	types := notifications.RegisteredTypes()
	result := make([]ChannelTypeInfo, 0, len(types))
	for _, t := range types {
		if info, ok := channelTypeInfo[t]; ok {
			result = append(result, info)
		} else {
			result = append(result, ChannelTypeInfo{Type: t, Label: t})
		}
	}
	return result
}

// exeNotifyAvailable reports whether the VM has a "notify" integration
// (exe.dev push notifications). Detected once (lazily, via exeNotifyOnce) and
// cached for the process lifetime, so attaching/detaching the integration at
// runtime requires a Shelley restart to take effect.
func (s *Server) exeNotifyAvailable() bool {
	s.exeNotifyOnce.Do(func() {
		s.exeNotifyDetected = exeDevHasNotifyIntegration()
	})
	return s.exeNotifyDetected
}

// exeNotifyEnabled reports whether end-of-turn pushes to the exe.dev notify
// gateway should be auto-configured: the VM must have a "notify" integration
// and the user must not have disabled it via the exe_notify setting.
func (s *Server) exeNotifyEnabled(ctx context.Context) bool {
	// Never send exe.dev push notifications in predictable-only mode. This mode
	// is used by automated browser tests (the LazyCue/Playwright predictable
	// server), which drive many end-of-turn events; we don't want those to fire
	// real push notifications to the VM owner's devices.
	if s.predictableOnly {
		return false
	}
	if !s.exeNotifyAvailable() {
		return false
	}
	// Default to enabled; only an explicit "false" disables it.
	val, err := s.db.GetSetting(ctx, exeNotifySettingKey)
	return !(err == nil && val == "false")
}

// ReloadNotificationChannels reads enabled channels from DB and replaces the dispatcher's channel set.
func (s *Server) ReloadNotificationChannels() {
	channels, err := s.db.GetEnabledNotificationChannels(context.Background())
	if err != nil {
		s.logger.Error("Failed to load notification channels", "error", err)
		return
	}

	var active []notifications.Channel
	for _, dbCh := range channels {
		config := map[string]any{"type": dbCh.ChannelType}
		var extra map[string]any
		if err := json.Unmarshal([]byte(dbCh.Config), &extra); err == nil {
			for k, v := range extra {
				config[k] = v
			}
		}
		ch, err := notifications.CreateFromConfig(config, s.logger)
		if err != nil {
			s.logger.Warn("Failed to create notification channel", "id", dbCh.ChannelID, "error", err)
			continue
		}
		active = append(active, ch)
	}

	s.notifDispatcher.ReplaceChannels(active)
	s.logger.Info("Reloaded notification channels", "count", len(active))
}
