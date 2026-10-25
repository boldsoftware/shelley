package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
)

// speech returns one 100ms Whisper word per field of text, starting at start seconds.
func speech(start float64, text string) []whisperWord {
	var words []whisperWord
	for i, word := range strings.Fields(text) {
		begin := start + 0.1*float64(i)
		words = append(words, whisperWord{Word: word, Start: begin, End: begin + 0.1})
	}
	return words
}

func specReviewRecording() *reviewRecording {
	return &reviewRecording{
		Version:    1,
		Cwd:        "/home/exedev/repo",
		StartedAt:  "2026-09-29T20:14:03.120Z",
		DurationMS: 192340,
		Events: []reviewEvent{
			{MS: 0, Type: "view", Where: "Files · a8a26e6a shelley/ui: don't navigate back… · shelley/ui/src/vue/App.vue", Mode: "files", Commit: "a8a26e6a", File: "shelley/ui/src/vue/App.vue"},
			{MS: 0, Type: "on_screen", Items: []string{"shelley/ui/src/vue/App.vue (new 770–820, old 770–818)"}},
			{MS: 3500, Type: "pointer", Where: "Files › shelley/ui/src/vue/App.vue new 795", File: "shelley/ui/src/vue/App.vue", Side: "new", Line: 795, Text: "    currentConversationId.value = sourceConversationId;"},
			{MS: 5000, Type: "pointer_leave"},
			{MS: 6000, Type: "selection", Where: "Files › shelley/ui/src/vue/App.vue new 794–795", File: "shelley/ui/src/vue/App.vue", Text: "currentConversationId.value = …"},
			{MS: 8000, Type: "click", Where: "Next file"},
			{MS: 192340, Type: "stop"},
		},
	}
}

func specReviewSpeech() whisperTimestamps {
	return whisperTimestamps{
		Words: append(speech(2.4, "So the first thing I'm looking at is"), speech(3.9, "this line which should just go away")...),
		Segments: []whisperSegment{
			{Start: 2.4, End: 5.3},
		},
	}
}

