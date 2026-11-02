package db

import (
	"fmt"
	"strings"
	"testing"
)

func TestSearchMessageContext(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := t.Context()
	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		id       string
		sequence int64
		text     string
	}
	var fixtures []fixture
	for i := range 30 {
		text := fmt.Sprintf("Visible message %d", i)
		contentType := 2
		if i == 15 {
			contentType = 6
		}
		if i == 16 {
			contentType = 3
		}
		if i == 17 {
			text = strings.Repeat("x", 6100)
		}
		var provenance any
		switch i {
		case 12:
			provenance = map[string]any{"sender_conversation_id": "child", "Text": text}
		case 13:
			provenance = map[string]any{"background_job_id": "job", "Text": text}
		case 14:
			provenance = map[string]any{"mcp_server_change": "added", "Text": text}
		case 18:
			provenance = map[string]any{"live_delivery_id": "human-delivery"}
		}
		message, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conversation.ConversationID,
			Type:           MessageTypeUser,
			LLMData:        map[string]any{"Content": []any{map[string]any{"Type": contentType, "Text": text}}},
			UserData:       provenance,
		})
		if err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, fixture{message.MessageID, message.SequenceID, text})
	}
	hit := fixtures[15]
	contexts, err := database.SearchMessageContext(ctx, []string{hit.id, hit.id, "missing"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]SearchMessageContext{}
	for _, item := range contexts {
		if item.HitID != hit.id || item.Speaker != "user" {
			t.Fatalf("wrong anchor or speaker: %+v", item)
		}
		if _, exists := got[item.MessageID]; exists {
			t.Fatalf("duplicate source: %s", item.MessageID)
		}
		got[item.MessageID] = item
	}
	for i, fixture := range fixtures {
		delta := fixture.sequence - hit.sequence
		want := delta >= -12 && delta <= 6 && (i < 12 || i > 16)
		item, exists := got[fixture.id]
		if exists != want {
			t.Fatalf("message %d included=%v, want %v", i, exists, want)
		}
		if !want {
			continue
		}
		if delta < 0 {
			delta = -delta
		}
		if item.Distance != delta || item.Text != fixture.text[:min(len(fixture.text), 6000)] {
			t.Fatalf("wrong excerpt or distance for message %d", i)
		}
	}
}
