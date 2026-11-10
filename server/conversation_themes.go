package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"shelley.exe.dev/featureflags"
)

var FlagConversationChapters = featureflags.Register(featureflags.Flag{
	Name:        "conversation-chapters",
	Description: "Let the conversation's model mark chapters, shown in the sidebar and inline in the table of contents.",
	Default:     false,
})

func (cm *ConversationManager) chaptersOn(ctx context.Context) (bool, error) {
	if cm.role != roleTopLevel || cm.server == nil {
		return false, nil
	}
	return cm.server.flagOn(ctx, FlagConversationChapters)
}

func (cm *ConversationManager) chapterContext(ctx context.Context) (string, error) {
	on, err := cm.chaptersOn(ctx)
	if err != nil || !on {
		return "", err
	}
	themes, err := cm.db.GetConversationThemes(ctx, cm.conversationID)
	if err != nil {
		return "", err
	}
	if len(themes.Chapters) == 0 {
		return "There is no conversation chapter yet. Use chapter to record the user's first substantive goal; do not announce this bookkeeping.", nil
	}
	current := themes.Chapters[len(themes.Chapters)-1].Title
	return fmt.Sprintf("Current conversation chapter: %q.\nCompare the latest user's topic with this heading. Emit chapter for a meaningful new subtopic, question, decision, goal, or approach worth navigating to, even within the same overall task. Do not wait for a major goal change or for the user to ask. Skip routine implementation, progress updates, and tiny corrections. The three-user-message spacing brake still applies.", current), nil
}

func (cm *ConversationManager) chapterRecorder(ctx context.Context) (func(context.Context, string) (string, error), error) {
	on, err := cm.chaptersOn(ctx)
	if err != nil || !on {
		return nil, err
	}
	return func(ctx context.Context, title string) (string, error) {
		on, err := cm.chaptersOn(ctx)
		if err != nil {
			return "", err
		}
		if !on {
			return "", fmt.Errorf("conversation-chapters feature flag is off")
		}
		return cm.db.AppendConversationChapter(ctx, cm.conversationID, title)
	}, nil
}

func (s *Server) handleConversationThemes(w http.ResponseWriter, r *http.Request) {
	on, err := s.flagOn(r.Context(), FlagConversationChapters)
	if err != nil {
		s.internalError(w, "Failed to read conversation chapters flag", err)
		return
	}
	if !on {
		http.Error(w, "The conversation-chapters feature flag is off", http.StatusForbidden)
		return
	}
	themes, err := s.db.GetConversationThemes(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("failed to load conversation themes", "error", err)
		http.Error(w, "Failed to load conversation themes", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, themes)
}
