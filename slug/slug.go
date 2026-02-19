package slug

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
)

// LLMServiceProvider defines the interface for getting LLM services
type LLMServiceProvider interface {
	GetService(modelID string) (llm.Service, error)
	GetAvailableModels() []string
	GetModelInfo(modelID string) *models.ModelInfo
}

// GenerateSlug generates a slug for a conversation and updates the database
// If conversationModelID is provided, it will be used as a fallback if no model is tagged with "slug"
func GenerateSlug(ctx context.Context, llmProvider LLMServiceProvider, database *db.DB, logger *slog.Logger, conversationID, userMessage, conversationModelID string) (string, error) {
	baseSlug, err := generateSlugText(ctx, llmProvider, logger, userMessage, conversationModelID)
	if err != nil {
		return "", err
	}

	// Try to update with the base slug first, then with numeric suffixes if needed
	slug := baseSlug
	for attempt := 0; attempt < 100; attempt++ {
		_, err = database.UpdateConversationSlug(ctx, conversationID, slug)
		if err == nil {
			// Success!
			logger.Info("Generated slug for conversation", "conversationID", conversationID, "slug", slug)
			return slug, nil
		}

		// Check if this is a unique constraint violation
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") ||
			strings.Contains(strings.ToLower(err.Error()), "unique constraint") ||
			strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			// Try with a numeric suffix
			slug = fmt.Sprintf("%s-%d", baseSlug, attempt+1)
			continue
		}

		// Some other error occurred
		return "", fmt.Errorf("failed to update conversation slug: %w", err)
	}

	// If we've tried 100 times and still failed, give up
	return "", fmt.Errorf("failed to generate unique slug after 100 attempts")
}

// generateSlugText generates a human-readable slug for a conversation based on the user message
// Priority order:
// 1. If conversationModelID is "predictable", use it
// 2. Try models tagged with "slug" (try the LLM call; if it fails, continue)
// 3. Try models tagged with "slug-backup"
// 4. Fall back to the conversation's model (conversationModelID)
func generateSlugText(ctx context.Context, llmProvider LLMServiceProvider, logger *slog.Logger, userMessage, conversationModelID string) (string, error) {
	// If conversation is using predictable model, use it for slug generation too
	if conversationModelID == "predictable" {
		llmService, err := llmProvider.GetService("predictable")
		if err == nil {
			logger.Debug("Using predictable model for slug generation")
			return callSlugLLM(ctx, llmService, userMessage)
		}
		logger.Debug("Predictable model not available for slug generation", "error", err)
	}

	// Try models tagged with "slug", then "slug-backup"
	for _, tag := range []string{"slug", "slug-backup"} {
		for _, modelID := range llmProvider.GetAvailableModels() {
			info := llmProvider.GetModelInfo(modelID)
			if info == nil || !hasTag(info.Tags, tag) {
				continue
			}
			llmService, err := llmProvider.GetService(modelID)
			if err != nil {
				logger.Debug("Failed to get model for slug generation", "model", modelID, "tag", tag, "error", err)
				continue
			}
			logger.Debug("Trying model for slug generation", "model", modelID, "tag", tag)
			slug, err := callSlugLLM(ctx, llmService, userMessage)
			if err == nil {
				return slug, nil
			}
			logger.Warn("Slug generation failed, trying next model", "model", modelID, "tag", tag, "error", err)
		}
	}

	// Fall back to the conversation's model
	if conversationModelID != "" && conversationModelID != "predictable" {
		llmService, err := llmProvider.GetService(conversationModelID)
		if err == nil {
			logger.Debug("Using conversation model for slug generation", "model", conversationModelID)
			return callSlugLLM(ctx, llmService, userMessage)
		}
		logger.Debug("Conversation model not available for slug generation", "model", conversationModelID, "error", err)
	}

	return "", fmt.Errorf("no suitable model available for slug generation")
}

// hasTag checks if a comma-separated tag list contains the exact given tag.
func hasTag(tags, tag string) bool {
	for _, t := range strings.Split(tags, ",") {
		if strings.TrimSpace(t) == tag {
			return true
		}
	}
	return false
}

// callSlugLLM calls an LLM service to generate a slug from a user message.
func callSlugLLM(ctx context.Context, llmService llm.Service, userMessage string) (string, error) {
	slugPrompt := fmt.Sprintf(`Generate a short, descriptive slug (2-6 words, lowercase, hyphen-separated) for a conversation that starts with this user message:

%s

The slug should:
- Be concise and descriptive
- Use only lowercase letters, numbers, and hyphens
- Capture the main topic or intent
- Be suitable as a filename or URL path

Respond with only the slug, nothing else.`, userMessage)

	message := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: slugPrompt},
		},
	}

	request := &llm.Request{
		Messages: []llm.Message{message},
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	response, err := llmService.Do(ctxWithTimeout, request)
	if err != nil {
		return "", fmt.Errorf("failed to generate slug: %w", err)
	}

	// Find the first text content block, skipping thinking blocks
	var rawSlug string
	for _, c := range response.Content {
		if c.Type == llm.ContentTypeText && c.Text != "" {
			rawSlug = c.Text
			break
		}
	}
	if rawSlug == "" {
		return "", fmt.Errorf("no text content in LLM response")
	}

	slug := strings.TrimSpace(rawSlug)
	slug = Sanitize(slug)
	if slug == "" {
		return "", fmt.Errorf("generated slug is empty after sanitization")
	}

	return slug, nil
}

// Sanitize cleans a string to be a valid slug
func Sanitize(input string) string {
	// Convert to lowercase
	slug := strings.ToLower(input)

	// Replace spaces and underscores with hyphens
	slug = regexp.MustCompile(`[\s_]+`).ReplaceAllString(slug, "-")

	// Remove non-alphanumeric characters except hyphens
	slug = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(slug, "")

	// Remove multiple consecutive hyphens
	slug = regexp.MustCompile(`-+`).ReplaceAllString(slug, "-")

	// Remove leading/trailing hyphens
	slug = strings.Trim(slug, "-")

	// Limit length
	if len(slug) > 60 {
		slug = slug[:60]
		slug = strings.Trim(slug, "-")
	}

	return slug
}
