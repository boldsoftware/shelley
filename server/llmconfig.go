package server

import (
	"context"
	"log/slog"
	"net/http"

	"shelley.exe.dev/db"
	"shelley.exe.dev/models"
)

// Link represents a link displayed in the UI overflow menu.
type Link struct {
	Title   string `json:"title"`
	IconSVG string `json:"icon_svg,omitempty"` // SVG path data for the icon
	URL     string `json:"url"`
}

// LLMConfig holds all configuration for LLM services.
type LLMConfig struct {
	// Models is the list of ready-to-use built-in models. The server
	// registers them as-is; custom models are loaded separately from DB.
	Models []models.Built

	// TranscriptionModels are known non-chat transcription routes.
	TranscriptionModels []models.TranscriptionModel

	// DefaultModel is an optional process or shelley.json override. When empty,
	// prefer the first ready tier-1 model, then the first ready model.
	DefaultModel string

	// DB is the database for custom models (optional).
	DB *db.DB

	// HTTPC is the shared HTTP client used by both the built-in models
	// (already baked into Models[*].Service) and custom DB-backed models
	// the Manager constructs. Pass nil to let the Manager create one.
	HTTPC *http.Client

	// RefreshBuiltModels rebuilds the ready-to-use built-in model set.
	// The server calls this for explicit user-triggered refreshes.
	RefreshBuiltModels func(context.Context) ([]models.Built, error)

	Logger *slog.Logger
}
