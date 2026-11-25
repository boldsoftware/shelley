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
	nextPromptQuickRepliesMax    = 4
	nextPromptQuickReplyMaxRunes = 24
)

// suggestNextPromptLocked asks the model, in the background, what the user
// will type after the turn that ended at endSeq. The request is the next
// turn's would-be prefix, the turn's last request and what followed it, plus
// the suggestion prompt, so the provider's prompt cache covers nearly all of
// it. Idle work cancellation stops it. The caller holds mu.
func (cm *ConversationManager) suggestNextPromptLocked(endSeq int64) {
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
		if err := cm.suggestNextPrompt(ctx, endSeq, service, &req); err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			cm.logger.Warn("next prompt suggestion failed", "error", err)
		}
	}()
}

func (cm *ConversationManager) suggestNextPrompt(ctx context.Context, endSeq int64, service llm.Service, req *llm.Request) error {
	s := cm.server
	if on, err := s.flagOn(ctx, FlagNextPromptSuggestions); err != nil || !on {
		return err
	}
	// Spend nothing on a suggestion the composer will not show: the user
	// answers channel conversations, and texted messages, in their chat, and
	// other work will start another turn first.
	conversation, err := s.db.GetConversationByID(ctx, cm.conversationID)
	if err != nil || conversation.ExternalConversationID != nil {
		return err
	}
	if texted, err := s.db.LatestUserExternalMessageID(ctx, cm.conversationID); err != nil || texted != "" {
		return err
	}
	if quiet, err := s.conversationQuiet(ctx, conversation); err != nil || !quiet {
		return err
	}
	ctx = llmhttp.WithConversationID(llm.WithPurpose(ctx, nextPromptSuggestionPurpose), cm.conversationID)
	resp, err := service.Do(ctx, req)
	if err != nil {
		return err
	}
	text, quickReplies, err := parseNextPromptSuggestion(resp)
	if err != nil {
		cm.logger.Warn("unusable next prompt suggestion", "error", err)
	}
	// Record what was spent even if the conversation moved on meanwhile.
	usage := llm.PurposedUsage{Purpose: nextPromptSuggestionPurpose, Usage: resp.UsageWithMeta()}
	marker, err := s.db.SetNextPromptSuggestion(context.WithoutCancel(ctx), cm.conversationID, endSeq, text, quickReplies, usage)
	if err != nil {
		return err
	}
	s.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID, *marker)
	return nil
}

// parseNextPromptSuggestion returns the suggested message, or "" when the
// model had none, and its quick replies. A reply that breaks the requested
// format is an error, though a code fence around the JSON is fine. So is an
// unusable suggestion, but then the quick replies are still returned. They
// are kept up to the first unusable one, and at most four.
func parseNextPromptSuggestion(resp *llm.Response) (string, []string, error) {
	var reply struct {
		Suggestion   *string         `json:"suggestion"`
		QuickReplies json.RawMessage `json:"quick_replies"`
	}
	if err := json.Unmarshal([]byte(unfence(llm.FirstText(resp))), &reply); err != nil {
		types := make([]string, len(resp.Content))
		for i, c := range resp.Content {
			types[i] = c.Type.String()
		}
		return "", nil, fmt.Errorf("decode %q (stop reason %v, content %v): %w", llm.FirstText(resp), resp.StopReason, types, err)
	}
	// A malformed list costs only the quick replies, not the suggestion.
	var items []any
	_ = json.Unmarshal(reply.QuickReplies, &items)
	var quickReplies []string
	for _, r := range items {
		if len(quickReplies) == nextPromptQuickRepliesMax {
			break
		}
		text, _ := r.(string)
		text = strings.TrimSpace(text)
		// A tap sends the reply as typed, so it must not run a command.
		if text == "" || !oneLine(text, nextPromptQuickReplyMaxRunes) || strings.ContainsAny(text[:1], "/!") {
			break
		}
		if !slices.ContainsFunc(quickReplies, func(q string) bool { return strings.EqualFold(q, text) }) {
			quickReplies = append(quickReplies, text)
		}
	}
	if reply.Suggestion == nil {
		return "", quickReplies, nil
	}
	text := strings.TrimSpace(*reply.Suggestion)
	if !oneLine(text, nextPromptSuggestionMaxRunes) {
		return "", quickReplies, fmt.Errorf("not one line of at most %d characters: %q", nextPromptSuggestionMaxRunes, text)
	}
	return text, quickReplies, nil
}

// unfence returns text without a Markdown code fence wrapped around all of
// it, which some models add to JSON despite being asked not to: any run of
// three or more backticks or tildes, an optional language tag, and the same
// run closing it, on separate lines or not.
func unfence(text string) string {
	text = strings.TrimSpace(text)
	fence := text[:len(text)-len(strings.TrimLeft(text, "`"))]
	if fence == "" {
		fence = text[:len(text)-len(strings.TrimLeft(text, "~"))]
	}
	if len(fence) < 3 || len(text) < 2*len(fence) || !strings.HasSuffix(text, fence) {
		return text
	}
	body := text[len(fence) : len(text)-len(fence)]
	tag, rest, ok := strings.Cut(body, "{")
	if !ok || len(strings.Fields(tag)) > 1 {
		return text
	}
	return strings.TrimSpace("{" + rest)
}

// oneLine reports whether text is one line of at most maxRunes characters.
func oneLine(text string, maxRunes int) bool {
	lineBreak := func(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Zl, unicode.Zp) }
	return utf8.RuneCountInString(text) <= maxRunes && !strings.ContainsFunc(text, lineBreak)
}
