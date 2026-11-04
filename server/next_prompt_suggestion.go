package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
)

//go:embed next_prompt_suggestion_prompt.txt
var nextPromptSuggestionPrompt string

const (
	nextPromptSuggestionPurpose  = "next_prompt_suggestion"
	nextPromptSuggestionMaxRunes = 240
)

// suggestNextPromptLocked asks the model, in the background, what the user
// will type after the turn that ended at endSeq. The request is the next
// turn's would-be prefix, the turn's last request and what followed it, plus
// the suggestion prompt, so the provider's prompt cache covers nearly all of
// it. Idle work cancellation stops it. The caller holds mu.
func (cm *ConversationManager) suggestNextPromptLocked(s *Server, endSeq int64) {
	tail := cm.cacheWarmTail
	// Providers reject empty messages, and an empty reply leaves nothing to
	// follow up on anyway.
	if cm.cacheWarmRequest == nil || len(tail) == 0 || slices.ContainsFunc(tail, func(m llm.Message) bool { return len(m.Content) == 0 }) {
		return
	}
	req := *cm.cacheWarmRequest
	req.Messages = slices.Concat(req.Messages, tail, []llm.Message{llm.UserStringMessage(nextPromptSuggestionPrompt)})
	service := cm.cacheWarmService
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	cm.cancelSuggestion = cancel
	go func() {
		defer cancel()
		if err := cm.suggestNextPrompt(ctx, s, endSeq, service, &req); err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			cm.logger.Warn("next prompt suggestion failed", "error", err)
		}
	}()
}

func (cm *ConversationManager) suggestNextPrompt(ctx context.Context, s *Server, endSeq int64, service llm.Service, req *llm.Request) error {
	if on, err := s.flagOn(ctx, FlagNextPromptSuggestions); err != nil || !on {
		return err
	}
	// Spend nothing on a suggestion the composer will not show: the user
	// answers channel conversations, and the Assistant's texted messages, in
	// their chat, and other work will start another turn first.
	conversation, err := s.db.GetConversationByID(ctx, cm.conversationID)
	if err != nil || conversation.ExternalConversationID != nil {
		return err
	}
	if s.isAssistant(cm.conversationID) {
		if texted, err := s.db.LatestUserExternalMessageID(ctx, cm.conversationID); err != nil || texted != "" {
			return err
		}
	}
	if quiet, err := s.conversationQuiet(ctx, conversation); err != nil || !quiet {
		return err
	}
	ctx = llmhttp.WithConversationID(llm.WithPurpose(ctx, nextPromptSuggestionPurpose), cm.conversationID)
	resp, err := service.Do(ctx, req)
	if err != nil {
		return err
	}
	text, err := parseNextPromptSuggestion(resp)
	if err != nil {
		cm.logger.Warn("unusable next prompt suggestion", "error", err)
	}
	// Record what was spent even if the conversation moved on meanwhile.
	usage := llm.PurposedUsage{Purpose: nextPromptSuggestionPurpose, Usage: resp.UsageWithMeta()}
	marker, err := s.db.SetNextPromptSuggestion(context.WithoutCancel(ctx), cm.conversationID, endSeq, text, usage)
	if err != nil {
		return err
	}
	s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID, *marker)
	return nil
}

// parseNextPromptSuggestion returns the suggested message, or "" when the
// model had none. A reply that breaks the requested format is an error.
func parseNextPromptSuggestion(resp *llm.Response) (string, error) {
	var reply struct {
		Suggestion *string `json:"suggestion"`
	}
	if err := json.Unmarshal([]byte(llm.FirstText(resp)), &reply); err != nil {
		types := make([]string, len(resp.Content))
		for i, c := range resp.Content {
			types[i] = c.Type.String()
		}
		return "", fmt.Errorf("decode %q (stop reason %v, content %v): %w", llm.FirstText(resp), resp.StopReason, types, err)
	}
	if reply.Suggestion == nil {
		return "", nil
	}
	text := strings.TrimSpace(*reply.Suggestion)
	lineBreak := func(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Zl, unicode.Zp) }
	if utf8.RuneCountInString(text) > nextPromptSuggestionMaxRunes || strings.ContainsFunc(text, lineBreak) {
		return "", fmt.Errorf("not one line of at most %d characters: %q", nextPromptSuggestionMaxRunes, text)
	}
	return text, nil
}
