package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

type fakeSlackAPI struct {
	posted []struct{ channel, threadTS, text string }
}

func (f *fakeSlackAPI) PostMessage(channel, threadTS, text string) error {
	f.posted = append(f.posted, struct{ channel, threadTS, text string }{channel, threadTS, text})
	return nil
}

func (f *fakeSlackAPI) ResolveChannel(_ context.Context, nameOrID string) (string, error) {
	// Simulate resolution: strip # and map known names
	switch nameOrID {
	case "C001", "C002":
		return nameOrID, nil
	case "general":
		return "C001", nil
	case "#general":
		return "C001", nil
	default:
		return "", fmt.Errorf("channel %q not found", nameOrID)
	}
}

func (f *fakeSlackAPI) GetHistory(_ context.Context, channel string, limit int) ([]SlackMessage, error) {
	return []SlackMessage{
		{User: "U123", Text: "hello", Timestamp: "1234567890.123456"},
		{User: "U456", Text: "world", Timestamp: "1234567891.123456"},
	}, nil
}

func (f *fakeSlackAPI) GetThread(_ context.Context, channel, threadTS string, limit int) ([]SlackMessage, error) {
	return []SlackMessage{
		{User: "U123", Text: "thread root", Timestamp: threadTS},
		{User: "U456", Text: "reply", Timestamp: "1234567891.123456", ThreadTS: threadTS},
	}, nil
}

func (f *fakeSlackAPI) ListChannels(_ context.Context) ([]SlackChannel, error) {
	return []SlackChannel{
		{ID: "C001", Name: "general"},
		{ID: "C002", Name: "random"},
	}, nil
}

func (f *fakeSlackAPI) AddReaction(_ context.Context, channel, timestamp, emoji string) error {
	if emoji == "fail" {
		return fmt.Errorf("reaction failed")
	}
	return nil
}

func TestSlackTool(t *testing.T) {
	api := &fakeSlackAPI{}
	tool := &SlackTool{API: api}
	ctx := context.Background()

	tests := []struct {
		name    string
		input   slackInput
		wantErr bool
	}{
		{"send_message", slackInput{Action: "send_message", Channel: "C001", Text: "hi"}, false},
		{"send_message_by_name", slackInput{Action: "send_message", Channel: "#general", Text: "hi"}, false},
		{"send_message_unknown_channel", slackInput{Action: "send_message", Channel: "#nonexistent", Text: "hi"}, true},
		{"send_message_no_channel", slackInput{Action: "send_message", Text: "hi"}, true},
		{"send_message_no_text", slackInput{Action: "send_message", Channel: "C001"}, true},
		{"get_history", slackInput{Action: "get_history", Channel: "C001"}, false},
		{"get_history_no_channel", slackInput{Action: "get_history"}, true},
		{"get_thread", slackInput{Action: "get_thread", Channel: "C001", ThreadTS: "123.456"}, false},
		{"get_thread_no_thread_ts", slackInput{Action: "get_thread", Channel: "C001"}, true},
		{"list_channels", slackInput{Action: "list_channels"}, false},
		{"add_reaction", slackInput{Action: "add_reaction", Channel: "C001", Timestamp: "123.456", Emoji: "thumbsup"}, false},
		{"add_reaction_fail", slackInput{Action: "add_reaction", Channel: "C001", Timestamp: "123.456", Emoji: "fail"}, true},
		{"unknown_action", slackInput{Action: "bogus"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, _ := json.Marshal(tt.input)
			out := tool.Run(ctx, inputJSON)
			isErr := out.Error != nil
			if isErr != tt.wantErr {
				t.Errorf("IsError = %v, want %v; content: %+v", isErr, tt.wantErr, out.LLMContent)
			}
		})
	}

	// Verify send_message actually called the API (2 successful sends: by ID and by name)
	if len(api.posted) != 2 {
		t.Fatalf("expected 2 posted messages, got %d", len(api.posted))
	}
	if api.posted[0].text != "hi" {
		t.Errorf("posted text = %q, want %q", api.posted[0].text, "hi")
	}
	// Channel name should have been resolved to ID
	if api.posted[1].channel != "C001" {
		t.Errorf("resolved channel = %q, want %q", api.posted[1].channel, "C001")
	}
}
