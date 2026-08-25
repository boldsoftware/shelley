package slug

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/models"
)

// LLMServiceProvider provides LLM services and workhorse model selection.
type LLMServiceProvider = models.WorkhorseProvider

// GenerateSlug generates a slug for a conversation and updates the database.
// If the conversation already has a slug, it is returned unchanged (no LLM call).
// If conversationModelID is provided, its provider's workhorse model is used
// when available, with one retry on the conversation model if it fails.
//
// Also returns the appended slug marker message (nil when no LLM call was made
// or no usage was reported). The caller MUST publish it: it carries a real
// sequence_id, so a client that never receives it sees a hole in the sequence
// and correctly discards its cached history.
func GenerateSlug(ctx context.Context, llmProvider LLMServiceProvider, database *db.DB, logger *slog.Logger, conversationID, userMessage, conversationModelID string) (string, *generated.Message, error) {
	// Don't regenerate if the conversation already has a slug. This matters
	// for flows that look like "first message" but are actually continuations,
	// e.g. starting a new generation after compaction.
	if conv, err := database.GetConversationByID(ctx, conversationID); err == nil && conv != nil && conv.Slug != nil && *conv.Slug != "" {
		return *conv.Slug, nil, nil
	}

	// Tag the ctx so the slug LLM call's usage is collected; it is recorded on
	// an appended slug marker message below. (WithConversationID also stamps the
	// gateway request logs; the caller's ctx does not carry it.)
	var otherUsage llmhttp.UsageAccumulator
	ctx = llmhttp.WithUsageCollector(ctx, otherUsage.Collect)
	ctx = llmhttp.WithConversationID(llmhttp.WithPurpose(ctx, "slug"), conversationID)

	baseSlug, err := generateSlugText(ctx, llmProvider, userMessage, conversationModelID)
	if err != nil {
		return "", nil, err
	}

	// Record the slug call's cost by APPENDING a marker message rather than
	// editing an existing row. Message rows are an append-only log: the browser
	// caches them keyed by (conversation_id, sequence_id) and only ever fetches
	// the tail, forks copy them, and a sequence_id is streamed exactly once. The
	// marker renders as nothing and is never sent to the LLM; it exists purely so
	// the usage is accounted for through the ordinary message-usage path.
	var marker *generated.Message
	if entries := otherUsage.Take(); len(entries) > 0 {
		marker, err = database.CreateSlugMessage(ctx, conversationID, entries)
		if err != nil {
			logger.Error("Failed to record slug usage", "conversationID", conversationID, "error", err)
		}
	}

	// Try to update with the base slug first, then with numeric suffixes if needed
	slug := baseSlug
	for attempt := 0; attempt < 100; attempt++ {
		_, err = database.UpdateConversationSlug(ctx, conversationID, slug)
		if err == nil {
			// Success!
			logger.Info("Generated slug for conversation", "conversationID", conversationID, "slug", slug)
			return slug, marker, nil
		}

		// Check if this is a unique constraint violation
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") ||
			strings.Contains(strings.ToLower(err.Error()), "unique constraint") ||
			strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			// Try with a numeric suffix
			slug = fmt.Sprintf("%s-%d", baseSlug, attempt+1)
			continue
		}

		// Some other error occurred. The marker still comes back so the caller
		// publishes it: the row exists and owns a sequence_id either way.
		return "", marker, fmt.Errorf("failed to update conversation slug: %w", err)
	}

	// If we've tried 100 times and still failed, give up
	return "", marker, fmt.Errorf("failed to generate unique slug after 100 attempts")
}

// generateSlugText generates a human-readable slug with the conversation's
// provider workhorse, or the conversation model when no workhorse is available.
func generateSlugText(ctx context.Context, llmProvider LLMServiceProvider, userMessage, conversationModelID string) (string, error) {
	slugPrompt := fmt.Sprintf(PromptPreamble+`

%s

The slug should:
- Be concise and descriptive
- Use only lowercase letters, numbers, and hyphens
- Capture the main topic or intent
- Be suitable as a filename or URL path

Respond with exactly one slug and no other text or markup.`, userMessage)

	request := &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{llm.StringContent(slugPrompt)},
		}},
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	response, err := models.WorkhorseDo(ctxWithTimeout, llmProvider, conversationModelID, request)
	if err != nil {
		return "", fmt.Errorf("failed to generate slug: %w", err)
	}

	slug := Sanitize(strings.TrimSpace(llm.FirstText(response)))
	if slug == "" {
		return "", fmt.Errorf("generated slug is empty after sanitization")
	}
	return slug, nil
}

// PromptPreamble is the fixed leading text of the slug-generation prompt. It is
// exported so tests (e.g. fake LLM services shared with the agent loop) can
// reliably distinguish a slug request from a real agent turn. Keep it in sync
// with the format string in generateSlugText.
const PromptPreamble = "Generate a short, descriptive slug (2-6 words, lowercase, hyphen-separated) for a conversation that starts with this user message:"

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
