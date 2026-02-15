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
	"shelley.exe.dev/server/notifications"
)

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

// SeedNotificationChannelsFromConfig seeds the DB with notification channels
// from shelley.json config if the DB table is empty. One-time migration for
// backwards compatibility.
func (s *Server) SeedNotificationChannelsFromConfig(configs []map[string]any) {
	existing, err := s.db.GetNotificationChannels(context.Background())
	if err != nil {
		s.logger.Warn("Failed to check existing notification channels", "error", err)
		return
	}
	if len(existing) > 0 {
		return // Already have channels in DB, skip seeding
	}
	if len(configs) == 0 {
		return
	}

	for _, cfg := range configs {
		typeName, _ := cfg["type"].(string)
		if typeName == "" {
			continue
		}

		// Build config without the "type" key
		configMap := make(map[string]any)
		for k, v := range cfg {
			if k != "type" {
				configMap[k] = v
			}
		}
		configJSON, err := json.Marshal(configMap)
		if err != nil {
			s.logger.Warn("Failed to marshal config for seeding", "type", typeName, "error", err)
			continue
		}

		channelID := "notif-" + uuid.New().String()[:8]
		_, err = s.db.CreateNotificationChannel(context.Background(), generated.CreateNotificationChannelParams{
			ChannelID:   channelID,
			ChannelType: typeName,
			DisplayName: typeName,
			Enabled:     1,
			Config:      string(configJSON),
		})
		if err != nil {
			s.logger.Warn("Failed to seed notification channel", "type", typeName, "error", err)
			continue
		}
		s.logger.Info("Seeded notification channel from config", "type", typeName, "id", channelID)
	}
}