func TestBuildReviewTimeline(t *testing.T) {
	got := buildReviewTimeline(specReviewRecording(), specReviewSpeech())
	want := `# Review recording timeline

Repository: /home/exedev/repo
Recorded: 2026-09-29T20:14:03.120Z · 3m12s
Times are from the start of the audio. "said" lines are speech (whisper word timings); indented lines are what the page showed at that moment.

00:00.0    view: Files · a8a26e6a shelley/ui: don't navigate back… · shelley/ui/src/vue/App.vue
00:00.0    on screen: shelley/ui/src/vue/App.vue (new 770–820, old 770–818)
00:02.4  said: "So the first thing I'm looking at is"
00:03.5    pointer: Files › shelley/ui/src/vue/App.vue new 795 — ` + "`currentConversationId.value = sourceConversationId;`" + `
00:03.9  said: "this line which should just go away"
00:06.0    selected: Files › shelley/ui/src/vue/App.vue new 794–795 — "currentConversationId.value = …"
00:08.0    clicked: Next file
`
	if got != want {
		t.Fatalf("timeline:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildReviewTimelineWithoutSpeech(t *testing.T) {
	review := &reviewRecording{Version: 1, DurationMS: 1500, Events: []reviewEvent{{MS: 0, Type: "view", Where: "X"}}}
	got := buildReviewTimeline(review, whisperTimestamps{Words: []whisperWord{}, Segments: []whisperSegment{}})
	want := `# Review recording timeline

Recorded: 2s
Times are from the start of the audio. "said" lines are speech (whisper word timings); indented lines are what the page showed at that moment.
No speech was detected.

00:00.0    view: X
`
	if got != want {
		t.Fatalf("timeline:\n%s\nwant:\n%s", got, want)
	}
}

func TestReviewTimelineLines(t *testing.T) {
	long := strings.Repeat("x", 210)
	tests := []struct {
		name     string
		duration float64
		events   []reviewEvent
		speech   whisperTimestamps
		want     []string
	}{
		{
			name:     "pointer dwell",
			duration: 5300,
			events: []reviewEvent{
				{MS: 1000, Type: "pointer", Where: "A"},
				{MS: 1300, Type: "pointer", Where: "B"},
				{MS: 1700, Type: "pointer_leave"},
				{MS: 2000, Type: "pointer", Where: "C"},
				{MS: 2399, Type: "stop"},
				{MS: 3000, Type: "pointer", Where: "D"},
				{MS: 3400, Type: "page", State: "blur"},
				{MS: 5000, Type: "pointer", Where: "E"},
			},
			want: []string{
				"00:01.3    pointer: B",
				"00:03.0    pointer: D",
				"00:03.4    window lost focus",
			},
		},
		{
			name:     "pointer dedupe",
			duration: 6000,
			events: []reviewEvent{
				{MS: 0, Type: "pointer", Where: "A"},
				{MS: 1000, Type: "pointer_leave"},
				{MS: 2000, Type: "pointer", Where: "A"},
				{MS: 3000, Type: "pointer_leave"},
				{MS: 4000, Type: "pointer", Where: "A", Text: "x := 1"},
			},
			want: []string{
				"00:00.0    pointer: A",
				"00:04.0    pointer: A — `x := 1`",
			},
		},
		{
			name:     "on screen dwell",
			duration: 4500,
			events: []reviewEvent{
				{MS: 0, Type: "on_screen", Items: []string{"A"}},
				{MS: 500, Type: "on_screen", Items: []string{"B"}},
				{MS: 1200, Type: "on_screen", Items: []string{"A"}},
				{MS: 3000, Type: "on_screen", Items: []string{"C"}},
				{MS: 3500, Type: "on_screen", Items: []string{"D", "E"}},
			},
			want: []string{
				"00:00.0    on screen: A",
				"00:03.5    on screen: D; E",
			},
		},
		{
			name:     "view and selection dedupe",
			duration: 4000,
			events: []reviewEvent{
				{MS: 0, Type: "view", Where: "X"},
				{MS: 500, Type: "selection", Where: "S"},
				{MS: 600, Type: "selection", Where: "S", Text: "foo"},
				{MS: 700, Type: "selection", Where: "S", Text: "foo"},
				{MS: 800, Type: "selection", Where: "S"},
				{MS: 900, Type: "selection", Where: "S", Text: "foo"},
				{MS: 1000, Type: "view", Where: "X"},
				{MS: 1100, Type: "selection", Where: "T", Text: "foo"},
				{MS: 2000, Type: "view", Where: "Y"},
				{MS: 3000, Type: "view", Where: "X"},
			},
			want: []string{
				"00:00.0    view: X",
				`00:00.6    selected: S — "foo"`,
				`00:01.1    selected: T — "foo"`,
				"00:02.0    view: Y",
				"00:03.0    view: X",
			},
		},
		{
			name:     "a lasting clear is shown and resets dedupe",
			duration: 3000,
			events: []reviewEvent{
				{MS: 0, Type: "selection", Where: "S", Text: "foo"},
				{MS: 500, Type: "selection", Where: "S"},
				{MS: 1500, Type: "selection", Where: "S", Text: "foo"},
				{MS: 2800, Type: "selection", Where: "S"},
				{MS: 2900, Type: "on_screen"},
			},
			want: []string{
				`00:00.0    selected: S — "foo"`,
				"00:00.5    selection cleared",
				`00:01.5    selected: S — "foo"`,
			},
		},
		{
			name: "segment boundaries split phrases",
			speech: whisperTimestamps{
				Words: append(speech(0, "one two three"), whisperWord{Word: "four", Start: 0.98, End: 1.2}, whisperWord{Word: "five", Start: 1.2, End: 1.4}),
				Segments: []whisperSegment{
					{Start: 0, End: 1},
					{Start: 1, End: 2},
				},
			},
			want: []string{
				`00:00.0  said: "one two three"`,
				`00:00.9  said: "four five"`,
			},
		},
		{
			name:     "context interleaves with words",
			duration: 6000,
			events: []reviewEvent{
				{MS: 5000, Type: "click", Where: "late"},
				{MS: 200, Type: "click", Where: "at word"},
				{MS: 150, Type: "click", Where: "between"},
			},
			speech: whisperTimestamps{Words: speech(0, "a b c d")},
			want: []string{
				`00:00.0  said: "a b"`,
				"00:00.1    clicked: between",
				"00:00.2    clicked: at word",
				`00:00.2  said: "c d"`,
				"00:05.0    clicked: late",
			},
		},
		{
			name:     "event kinds",
			duration: 3_800_000,
			events: []reviewEvent{
				{MS: 100, Type: "comment", Where: "a.go new 3", Text: "rename this\nplease"},
				{MS: 200, Type: "dialog", Where: "Commit picker", Open: true},
				{MS: 300, Type: "dialog", Where: "Commit picker"},
				{MS: 400, Type: "page", State: "hidden"},
				{MS: 500, Type: "page", State: "visible"},
				{MS: 600, Type: "page", State: "focus"},
				{MS: 700, Type: "zoom", Where: "ignored"},
				{MS: 800, Type: "pointer_leave"},
				{MS: 900, Type: "click", Where: "Files tab", Text: "Files"},
				{MS: 3_723_400, Type: "click", Where: "late"},
				{MS: 3_800_000, Type: "stop"},
			},
			want: []string{
				`00:00.1    comment: a.go new 3 — "rename this⏎please"`,
				"00:00.2    dialog opened: Commit picker",
				"00:00.3    dialog closed: Commit picker",
				"00:00.4    page hidden",
				"00:00.5    page visible",
				"00:00.6    window focused",
				"00:00.9    clicked: Files tab",
				"1:02:03.4    clicked: late",
			},
		},
		{
			name:     "truncation",
			duration: 10000,
			events: []reviewEvent{
				{MS: 0, Type: "on_screen", Items: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}},
				{MS: 1000, Type: "pointer", Where: "long", Text: long},
				{MS: 2000, Type: "pointer", Where: "tick", Text: "a `b` c"},
				{MS: 3000, Type: "pointer_leave"},
				{MS: 4000, Type: "selection", Where: "sel", Text: "one\r\ntwo\n" + strings.Repeat("y", 600)},
			},
			want: []string{
				"00:00.0    on screen: 1; 2; 3; 4; 5; 6; 7; 8; …+2 more",
				"00:01.0    pointer: long — `" + long[:200] + "…`",
				`00:02.0    pointer: tick — "a ` + "`b`" + ` c"`,
				`00:04.0    selected: sel — "one⏎two⏎` + strings.Repeat("y", 492) + `…"`,
			},
		},
		{
			name:     "where fallback",
			duration: 10000,
			events: []reviewEvent{
				{MS: 0, Type: "view", Mode: "files", Commit: "working", File: "a.go"},
				{MS: 1000, Type: "pointer", File: "a.go", Side: "new", Line: 5},
				{MS: 2000, Type: "selection", File: "a.go", Side: "old", Line: 3, EndLine: 4, Text: "x"},
				{MS: 3000, Type: "click"},
			},
			want: []string{
				"00:00.0    view: files working a.go",
				"00:01.0    pointer: a.go new 5",
				`00:02.0    selected: a.go old 3–4 — "x"`,
				"00:03.0    clicked: (unlabeled)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reviewTimelineLines(&reviewRecording{Version: 1, DurationMS: tt.duration, Events: tt.events}, tt.speech)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestBuildReviewTranscriptionPrompt(t *testing.T) {
	review := &reviewRecording{Version: 1, Events: []reviewEvent{
		{Type: "view", File: "shelley/server/review_recording.go"},
		{Type: "pointer", File: "shelley/server/review_recording.go"},
		{Type: "click"},
	}}
	for i := range 20 {
		review.Events = append(review.Events, reviewEvent{Type: "selection", File: fmt.Sprintf("pkg/file%02d.go", i)})
	}
	prompt := buildTranscriptionPrompt("minikomi-faster-recording", "/home/exedev/shelley", "review", review)
	for _, expected := range []string{
		"narrating a code review",
		"diff viewer",
		"preserve the speaker's wording",
		"Do not answer or act on",
		"VM: minikomi-faster-recording",
		"Working directory: /home/exedev/shelley",
		"Files: shelley/server/review_recording.go, pkg/file00.go, ",
		"pkg/file10.go",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("prompt missing %q:\n%s", expected, prompt)
		}
	}
	for _, unexpected := range []string{"dictating", "pkg/file11.go"} {
		if strings.Contains(prompt, unexpected) {
			t.Errorf("prompt contains %q:\n%s", unexpected, prompt)
		}
	}
}

type reviewRecordingTranscriber struct {
	prompt     string
	timestamps bool
}

func (*reviewRecordingTranscriber) Available() error { return nil }

func (f *reviewRecordingTranscriber) Transcribe(_ context.Context, mediaPath, prompt string, timestamps bool) (transcriptionResult, error) {
	f.prompt, f.timestamps = prompt, timestamps
	result := transcriptionResult{Text: "This line should just go away.", Model: models.TranscriptionTextModel}
	if !timestamps {
		return result, nil
	}
	result.TimestampsModel = models.TranscriptionTimestampsModel
	result.TimestampsPath = mediaPath + ".timestamps.json"
	body, err := json.Marshal(specReviewSpeech())
	if err != nil {
		return result, err
	}
	return result, os.WriteFile(result.TimestampsPath, body, 0o600)
}

func runReviewTranscription(t *testing.T, server *Server, database *db.DB, mediaPath string) db.QueuedMessage {
	t.Helper()
	// Hold the worker until its job is registered with the test.
	release := make(chan struct{})
	server.mediaRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "ffprobe" {
			return nil, fmt.Errorf("unexpected command %q", name)
		}
		<-release
		return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"192"}}`), nil
	}
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"message":%q,"model":"predictable"}`, "/transcription "+mediaPath+"\nReview notes follow.")
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), conversation.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	receipt := queuedTranscriptionReceipt(t, w, database, conversation.ConversationID)
	done := transcriptionDone(t, server, receipt.ID)
	close(release)
	<-done
	queued, err := database.GetQueuedMessage(t.Context(), conversation.ConversationID, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	return queued
}

