package claudetool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestIsEmoji(t *testing.T) {
	for _, s := range []string{"👍", "❤️", "1️⃣", "🇫🇷", "👩🏽‍💻", "✅", "🎉", "‼️", "⁉️", "〽️"} {
		if !isEmoji(s) {
			t.Errorf("isEmoji(%q) = false", s)
		}
	}
	for _, s := range []string{"", "ok", "1", "👍👍", "…", " 👍x", ":+1:", "->", "\uFE0F", "a\uFE0F", ".\uFE0F", "!"} {
		if isEmoji(s) {
			t.Errorf("isEmoji(%q) = true", s)
		}
	}
}

type noMessages struct{}

func (noMessages) FindUserMessageBySequence(context.Context, int64) (UserMessage, bool, error) {
	return UserMessage{}, false, nil
}

func TestMessageUserReactionWithoutReplyToRefused(t *testing.T) {
	for _, in := range []MessageUserInput{
		{Reaction: "👍"},
		{Text: "hi", ReplyTo: -1, EndTurn: true},
	} {
		if out := runMessageUser(context.Background(), noMessages{}, nil, t.TempDir(), in); out.Error == nil {
			t.Errorf("%+v: sent", in)
		}
	}
}

// fakeChat records what it is sent, refusing messages with sendErr and
// reactions with reactErr.
type fakeChat struct {
	sent              []string
	sendErr, reactErr error
}

func (c *fakeChat) Send(_ context.Context, text string, target UserMessage) error {
	if c.sendErr != nil {
		return c.sendErr
	}
	c.sent = append(c.sent, "send "+text+" reply-to "+target.ExternalID)
	return nil
}

func (c *fakeChat) React(_ context.Context, target UserMessage, emoji string) error {
	if c.reactErr != nil {
		return c.reactErr
	}
	c.sent = append(c.sent, "react "+emoji+" "+target.ExternalID)
	return nil
}

type oneMessage struct{ UserMessage }

func (f oneMessage) FindUserMessageBySequence(_ context.Context, sequenceID int64) (UserMessage, bool, error) {
	return f.UserMessage, f.SequenceID == sequenceID, nil
}

func TestMessageUserStripsChatCitations(t *testing.T) {
	for _, tt := range []struct {
		name, text, want string
	}{
		{"cited prose", " \ue203It works.\ue204 \ue200cite\ue202turn0search0\ue201 ", "It works."},
		{"plain text", "Résumé 👍", "Résumé 👍"},
		{"citations only", " \ue200cite\ue202turn0search0\ue201 ", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			chat := &fakeChat{}
			out := runMessageUser(t.Context(), noMessages{}, chat, t.TempDir(), MessageUserInput{Text: tt.text, EndTurn: true})
			if tt.want == "" {
				if out.Error == nil || len(chat.sent) != 0 || out.EndTurn {
					t.Fatalf("empty message: %+v, sent %q", out, chat.sent)
				}
				return
			}
			if out.Error != nil || !out.EndTurn {
				t.Fatalf("send: %+v", out)
			}
			if want := []string{"send " + tt.want + " reply-to "}; !slices.Equal(chat.sent, want) {
				t.Fatalf("sent %q, want %q", chat.sent, want)
			}
		})
	}
}

