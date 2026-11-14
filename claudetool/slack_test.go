package claudetool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type failingSlack struct{}

func (failingSlack) Send(context.Context, SlackPost) (string, error) {
	return "", errors.New("refused")
}

func (failingSlack) React(context.Context, string, string, string) error {
	return errors.New("refused")
}

// A call Slack fails is marked chat_failed, for brief view; a mistake in
// the call is not.
func TestSlackFailuresAreChatFailures(t *testing.T) {
	tools := map[string]func(context.Context, json.RawMessage) any{}
	for _, tool := range SlackTools(failingSlack{}, NewMutableWorkingDir(t.TempDir())) {
		tools[tool.Name] = func(ctx context.Context, in json.RawMessage) any {
			out := tool.Run(ctx, in)
			if out.Error == nil {
				t.Fatalf("%s %s: no error", tool.Name, in)
			}
			return out.Display
		}
	}
	for _, tc := range []struct {
		tool, input string
		want        any
	}{
		{SendToThreadName, `{"channel":"C1","thread_ts":"1.0","text":"hi"}`, SlackDisplay{ChatFailed: true}},
		{SendToChannelName, `{"channel":"C1","text":"hi"}`, SlackDisplay{ChatFailed: true}},
		{AddReactionName, `{"channel":"C1","ts":"1.0","emoji":"eyes"}`, SlackDisplay{ChatFailed: true}},
		{SendToThreadName, `{"channel":"C1","text":"hi"}`, nil},
		{AddReactionName, `{"channel":"C1","ts":"1.0"}`, nil},
	} {
		if got := tools[tc.tool](t.Context(), json.RawMessage(tc.input)); got != tc.want {
			t.Errorf("%s %s: display %#v, want %#v", tc.tool, tc.input, got, tc.want)
		}
	}
}
