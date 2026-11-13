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

func (s *Server) handleConversationThemesIndex(w http.ResponseWriter, r *http.Request) {
	on, err := s.flagOn(r.Context(), FlagConversationChapters)
	if err != nil {
		s.internalError(w, "Failed to read conversation chapters flag", err)
		return
	}
	if !on {
		http.Error(w, "The conversation-chapters feature flag is off", http.StatusForbidden)
		return
	}
	themes, err := s.db.ListConversationThemes(r.Context())
	if err != nil {
		s.logger.Error("failed to load conversation themes index", "error", err)
		http.Error(w, "Failed to load conversation themes", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, themes)
}