// The chat gets the message, then the reaction. A refused message is an
// error, so the agent learns it was not delivered; a reaction refused after
// its message went out leaves the call standing, with a note.
func TestMessageUserSendsToChat(t *testing.T) {
	finder := oneMessage{UserMessage{ID: "m1", SequenceID: 17, Text: "hello there", ExternalID: "ext-1"}}
	reply := MessageUserInput{Text: "hi", ReplyTo: 17, Reaction: "👍"}
	chat := &fakeChat{}
	out := runMessageUser(t.Context(), finder, chat, t.TempDir(), reply)
	if out.Error != nil {
		t.Fatal(out.Error)
	}
	if want := []string{"send hi reply-to ext-1", "react 👍 ext-1"}; !slices.Equal(chat.sent, want) {
		t.Fatalf("sent %q, want %q", chat.sent, want)
	}
	out = runMessageUser(t.Context(), finder, chat, t.TempDir(), MessageUserInput{Text: "by sequence", ReplyTo: 17})
	if out.Error != nil || chat.sent[len(chat.sent)-1] != "send by sequence reply-to ext-1" {
		t.Fatalf("sequence reply: %+v, sent %q", out, chat.sent)
	}
	for _, in := range []MessageUserInput{
		{Text: "bad", ReplyTo: 16},
		{Text: "bad", ReplyTo: -1},
	} {
		if got := runMessageUser(t.Context(), finder, chat, t.TempDir(), in); got.Error == nil {
			t.Fatalf("invalid reply target %+v succeeded", in)
		}
	}
	out = runMessageUser(t.Context(), finder, chat, t.TempDir(), MessageUserInput{Text: "plain"})
	if out.Error != nil || chat.sent[len(chat.sent)-1] != "send plain reply-to " {
		t.Fatalf("plain message: %+v, sent %q", out, chat.sent)
	}
	uiFinder := oneMessage{UserMessage{ID: "ui-1", SequenceID: 19, Text: "from the UI"}}
	out = runMessageUser(t.Context(), uiFinder, chat, t.TempDir(), MessageUserInput{Text: "UI reply", ReplyTo: 19})
	if out.Error != nil || chat.sent[len(chat.sent)-1] != "send UI reply reply-to " {
		t.Fatalf("UI reply: %+v, sent %q", out, chat.sent)
	}

	chat = &fakeChat{sendErr: errors.New("opted_out")}
	out = runMessageUser(t.Context(), finder, chat, t.TempDir(), reply)
	if out.Error == nil || !strings.Contains(out.Error.Error(), "opted_out") || len(chat.sent) != 0 {
		t.Fatalf("refused send: %v, sent %q", out.Error, chat.sent)
	}
	if d, _ := out.Display.(MessageUserDisplay); !d.ChatFailed {
		t.Fatalf("refused send is not marked for brief view: %+v", out.Display)
	}

	chat = &fakeChat{reactErr: errors.New("line_paused")}
	out = runMessageUser(t.Context(), finder, chat, t.TempDir(), reply)
	if out.Error != nil || out.Display == nil || !strings.Contains(out.LLMContent[0].Text, "line_paused") || strings.Contains(out.LLMContent[0].Text, "reacted") {
		t.Fatalf("refused reaction after message: %+v", out)
	}
	out = runMessageUser(t.Context(), finder, chat, t.TempDir(), MessageUserInput{ReplyTo: 17, Reaction: "👍"})
	if out.Error == nil || !strings.Contains(out.Error.Error(), "line_paused") {
		t.Fatalf("refused reaction: %v", out.Error)
	}

	// Mistakes in the call reach no chat, and are not the chat's failures.
	chat = &fakeChat{}
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, in := range []MessageUserInput{
		{Text: "hi", ReplyTo: 999},
		{Text: "see attached", Attachments: []string{file}},
	} {
		out = runMessageUser(t.Context(), finder, chat, t.TempDir(), in)
		if out.Error == nil || out.Display != nil || len(chat.sent) != 0 {
			t.Fatalf("%+v: %+v, sent %q", in, out, chat.sent)
		}
	}

	// ‼️ is a tapback, though ‼ is punctuation.
	out = runMessageUser(t.Context(), finder, chat, t.TempDir(), MessageUserInput{ReplyTo: 17, Reaction: "‼️"})
	if out.Error != nil || !slices.Equal(chat.sent, []string{"react ‼️ ext-1"}) {
		t.Fatalf("‼️: %v, sent %q", out.Error, chat.sent)
	}
	tool := MessageUserTool(finder, chat, NewMutableWorkingDir(t.TempDir()))
	if !tool.Sequential {
		t.Fatal("message_user is not sequential, so the chat could get messages out of order")
	}
	if !strings.Contains(tool.Description, "phone") || strings.Contains(tool.Description, "Attach files") {
		t.Fatalf("description for a chat: %s", tool.Description)
	}
	if MessageUserTool(finder, nil, NewMutableWorkingDir(t.TempDir())).Description != messageUserDescription {
		t.Fatal("description without a chat changed")
	}
}
