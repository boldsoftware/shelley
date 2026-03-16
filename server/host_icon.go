package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"shelley.exe.dev/llm"
)

const hostIconSettingKey = "host_icon_svg"

// generateHostIcon uses an LLM to create an SVG icon inspired by the hostname.
func generateHostIcon(ctx context.Context, llmProvider LLMProvider, hostname string, logger *slog.Logger) (string, error) {
	svc, err := llmProvider.GetService("claude-haiku-4.5")
	if err != nil {
		return "", fmt.Errorf("get haiku service: %w", err)
	}

	prompt := fmt.Sprintf(
		`Generate a simple, cute SVG icon (64x64 viewBox) inspired by the word %q. `+
			`The icon should be a recognizable representation of the concept. `+
			`Use warm, friendly colors. Keep shapes simple with minimal detail. `+
			`Output ONLY the raw SVG markup. No markdown fences, no explanation, no comments inside the SVG.`,
		hostname,
	)

	resp, err := svc.Do(ctx, &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: prompt}},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("llm request: %w", err)
	}

	for _, c := range resp.Content {
		if c.Type == llm.ContentTypeText && strings.Contains(c.Text, "<svg") {
			// Extract just the SVG
			start := strings.Index(c.Text, "<svg")
			end := strings.LastIndex(c.Text, "</svg>")
			if start >= 0 && end >= 0 {
				return c.Text[start : end+len("</svg>")], nil
			}
		}
	}
	return "", fmt.Errorf("no SVG in LLM response")
}

// EnsureHostIcon checks the DB for a cached host icon, generating one if needed.
// Runs in a goroutine at startup; errors are logged, not fatal.
func (s *Server) EnsureHostIcon() {
	ctx := context.Background()
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}

	// Check if we already have one cached (with matching hostname).
	existing, _ := s.db.GetSetting(ctx, hostIconSettingKey)
	if existing != "" {
		// Also check hostname hasn't changed.
		cachedHost, _ := s.db.GetSetting(ctx, hostIconSettingKey+"_hostname")
		if cachedHost == hostname {
			return
		}
	}

	svg, err := generateHostIcon(ctx, s.llmManager, hostname, s.logger)
	if err != nil {
		s.logger.Warn("Failed to generate host icon", "error", err)
		return
	}

	if err := s.db.SetSetting(ctx, hostIconSettingKey, svg); err != nil {
		s.logger.Warn("Failed to cache host icon", "error", err)
		return
	}
	if err := s.db.SetSetting(ctx, hostIconSettingKey+"_hostname", hostname); err != nil {
		s.logger.Warn("Failed to cache host icon hostname", "error", err)
	}
	s.logger.Info("Generated host icon", "hostname", hostname)
}

// handleHostIcon serves the cached host icon SVG.
func (s *Server) handleHostIcon(w http.ResponseWriter, r *http.Request) {
	svg, err := s.db.GetSetting(r.Context(), hostIconSettingKey)
	if err != nil || svg == "" {
		http.Error(w, "no icon available", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	fmt.Fprint(w, svg)
}
