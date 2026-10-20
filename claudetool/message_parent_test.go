package claudetool

import (
	"context"
	"encoding/json"
	"testing"
)

type parentMessengerFunc func(context.Context, string, string) error

func (f parentMessengerFunc) MessageParent(ctx context.Context, conversationID, text string, final bool) error {
	return f(ctx, conversationID, text)
}

func TestMessageParentEndTurn(t *testing.T) {
	var sent []string
	tool := (&MessageParentTool{
		ConversationID: "child",
		Messenger: parentMessengerFunc(func(_ context.Context, id, text string) error {
			if id != "child" {
				t.Fatalf("recipient %q, want child", id)
			}
			sent = append(sent, text)
			return nil
		}),
	}).Tool()

	for _, tc := range []struct {
		name    string
		input   string
		endTurn bool
		solo    bool
		wantErr bool
	}{
		{name: "progress", input: `{"text":"halfway","end_turn":false}`},
		{name: "final", input: `{"text":"done","end_turn":true}`, endTurn: true, solo: true},
		{name: "missing flag", input: `{"text":"ambiguous"}`, wantErr: true},
		{name: "empty text", input: `{"text":"  ","end_turn":true}`, solo: true, wantErr: true},
		{name: "bad boolean", input: `{"text":"bad","end_turn":"true"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tool.EndsTurnWhen(json.RawMessage(tc.input)); got != tc.solo {
				t.Fatalf("solo-call preflight = %v, want %v", got, tc.solo)
			}
			out := tool.Run(t.Context(), json.RawMessage(tc.input))
			if (out.Error != nil) != tc.wantErr {
				t.Fatalf("tool error = %v, wantErr=%v", out.Error, tc.wantErr)
			}
			if out.EndTurn != tc.endTurn {
				t.Fatalf("endTurn=%v, want %v", out.EndTurn, tc.endTurn)
			}
		})
	}
	if len(sent) != 2 || sent[0] != "halfway" || sent[1] != "done" {
		t.Fatalf("sent messages = %v, want progress and final", sent)
	}
}
