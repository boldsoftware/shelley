package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"

	"shelley.exe.dev/server/notifications/channels"
)

// initWebPush loads or generates VAPID keys, stores them in settings, and
// registers the web push notification channel with the dispatcher.
func (s *Server) initWebPush() error {
	ctx := context.Background()

	privKey, err := s.db.GetSetting(ctx, "push_vapid_private_key")
	if err != nil {
		return fmt.Errorf("get vapid private key: %w", err)
	}
	pubKey, err := s.db.GetSetting(ctx, "push_vapid_public_key")
	if err != nil {
		return fmt.Errorf("get vapid public key: %w", err)
	}

	if privKey == "" || pubKey == "" {
		s.logger.Info("Generating VAPID key pair for web push")
		privKey, pubKey, err = webpush.GenerateVAPIDKeys()
		if err != nil {
			return fmt.Errorf("generate vapid keys: %w", err)
		}
		if err := s.db.SetSetting(ctx, "push_vapid_private_key", privKey); err != nil {
			return fmt.Errorf("store vapid private key: %w", err)
		}
		if err := s.db.SetSetting(ctx, "push_vapid_public_key", pubKey); err != nil {
			return fmt.Errorf("store vapid public key: %w", err)
		}
	}

	ch := channels.NewWebPushChannel(s.db, privKey, pubKey, s.logger)
	s.notifDispatcher.Register(ch)
	s.logger.Info("Web push notifications enabled")
	return nil
}

// handlePushVapidKey returns the VAPID public key so the frontend can subscribe.
func (s *Server) handlePushVapidKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pubKey, err := s.db.GetSetting(r.Context(), "push_vapid_public_key")
	if err != nil || pubKey == "" {
		http.Error(w, "Web push not initialized", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"public_key": pubKey})
}

// PushSubscribeRequest is the body of POST /api/push/subscribe.
type PushSubscribeRequest struct {
	Endpoint  string `json:"endpoint"`
	P256DH    string `json:"p256dh"`
	Auth      string `json:"auth"`
	UserAgent string `json:"user_agent"`
}

// handlePushSubscribe stores a push subscription.
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req PushSubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Endpoint == "" || req.P256DH == "" || req.Auth == "" {
		http.Error(w, "endpoint, p256dh, and auth are required", http.StatusBadRequest)
		return
	}

	id := "push-" + uuid.New().String()[:8]
	if err := s.db.CreatePushSubscription(r.Context(), id, req.Endpoint, req.P256DH, req.Auth, req.UserAgent); err != nil {
		s.logger.Error("Failed to create push subscription", "error", err)
		http.Error(w, "Failed to store subscription", http.StatusInternalServerError)
		return
	}

	s.logger.Info("Push subscription registered", "id", id, "endpoint", truncateURL(req.Endpoint, 60))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

// handlePushUnsubscribe removes a push subscription by endpoint.
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Endpoint == "" {
		http.Error(w, "endpoint is required", http.StatusBadRequest)
		return
	}
	if err := s.db.DeletePushSubscriptionByEndpoint(r.Context(), req.Endpoint); err != nil {
		s.logger.Error("Failed to delete push subscription", "error", err)
		http.Error(w, "Failed to delete subscription", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func truncateURL(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
