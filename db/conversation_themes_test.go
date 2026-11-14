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
	checkpoint := ConversationThemes{Chapters: []ConversationChapter{}, ThroughMessageID: ids[2]}
	if err := database.SetConversationThemes(ctx, id, checkpoint); err != nil {
		t.Fatal(err)
	}
	if got, err := database.GetConversationThemes(ctx, id); err != nil || !reflect.DeepEqual(got, checkpoint) {
		t.Fatalf("empty chapter checkpoint = %+v, %v; want %+v", got, err, checkpoint)
	}
	for _, invalid := range []ConversationThemes{
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

func TestConversationThemesIndex(t *testing.T) {
	database, cleanup := NewTestDB(t)
	t.Cleanup(cleanup)
	ctx := t.Context()
	createWithThemes := func(opts ConversationOptions) string {
		t.Helper()
		conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		message, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conversation.ConversationID, Type: MessageTypeUser,
			LLMData: llm.UserStringMessage("hello"),
		})
		if err != nil {
			t.Fatal(err)
		}
		themes := ConversationThemes{
			Chapters:         []ConversationChapter{{Title: "A chapter", MessageID: message.MessageID}},
			ThroughMessageID: message.MessageID,
		}
		if err := database.SetConversationThemes(ctx, conversation.ConversationID, themes); err != nil {
			t.Fatal(err)
		}
		return conversation.ConversationID
	}

	empty, err := database.ListConversationThemes(ctx)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty index = %#v, %v", empty, err)
	}

	active := createWithThemes(ConversationOptions{})
	archived := createWithThemes(ConversationOptions{})
	if _, err := database.ArchiveConversation(ctx, archived); err != nil {
		t.Fatal(err)
	}
	search := createWithThemes(ConversationOptions{Kind: "search"})
	draft, err := database.CreateDraftConversation(ctx, nil, nil, ConversationOptions{}, "draft body")
	if err != nil {
		t.Fatal(err)
	}
	if !draft.IsDraft {
		t.Fatal("draft fixture was not created as a draft")
	}
	draftMsg, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: draft.ConversationID, Type: MessageTypeUser,
		LLMData: llm.UserStringMessage("hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetConversationThemes(ctx, draft.ConversationID, ConversationThemes{
		Chapters:         []ConversationChapter{{Title: "Draft chapter", MessageID: draftMsg.MessageID}},
		ThroughMessageID: draftMsg.MessageID,
	}); err != nil {
		t.Fatal(err)
	}
	parent := createWithThemes(ConversationOptions{})
	subagent, err := database.CreateSubagentConversation(ctx, "child", parent, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Add stored chapters to the subagent too; it must still be excluded.
	subagentMsg, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: subagent.ConversationID, Type: MessageTypeUser,
		LLMData: llm.UserStringMessage("hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetConversationThemes(ctx, subagent.ConversationID, ConversationThemes{
		Chapters:         []ConversationChapter{{Title: "Child chapter", MessageID: subagentMsg.MessageID}},
		ThroughMessageID: subagentMsg.MessageID,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := database.ListConversationThemes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{active, archived, parent}
	if len(got) != len(wantIDs) {
		t.Fatalf("index = %#v, want only active, archived, and ordinary top-level conversations (excluded search %s, draft %s, subagent %s)",
			got, search, draft.ConversationID, subagent.ConversationID)
	}
	for _, id := range wantIDs {
		if chapters, ok := got[id]; !ok || len(chapters) != 1 || chapters[0].Title != "A chapter" || chapters[0].MessageID == "" {
			t.Errorf("index[%q] = %#v, want one stored chapter", id, chapters)
		}
	}
}