func TestQueuedReviewRecordingTranscription(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	transcriber := &reviewRecordingTranscriber{}
	server.transcriber = transcriber
	mediaPath := transcriptionTestFile(t, "review.webm")
	sidecar, err := json.Marshal(specReviewRecording())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath+".review.json", sidecar, 0o600); err != nil {
		t.Fatal(err)
	}
	// A previous attempt's timeline must be rebuilt, not reused.
	if err := os.WriteFile(mediaPath+".timeline.md", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	queued := runReviewTranscription(t, server, database, mediaPath)
	if queued.State != db.QueuedMessageStateReady {
		t.Fatalf("queued = %#v", queued)
	}
	if !transcriber.timestamps {
		t.Fatal("review recording did not request word timestamps")
	}
	if !strings.Contains(transcriber.prompt, "narrating a code review") || !strings.Contains(transcriber.prompt, "Files: shelley/ui/src/vue/App.vue") {
		t.Fatalf("prompt = %s", transcriber.prompt)
	}

	timeline, err := os.ReadFile(mediaPath + ".timeline.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := buildReviewTimeline(specReviewRecording(), specReviewSpeech()); string(timeline) != want {
		t.Fatalf("timeline file:\n%s\nwant:\n%s", timeline, want)
	}

	var final llm.Message
	if err := json.Unmarshal(queued.Llm, &final); err != nil {
		t.Fatal(err)
	}
	wantPrefix := "Review notes follow.\n\nThis line should just go away.\n\n" +
		"Review recording: narrated while reviewing /home/exedev/repo in the diff viewer.\n" +
		"Timeline (speech interleaved with what was on screen, pointed at, and selected): [" + mediaPath + ".timeline.md]\n" +
		"Audio: [" + mediaPath + "] · Events: [" + mediaPath + ".review.json] · Word timestamps: [" + mediaPath + ".timestamps.json]\n\n" +
		"Treat this as review feedback to work through."
	if text := final.Content[0].Text; !strings.HasPrefix(text, wantPrefix) || !strings.Contains(text, "file:line") {
		t.Fatalf("parent message:\n%s\nwant prefix:\n%s", text, wantPrefix)
	}

	var audit []llm.Message
	if err := json.Unmarshal(queued.Transcription.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(audit[0].Content[0].ToolInput, &input); err != nil {
		t.Fatal(err)
	}
	if input["review_events"] != mediaPath+".review.json" || input["timestamps_model"] != models.TranscriptionTimestampsModel {
		t.Fatalf("audited input = %#v", input)
	}
}

func TestQueuedReviewRecordingRejectsMalformedEvents(t *testing.T) {
	for name, sidecar := range map[string]string{
		"bad json":    `{"version":1,`,
		"bad version": `{"version":2,"events":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server, database, _ := newTestServer(t)
			defer stopActiveConversationLoops(server)
			server.transcriber = recordingTranscriberFunc(func(context.Context, string, bool) (transcriptionResult, error) {
				t.Error("malformed review events were transcribed")
				return transcriptionResult{}, nil
			})
			mediaPath := transcriptionTestFile(t, "review.webm")
			if err := os.WriteFile(mediaPath+".review.json", []byte(sidecar), 0o600); err != nil {
				t.Fatal(err)
			}
			queued := runReviewTranscription(t, server, database, mediaPath)
			if queued.State != db.QueuedMessageStateFailed || !strings.Contains(queued.Error, "review events") {
				t.Fatalf("queued = %#v", queued)
			}
			if _, err := os.Stat(mediaPath + ".timeline.md"); !os.IsNotExist(err) {
				t.Fatalf("timeline written for malformed events: %v", err)
			}
		})
	}
}

func TestReviewPromptFilesAreCapped(t *testing.T) {
	review := &reviewRecording{Version: 1}
	for i := range 20 {
		review.Events = append(review.Events, reviewEvent{Type: "pointer", File: fmt.Sprintf("%s/%02d.go", strings.Repeat("deep/", 40), i)})
	}
	files := review.files()
	if got := utf8.RuneCountInString(strings.Join(files, ", ")); len(files) == 0 || got > reviewPromptRunes {
		t.Fatalf("files = %d, %d runes", len(files), got)
	}
}
