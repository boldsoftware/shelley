package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

var errProfileTurnStopped = errors.New("profile turn interrupted; resend the +Profile message to continue")

// checkProfileResume rejects a profile turn whose in-memory runner was lost.
// seq excludes the terminal error being retried; 0 means the current turn.
func (cm *ConversationManager) checkProfileResume(ctx context.Context, seq int64) error {
	cm.mu.Lock()
	hasRunner := cm.profileTurn
	cm.mu.Unlock()
	if hasRunner {
		return nil
	}
	rows, err := cm.db.ListMessages(ctx, cm.conversationID)
	if err != nil {
		return err
	}
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		if seq > 0 && row.SequenceID >= seq {
			continue
		}
		if row.Type == string(db.MessageTypeProfileTurn) {
			return errProfileTurnStopped
		}
		if isAgentEndOfTurn(&row) {
			return nil
		}
		if row.Type != string(db.MessageTypeUser) || row.LlmData == nil {
			continue
		}
		var message llm.Message
		if err := json.Unmarshal([]byte(*row.LlmData), &message); err != nil {
			return err
		}
		var userData []byte
		if row.UserData != nil {
			userData = []byte(*row.UserData)
		}
		if typedUserForLLM(message, userData) {
			return nil
		}
	}
	return nil
}

// profileMention matches the longest profile name after a leading +.
func (s *Server) profileMention(ctx context.Context, text string) (*Profile, error) {
	if !strings.HasPrefix(text, "+") {
		return nil, nil
	}
	var rows []generated.Profile
	if err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		rows, err = q.ListProfiles(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	var match *generated.Profile
	tail := []rune(text[1:])
	for i := range rows {
		name := []rune(rows[i].Name)
		if len(tail) <= len(name) || !unicode.IsSpace(tail[len(name)]) ||
			!strings.EqualFold(string(tail[:len(name)]), rows[i].Name) ||
			strings.TrimSpace(string(tail[len(name):])) == "" {
			continue
		}
		if match == nil || len(rows[i].Name) > len(match.Name) {
			match = &rows[i]
		}
	}
	if match == nil {
		return nil, nil
	}
	if _, err := s.db.GetProject(ctx, match.Name); err == nil {
		return nil, invalidSettings("project profiles cannot be used for one turn")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	profile, err := profileFromRow(*match)
	if err != nil {
		return nil, err
	}
	models := s.getModelList()
	if profile.Model == "" {
		profile.Model = s.effectiveDefaultModel(models)
	}
	if problem := profile.Settings.validate(models, true); problem != "" {
		return nil, invalidSettings(problem)
	}
	return &profile, nil
}

// prepareProfileTurn leaves the conversation settings and stored prompt alone.
func (cm *ConversationManager) prepareProfileTurn(ctx context.Context, message llm.Message, userData []byte) (loop.TurnRunner, error) {
	if cm.profileMention == nil || !typedUserForLLM(message, userData) {
		return nil, nil
	}
	profile, err := cm.profileMention(ctx, messageText(message))
	if err != nil || profile == nil {
		return nil, err
	}
	service, err := cm.profileModelService(profile.Model)
	if err != nil {
		return nil, err
	}
	service, err = cm.serviceForLoop(service)
	if err != nil {
		return nil, err
	}
	cm.mu.Lock()
	opts := cm.conversationOptions
	cfg := cm.turnToolConfig
	returnTo := opts.Profile
	cm.mu.Unlock()
	opts.SystemPrompt = profile.SystemPrompt
	prompt, err := cm.renderSystemPrompt(ctx, opts)
	if err != nil {
		return nil, err
	}
	cfg.ModelID, cfg.Env.Model = profile.Model, profile.Model
	cfg.ToolOverrides = profile.ToolOverrides
	cfg.ReasoningLevel = profile.ThinkingLevel
	return func(ctx context.Context, turn loop.RunConfig) error {
		cm.mu.Lock()
		cfg.WorkingDir = cm.cwd
		cm.mu.Unlock()
		tools := claudetool.NewToolSet(ctx, cfg)
		defer tools.Cleanup()
		turn.LLM, turn.ModelID = service, profile.Model
		turn.Tools = tools.Tools()
		turn.System = []llm.SystemContent{{Text: prompt.text}}
		turn.CurrentSystem = nil
		turn.ThinkingLevel = llm.ParseThinkingLevel(profile.ThinkingLevel)
		// Keep the conversation's cache warmer on its own model and prompt.
		turn.Hooks.OnSuccessfulRequest = nil
		marker, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: cm.conversationID,
			Type:           db.MessageTypeProfileTurn, ExcludedFromContext: true,
			UserData: map[string]string{
				"profile_to": profile.Name, "profile_return_to": returnTo,
				"profile_mention_phase": "start",
			},
		})
		if err != nil {
			recordErr := turn.Hooks.OnResponse(ctx, loop.Response{Message: llm.Message{
				Role: llm.MessageRoleAssistant, EndOfTurn: true, ErrorType: llm.ErrorTypeLLMRequest,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: fmt.Sprintf("Profile turn failed: %v", err)}},
			}})
			return errors.Join(err, recordErr)
		}
		cm.publishStream(marker.SequenceID, StreamResponse{Messages: toAPIMessages([]generated.Message{*marker})})
		return loop.Run(ctx, turn)
	}, nil
}
