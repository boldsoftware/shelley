package db

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"shelley.exe.dev/llm"
)

func TestConversationThemes(t *testing.T) {
	database, cleanup := NewTestDB(t)
	t.Cleanup(cleanup)
	ctx := t.Context()
	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID
	empty, err := database.GetConversationThemes(ctx, id)
	if err != nil || empty.Chapters == nil || len(empty.Chapters) != 0 {
		t.Fatalf("unseeded themes = %+v, %v", empty, err)
	}
	var ids []string
	for range 3 {
		m, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: id, Type: MessageTypeUser, LLMData: llm.UserStringMessage("hello"),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.MessageID)
	}
	themes := ConversationThemes{
		Chapters:         []ConversationChapter{{Title: "First idea", MessageID: ids[0]}, {Title: "Changed direction", MessageID: ids[1]}},
		ThroughMessageID: ids[2],
	}
	if err := database.SetConversationThemes(ctx, id, themes); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetConversationThemes(ctx, id)
	if err != nil || !reflect.DeepEqual(got, themes) {
		t.Fatalf("themes = %+v, %v; want %+v", got, err, themes)
	}
	themes.Chapters[1].Title = "Settled on a direction"
	if err := database.SetConversationThemes(ctx, id, themes); err != nil {
		t.Fatal(err)
	}
	got, err = database.GetConversationThemes(ctx, id)
	if err != nil || !reflect.DeepEqual(got, themes) {
		t.Fatalf("updated themes = %+v, %v", got, err)
	}
	for _, invalid := range []ConversationThemes{
		{ThroughMessageID: ids[2]},
		{Chapters: []ConversationChapter{{Title: "", MessageID: ids[0]}}, ThroughMessageID: ids[2]},
		{Chapters: []ConversationChapter{themes.Chapters[1], themes.Chapters[0]}, ThroughMessageID: ids[2]},
		{Chapters: themes.Chapters, ThroughMessageID: ids[0]},
	} {
		if err := database.SetConversationThemes(ctx, id, invalid); err == nil {
			t.Fatalf("accepted invalid themes: %+v", invalid)
		}
	}
	other, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetConversationThemes(ctx, other.ConversationID, themes); err == nil {
		t.Fatal("accepted anchors from another conversation")
	}
	if err := database.DeleteConversation(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetConversationThemes(ctx, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted conversation: %v", err)
	}
}
