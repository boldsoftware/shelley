package server

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/models"
)

const chapterIndexUserTurns = 4

//go:embed chapter_index_prompt.txt
var chapterIndexPrompt string

func (cm *ConversationManager) indexChaptersLocked(endSeq int64) {
	modelID := cm.modelID
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	cm.cancelChapterIndex = cancel
	go func() {
		defer cancel()
		if err := cm.indexChapters(ctx, endSeq, modelID); err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			cm.logger.Warn("chapter indexing failed", "error", err)
		}
	}()
}

func (cm *ConversationManager) indexChapters(ctx context.Context, endSeq int64, modelID string) error {
	if cm.role != roleTopLevel {
		return nil
	}
	themes, err := cm.db.GetConversationThemes(ctx, cm.conversationID)
	if err != nil {
		return err
	}
	var through int64
	if themes.ThroughMessageID != "" {
		m, err := cm.db.GetMessageByID(ctx, themes.ThroughMessageID)
		if err != nil {
			return err
		}
		through = m.SequenceID
	}
	users, err := cm.db.ListTypedUserMessages(ctx, cm.conversationID)
	if err != nil {
		return err
	}
	originals := users[:0]
	for _, user := range users {
		if user.SequenceID > endSeq {
			continue
		}
		var data struct {
			CompactionCarried string `json:"compaction_carried"`
		}
		if user.UserData != nil && *user.UserData != "" {
			if err := json.Unmarshal([]byte(*user.UserData), &data); err != nil {
				return err
			}
		}
		if data.CompactionCarried != "true" {
			originals = append(originals, user)
		}
	}
	users = originals
	slices.SortFunc(users, func(a, b generated.Message) int {
		return cmp.Compare(a.SequenceID, b.SequenceID)
	})
	first := slices.IndexFunc(users, func(m generated.Message) bool { return m.SequenceID > through })
	if first < 0 || len(users)-first < chapterIndexUserTurns {
		return nil
	}
	registry, ok := cm.server.llmManager.(*models.Manager)
	if !ok {
		return fmt.Errorf("chapter indexing requires the native model registry")
	}
	service, err := registry.GetWorkhorseServiceNoFallback(modelID)
	if err != nil {
		return err
	}
	answers, err := cm.db.ListChapterAnswerText(ctx, cm.conversationID, users[len(users)-1].SequenceID)
	if err != nil {
		return err
	}
	type entry struct {
		ID     string `json:"message_id"`
		Text   string `json:"text"`
		Answer string `json:"answer_excerpt,omitempty"`
	}
	eligible := make(map[string]int)
	transcript := []entry{}
	for i, m := range users {
		text, err := chapterIndexText(m.LlmData, 3000)
		if err != nil {
			return err
		}
		if text == "" {
			continue
		}
		id := fmt.Sprintf("u%d", i+1)
		eligible[id] = i
		var answer string
		if i+1 < len(users) {
			for len(answers) > 0 && answers[0].SequenceID < users[i+1].SequenceID {
				a := answers[0]
				answers = answers[1:]
				if a.SequenceID > m.SequenceID && utf8.RuneCountInString(answer) <= 400 {
					if answer != "" {
						answer += "\n"
					}
					answer += a.Excerpt
				}
			}
		}
		if r := []rune(answer); len(r) > 400 {
			answer = string(r[:400]) + "\n[excerpt ends]"
		}
		transcript = append(transcript, entry{id, text, answer})
	}
	payload, err := json.Marshal(struct {
		Transcript []entry `json:"transcript"`
	}{transcript})
	if err != nil {
		return err
	}
	ctx = llmhttp.WithConversationID(llm.WithPurpose(ctx, "chapter_index"), cm.conversationID)
	response, err := service.Do(ctx, &llm.Request{
		Messages:      []llm.Message{llm.UserStringMessage(chapterIndexPrompt + "\n" + string(payload))},
		ThinkingLevel: llm.ThinkingLevelMedium,
	})
	if err != nil {
		return err
	}
	marker, err := cm.db.CreateUsageMessage(context.WithoutCancel(ctx), cm.conversationID, []llm.PurposedUsage{
		{Purpose: "chapter_index", Usage: response.UsageWithMeta()},
	})
	if err != nil {
		return err
	}
	defer cm.server.notifySubscribers(context.WithoutCancel(ctx), cm.conversationID, *marker)
	if err := ctx.Err(); err != nil {
		return err
	}
	var result struct {
		Chapters *[]db.ConversationChapter `json:"chapters"`
	}
	if err := json.Unmarshal([]byte(llm.FirstText(response)), &result); err != nil {
		return fmt.Errorf("decode chapter index: %w", err)
	}
	if result.Chapters == nil {
		return fmt.Errorf("chapter index response is missing chapters")
	}

	themes.Chapters = nil
	var last int64
	for _, chapter := range *result.Chapters {
		index, ok := eligible[chapter.MessageID]
		title := strings.TrimSpace(chapter.Title)
		if !ok || users[index].SequenceID <= last || title == "" || utf8.RuneCountInString(title) > 50 {
			return fmt.Errorf("invalid indexed chapter: %+v", chapter)
		}
		if len(themes.Chapters) > 0 && strings.EqualFold(themes.Chapters[len(themes.Chapters)-1].Title, title) {
			return fmt.Errorf("chapter repeats the previous heading")
		}
		chapter.Title = title
		chapter.MessageID = users[index].MessageID
		themes.Chapters = append(themes.Chapters, chapter)
		last = users[index].SequenceID
	}
	themes.ThroughMessageID = users[len(users)-1].MessageID
	if err := cm.db.SetConversationThemes(ctx, cm.conversationID, themes); err != nil {
		return err
	}
	cm.logger.Info("chapter index updated", "user_turns", len(users)-first, "chapters", len(*result.Chapters))
	return nil
}

func chapterIndexText(data *string, limit int) (string, error) {
	if data == nil {
		return "", nil
	}
	var message llm.Message
	if err := json.Unmarshal([]byte(*data), &message); err != nil {
		return "", err
	}
	var texts []string
	for _, content := range message.Content {
		if content.Type == llm.ContentTypeText {
			texts = append(texts, content.Text)
		}
	}
	text := strings.Join(texts, "\n")
	if runes := []rune(text); len(runes) > limit {
		text = string(runes[:limit]) + "\n[excerpt ends]"
	}
	return text, nil
}
