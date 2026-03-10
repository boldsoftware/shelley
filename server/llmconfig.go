package server

import (
	"log/slog"

	"shelley.exe.dev/db"
)

// Link represents a custom link to be displayed in the UI
type Link struct {
	Title   string `json:"title"`
	IconSVG string `json:"icon_svg,omitempty"` // SVG path data for the icon
	URL     string `json:"url"`
}

// LLMConfig holds all configuration for LLM services
type LLMConfig struct {
	// API keys for each provider
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GeminiAPIKey    string
	FireworksAPIKey string

	// Gateway is the base URL of the LLM gateway (optional)
	Gateway string

	// TerminalURL is the URL to the terminal interface (optional)
	TerminalURL string

	// DefaultModel is the default model to use (optional, defaults to models.Default())
	DefaultModel string

	// Links are custom links to be displayed in the UI (optional)
	Links []Link

	// NotificationChannels is a list of notification channel configs from shelley.json.
	// Each entry is a map with at least a "type" key, plus channel-specific fields.
	NotificationChannels []map[string]any

	// Slack integration tokens (optional)
	SlackBotToken string
	SlackAppToken string

	// DB is the database for recording LLM requests (optional)
	DB *db.DB

	Logger *slog.Logger
}
