package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
)

type recordingTranscriberFunc func(context.Context, string, bool) (transcriptionResult, error)

func (recordingTranscriberFunc) Available() error { return nil }

func (f recordingTranscriberFunc) Transcribe(ctx context.Context, path, _ string, timestamps bool) (transcriptionResult, error) {
	return f(ctx, path, timestamps)
}

func successfulRecordingTranscriber(text string) recordingTranscriber {
	return recordingTranscriberFunc(func(_ context.Context, mediaPath string, timestamps bool) (transcriptionResult, error) {
		result := transcriptionResult{Text: text, Model: models.TranscriptionTextModel}
		if !timestamps {
			return result, nil
		}
		result.TimestampsModel = models.TranscriptionTimestampsModel
		result.TimestampsPath = mediaPath + ".timestamps.json"
		body, err := json.Marshal(speechTimings{
			Words:    speech(1.5, text),
			Segments: []timedSegment{{Start: 1.5, End: 3}},
		})
		if err != nil {
			return result, err
		}
		return result, os.WriteFile(result.TimestampsPath, body, 0o600)
	})
}

func TestTranscriptionToolResultAttributesModelFailure(t *testing.T) {
	now := time.Now()
	message, err := transcriptionToolResult(
		"transcription_test",
		transcriptionResult{},
		now,
		now,
		&transcriptionModelError{Model: models.TranscriptionTimestampsModel, Err: errors.New("failed")},
	)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(message.Content[0].ToolResult[0].Text), &output); err != nil {
		t.Fatal(err)
	}
	if output["model"] != models.TranscriptionTimestampsModel {
		t.Fatalf("output = %#v", output)
	}
}

type promptCapturingTranscriber struct {
	prompt chan string
	text   string
}

func (*promptCapturingTranscriber) Available() error { return nil }

func (t *promptCapturingTranscriber) Transcribe(_ context.Context, mediaPath, prompt string, timestamps bool) (transcriptionResult, error) {
	t.prompt <- prompt
	result := transcriptionResult{Text: t.text, Model: models.TranscriptionTextModel}
	if timestamps {
		result.TimestampsModel = models.TranscriptionTimestampsModel
		result.TimestampsPath = mediaPath + ".timestamps.json"
	}
	return result, nil
}

func transcriptionTestFile(t *testing.T, name string) string {
	t.Helper()
	if err := os.MkdirAll(browse.UploadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(browse.UploadDir, "transcription-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTranscriptionParentMessageOmitsWorkerMetadata(t *testing.T) {
	message := transcriptionParentMessage("", "Spoken words.")
	got := message.Content[0].Text
	want := "Spoken words."
	if got != want {
		t.Fatalf("parent message = %q, want %q", got, want)
	}
}

func TestParseTranscriptionCommand(t *testing.T) {
	tests := []struct {
		message string
		path    string
		context string
		ok      bool
	}{
		{"/transcription /tmp/shelley-uploads/a.webm", "/tmp/shelley-uploads/a.webm", "", true},
		{"  /transcription\t/tmp/a file.webm\nkeep this  ", "/tmp/a file.webm", "keep this", true},
		{"/transcription", "", "", true},
		{"/transcriptionx /tmp/a", "", "", false},
		{"please /transcription /tmp/a", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			path, context, ok := parseTranscriptionCommand(tt.message)
			if path != tt.path || context != tt.context || ok != tt.ok {
				t.Fatalf("parseTranscriptionCommand(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.message, path, context, ok, tt.path, tt.context, tt.ok)
			}
		})
	}
}

func TestValidateTranscriptionPath(t *testing.T) {
	valid := transcriptionTestFile(t, "recording.webm")
	if got, err := validateTranscriptionPath(valid); err != nil || got != valid {
		t.Fatalf("valid path = %q, %v", got, err)
	}

	if _, err := validateTranscriptionPath("relative.webm"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative path error = %v", err)
	}
	if _, err := validateTranscriptionPath(filepath.Join(browse.UploadDir, "missing.webm")); err == nil {
		t.Fatal("missing file accepted")
	}

	outside := filepath.Join(t.TempDir(), "outside.webm")
	if err := os.WriteFile(outside, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateTranscriptionPath(outside); err == nil || !strings.Contains(err.Error(), "inside") {
		t.Fatalf("outside path error = %v", err)
	}

	symlink := filepath.Join(filepath.Dir(valid), "link.webm")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := validateTranscriptionPath(symlink); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestProbeHasVideo(t *testing.T) {
	for _, tt := range []struct {
		probe string
		want  bool
	}{
		{`{"streams":[{"codec_type":"audio"},{"codec_type":"video"}],"format":{}}`, true},
		{`{"streams":[{"codec_type":"audio"}],"format":{}}`, false},
	} {
		run := func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "ffprobe" || args[len(args)-1] != "/tmp/media.webm" {
				return nil, fmt.Errorf("unexpected command: %s %v", name, args)
			}
			return []byte(tt.probe), nil
		}
		got, err := probeHasVideo(context.Background(), "/tmp/media.webm", run)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("probeHasVideo(%s) = %v, want %v", tt.probe, got, tt.want)
		}
	}
}

func queuedTranscriptionReceipt(t *testing.T, w *httptest.ResponseRecorder, database *db.DB, parentID string) db.QueuedMessage {
	t.Helper()
	var receipt map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["status"] != "queued" {
		t.Fatalf("receipt = %#v", receipt)
	}
	queued := queuedMessages(t, database, parentID)
	for i := len(queued) - 1; i >= 0; i-- {
		if queued[i].Kind == db.QueuedMessageKindTranscription {
			return queued[i]
		}
	}
	t.Fatal("queued transcription missing")
	return db.QueuedMessage{}
}

// holdTranscriptionProbe makes the worker's ffprobe wait for release, so a
// launched job stays registered until the test has its done channel.
func holdTranscriptionProbe(server *Server) (release func()) {
	ch := make(chan struct{})
	server.mediaRun = func(context.Context, string, ...string) ([]byte, error) {
		<-ch
		return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"2"}}`), nil
	}
	return func() { close(ch) }
}

func transcriptionDone(t *testing.T, server *Server, queuedID string) <-chan struct{} {
	t.Helper()
	server.transcriptionMu.Lock()
	defer server.transcriptionMu.Unlock()
	job, ok := server.transcriptionJobs[queuedID]
	if !ok {
		t.Fatalf("transcription job %q not registered", queuedID)
	}
	return job.done
}

func TestTranscriptionCommandPersistsBeforeDetachedWork(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	transcriber := &promptCapturingTranscriber{
		prompt: make(chan string, 1),
		text:   "predictable spoken words",
	}
	server.transcriber = transcriber
	mediaPath := transcriptionTestFile(t, "audio.webm")
	metadataPath := mediaPath + ".json"
	if err := os.WriteFile(metadataPath, []byte(`{"started_at":"2026-09-16T22:00:00Z","duration_ms":2000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	model := "predictable"
	draft, err := database.CreateDraftConversation(t.Context(), &cwd, &model, db.ConversationOptions{}, "Keep draft")
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	server.mediaRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ffprobe" || args[len(args)-1] != mediaPath {
			return nil, fmt.Errorf("unexpected command %s %v", name, args)
		}
		close(started)
		<-release
		return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"2"}}`), nil
	}

	body := fmt.Sprintf(`{"message":%q,"model":"predictable","conversation_options":{"thinking_level":"high"}}`, "/transcription "+mediaPath+"\nKeep draft")
	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+draft.ConversationID+"/chat", strings.NewReader(body))
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, draft.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	receipt := queuedTranscriptionReceipt(t, w, database, draft.ConversationID)
	done := transcriptionDone(t, server, receipt.ID)

	<-started
	queued := queuedMessages(t, database, draft.ConversationID)
	if len(queued) != 1 || queued[0].ID != receipt.ID {
		t.Fatalf("queued = %#v", queued)
	}
	item := queued[0]
	if item.Kind != db.QueuedMessageKindTranscription || item.State != db.QueuedMessageStateWorking || item.Transcription == nil {
		t.Fatalf("queued item = %#v", item)
	}
	if item.Transcription.MediaPath != mediaPath {
		t.Fatalf("transcription = %#v", item.Transcription)
	}
	if item.Model != "predictable" || item.CreatedAt.IsZero() || len(item.Llm) != 0 {
		t.Fatalf("working queue fields = %#v", item)
	}

	parent, err := database.GetConversationByID(t.Context(), draft.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.IsDraft || parent.Draft != "" {
		t.Fatalf("transcription parent was not promoted: %#v", parent)
	}
	children, err := database.GetSubagents(t.Context(), draft.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 0 {
		t.Fatalf("children = %#v", children)
	}
	server.mu.Lock()
	parentManager := server.activeConversations[draft.ConversationID]
	server.mu.Unlock()
	parentManager.SetAgentWorking(true)

	close(release)
	<-done
	prompt := <-transcriber.prompt
	if !strings.Contains(prompt, "Application: Shelley") ||
		!strings.Contains(prompt, "Project: "+filepath.Base(cwd)) ||
		!strings.Contains(prompt, "Working directory: "+cwd) {
		t.Fatalf("prompt = %q", prompt)
	}
	if strings.Contains(prompt, "Keep draft") {
		t.Fatalf("prompt contains composer text: %q", prompt)
	}
	queued = queuedMessages(t, database, draft.ConversationID)
	if len(queued) != 1 || queued[0].State != db.QueuedMessageStateReady {
		t.Fatalf("completed queue = %#v", queued)
	}
	if strings.Contains(string(queued[0].Llm), metadataPath) {
		t.Fatalf("audio transcript includes timing metadata: %s", queued[0].Llm)
	}
	var audit []llm.Message
	if err := json.Unmarshal(queued[0].Transcription.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit) != 2 || len(audit[0].Content) != 1 ||
		audit[0].Content[0].ToolName != "openai_audio_transcription" ||
		len(audit[1].Content) != 1 || !strings.Contains(audit[1].Content[0].ToolResult[0].Text, "predictable spoken words") {
		t.Fatalf("parent audit = %#v", audit)
	}
	var auditedInput map[string]any
	if err := json.Unmarshal(audit[0].Content[0].ToolInput, &auditedInput); err != nil {
		t.Fatal(err)
	}
	if auditedInput["prompt_chars"] != float64(utf8.RuneCountInString(prompt)) {
		t.Fatalf("audited input = %#v", auditedInput)
	}
	if auditedInput["model"] != "gpt-transcribe" || auditedInput["response_format"] != "json" {
		t.Fatalf("audited audio request = %#v", auditedInput)
	}
	if _, ok := auditedInput["timestamps"]; ok {
		t.Fatalf("audited audio request asks for timestamps: %#v", auditedInput)
	}
	if strings.Contains(audit[1].Content[0].ToolResult[0].Text, "timestamps_path") {
		t.Fatalf("audited audio result has timestamps: %#v", audit[1])
	}
	parentManager.SetAgentWorking(false)
	if _, err := parentManager.CancelQueuedMessages(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedTranscriptionPreservesFIFOAndVideoPaths(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	server.transcriber = successfulRecordingTranscriber("predictable spoken words")
	mediaPath := transcriptionTestFile(t, "screen.webm")
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	server.mediaRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ffprobe":
			close(started)
			<-release
			return []byte(`{"streams":[{"codec_type":"audio"},{"codec_type":"video"}],"format":{"duration":"12"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected command %q", name)
		}
	}

	body := fmt.Sprintf(`{"message":%q,"model":"predictable"}`, "/transcription "+mediaPath+"\nKeep this note. [/tmp/shelley-uploads/context.png]")
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), conversation.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	receipt := queuedTranscriptionReceipt(t, w, database, conversation.ConversationID)
	done := transcriptionDone(t, server, receipt.ID)
	<-started

	server.mu.Lock()
	manager := server.activeConversations[conversation.ConversationID]
	server.mu.Unlock()
	manager.SetAgentWorking(true)

	ordinaryBody := `{"message":"echo: after transcription","model":"predictable"}`
	ordinaryW := httptest.NewRecorder()
	server.handleChatConversation(ordinaryW, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(ordinaryBody)), conversation.ConversationID)
	if ordinaryW.Code != http.StatusAccepted || !strings.Contains(ordinaryW.Body.String(), "queued") {
		t.Fatalf("ordinary send = %d %s", ordinaryW.Code, ordinaryW.Body.String())
	}
	if userMessageRowExists(t, database, conversation.ConversationID, "after transcription") {
		t.Fatal("ordinary send overtook working transcription")
	}

	close(release)
	<-done
	queued := queuedMessages(t, database, conversation.ConversationID)
	if len(queued) != 2 || queued[0].ID != receipt.ID || queued[0].State != db.QueuedMessageStateReady {
		t.Fatalf("ready queue = %#v", queued)
	}
	var final llm.Message
	if err := json.Unmarshal(queued[0].Llm, &final); err != nil {
		t.Fatal(err)
	}
	finalText := final.Content[0].Text
	for _, want := range []string{
		"Keep this note. [/tmp/shelley-uploads/context.png]",
		"predictable spoken words",

		"Screen recording: [" + mediaPath + "]\nTimeline: [" + mediaPath + ".timeline.md]",
		screenRecordingGuidance,
	} {
		if !strings.Contains(finalText, want) {
			t.Errorf("final text missing %q: %s", want, finalText)
		}
	}
	timeline, err := os.ReadFile(mediaPath + ".timeline.md")
	if err != nil {
		t.Fatal(err)
	}
	wantTimeline := `# Screen recording timeline

Times are from the start of the recording.

00:01.5  said: "predictable spoken words"
`
	if string(timeline) != wantTimeline {
		t.Errorf("timeline file:\n%s\nwant:\n%s", timeline, wantTimeline)
	}
	if strings.Contains(finalText, "transcribed by subagent") {
		t.Fatalf("final text has misleading attribution: %s", finalText)
	}
	if len(queued[0].Transcription.Audit) == 0 {
		t.Fatal("ready transcription is missing inline audit messages")
	}
	var audit []llm.Message
	if err := json.Unmarshal(queued[0].Transcription.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	var auditedInput map[string]any
	if err := json.Unmarshal(audit[0].Content[0].ToolInput, &auditedInput); err != nil {
		t.Fatal(err)
	}
	if auditedInput["model"] != "gpt-transcribe" || auditedInput["response_format"] != "json" {
		t.Fatalf("audited video request = %#v", auditedInput)
	}
	if auditedInput["timestamps"] != true {
		t.Fatalf("audited video timestamps = %#v", auditedInput)
	}
	if !strings.Contains(audit[1].Content[0].ToolResult[0].Text, `"model":"gpt-transcribe"`) ||
		!strings.Contains(audit[1].Content[0].ToolResult[0].Text, `"timestamps_model":"whisper-1"`) ||
		!strings.Contains(audit[1].Content[0].ToolResult[0].Text, mediaPath+".timestamps.json") {
		t.Fatalf("audited video result = %#v", audit[1])
	}

	manager.SetAgentWorking(false)
	manager.drainPendingMessages()
	if got := queuedMessages(t, database, conversation.ConversationID); len(got) != 0 {
		t.Fatalf("queue after drain = %#v", got)
	}
	messages, err := database.ListMessages(t.Context(), conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for i, row := range messages {
		if row.LlmData == nil {
			continue
		}
		var message llm.Message
		if err := json.Unmarshal([]byte(*row.LlmData), &message); err != nil {
			t.Fatal(err)
		}
		for _, content := range message.Content {
			switch {
			case content.Type == llm.ContentTypeToolUse && content.ToolName == "openai_audio_transcription":
				positions["tool_use"] = i
				if !message.ExcludedFromContext {
					t.Error("inline transcription tool use is not excluded from model context")
				}
			case content.Type == llm.ContentTypeToolResult:
				positions["tool_result"] = i
				if !message.ExcludedFromContext {
					t.Error("inline transcription tool result is not excluded from model context")
				}
			case strings.Contains(content.Text, "predictable spoken words"):
				positions["transcript"] = i
			case strings.Contains(content.Text, "after transcription"):
				positions["after"] = i
			}
		}
	}
	if !(positions["tool_use"] < positions["tool_result"] &&
		positions["tool_result"] < positions["transcript"] &&
		positions["transcript"] < positions["after"]) {
		t.Fatalf("inline transcription order = %#v", positions)
	}
}

func TestFailedTranscriptionBlocksUntilRetry(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	server.transcriber = successfulRecordingTranscriber("predictable spoken words")
	mediaPath := transcriptionTestFile(t, "retry.webm")
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	server.mediaRun = func(context.Context, string, ...string) ([]byte, error) {
		close(firstStarted)
		<-firstRelease
		return nil, errors.New("broken media")
	}
	body := fmt.Sprintf(`{"message":%q,"model":"predictable"}`, "/transcription "+mediaPath)
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), conversation.ConversationID)
	receipt := queuedTranscriptionReceipt(t, w, database, conversation.ConversationID)
	firstDone := transcriptionDone(t, server, receipt.ID)
	<-firstStarted
	close(firstRelease)
	<-firstDone

	queued := queuedMessages(t, database, conversation.ConversationID)
	if len(queued) != 1 || queued[0].State != db.QueuedMessageStateFailed || !strings.Contains(queued[0].Error, "broken media") {
		t.Fatalf("failed queue = %#v", queued)
	}

	ordinaryW := httptest.NewRecorder()
	server.handleChatConversation(ordinaryW, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"message":"echo: held behind failure","model":"predictable"}`)), conversation.ConversationID)
	if ordinaryW.Code != http.StatusAccepted || userMessageRowExists(t, database, conversation.ConversationID, "held behind failure") {
		t.Fatalf("ordinary send overtook failure: %d %s", ordinaryW.Code, ordinaryW.Body.String())
	}

	server.mu.Lock()
	manager := server.activeConversations[conversation.ConversationID]
	server.mu.Unlock()
	manager.SetAgentWorking(true)
	secondStarted := make(chan struct{})
	secondRelease := make(chan struct{})
	server.mediaRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "ffprobe" {
			return nil, fmt.Errorf("unexpected command %q", name)
		}
		close(secondStarted)
		<-secondRelease
		return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"2"}}`), nil
	}
	retryW := httptest.NewRecorder()
	retryReq := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversation.ConversationID+"/retry-queued?queued_id="+receipt.ID, nil)
	server.handleRetryQueued(retryW, retryReq, conversation.ConversationID)
	if retryW.Code != http.StatusAccepted {
		t.Fatalf("retry = %d: %s", retryW.Code, retryW.Body.String())
	}
	retryReceipt := queuedTranscriptionReceipt(t, retryW, database, conversation.ConversationID)
	if retryReceipt.ID != receipt.ID || retryReceipt.State != db.QueuedMessageStateWorking {
		t.Fatalf("retry receipt = %#v", retryReceipt)
	}
	secondDone := transcriptionDone(t, server, receipt.ID)
	<-secondStarted
	close(secondRelease)
	<-secondDone

	queued = queuedMessages(t, database, conversation.ConversationID)
	if len(queued) != 2 || queued[0].State != db.QueuedMessageStateReady || queued[0].Error != "" {
		t.Fatalf("retried queue = %#v", queued)
	}
	manager.SetAgentWorking(false)
	<-manager.drainPendingMessages()
	if !userMessageRowExists(t, database, conversation.ConversationID, "held behind failure") {
		t.Fatal("ordinary message did not drain after retry became ready")
	}
}

type blockingRecordingTranscriber struct {
	started   chan struct{}
	cancelled chan struct{}
	once      sync.Once
}

func (*blockingRecordingTranscriber) Available() error { return nil }

func (s *blockingRecordingTranscriber) Transcribe(ctx context.Context, _, _ string, _ bool) (transcriptionResult, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	close(s.cancelled)
	return transcriptionResult{}, ctx.Err()
}

func TestRetryWorkingTranscriptionDoesNotCancelWorker(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	blocking := &blockingRecordingTranscriber{
		started: make(chan struct{}), cancelled: make(chan struct{}),
	}
	server.transcriber = blocking
	server.mediaRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "ffprobe" {
			return nil, fmt.Errorf("unexpected command %q", name)
		}
		return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"2"}}`), nil
	}
	mediaPath := transcriptionTestFile(t, "duplicate-retry.webm")
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"message":%q,"model":"predictable"}`, "/transcription "+mediaPath)
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), conversation.ConversationID)
	receipt := queuedTranscriptionReceipt(t, w, database, conversation.ConversationID)
	done := transcriptionDone(t, server, receipt.ID)
	<-blocking.started
	defer func() {
		server.cancelQueuedTranscriptions(t.Context(), conversation.ConversationID, receipt.ID, func() error { return nil })
		<-done
	}()

	retryW := httptest.NewRecorder()
	server.handleRetryQueued(
		retryW,
		httptest.NewRequest(http.MethodPost, "/retry-queued?queued_id="+receipt.ID, nil),
		conversation.ConversationID,
	)
	if retryW.Code != http.StatusConflict {
		t.Fatalf("retry = %d: %s", retryW.Code, retryW.Body.String())
	}
	select {
	case <-blocking.cancelled:
		t.Fatal("duplicate retry cancelled active worker")
	default:
	}
	server.transcriptionMu.Lock()
	_, registered := server.transcriptionJobs[receipt.ID]
	server.transcriptionMu.Unlock()
	if !registered {
		t.Fatal("duplicate retry removed active worker")
	}
}

func TestRetryTranscriptionWithWrongParentDoesNotCancelWorker(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	blocking := &blockingRecordingTranscriber{
		started: make(chan struct{}), cancelled: make(chan struct{}),
	}
	server.transcriber = blocking
	server.mediaRun = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"2"}}`), nil
	}
	owner, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := transcriptionTestFile(t, "wrong-parent-retry.webm")
	body := fmt.Sprintf(`{"message":%q,"model":"predictable"}`, "/transcription "+mediaPath)
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), owner.ConversationID)
	receipt := queuedTranscriptionReceipt(t, w, database, owner.ConversationID)
	done := transcriptionDone(t, server, receipt.ID)
	<-blocking.started
	defer func() {
		server.cancelQueuedTranscriptions(t.Context(), owner.ConversationID, receipt.ID, func() error { return nil })
		<-done
	}()

	retryW := httptest.NewRecorder()
	server.handleRetryQueued(
		retryW,
		httptest.NewRequest(http.MethodPost, "/retry-queued?queued_id="+receipt.ID, nil),
		other.ConversationID,
	)
	if retryW.Code != http.StatusNotFound {
		t.Fatalf("retry = %d: %s", retryW.Code, retryW.Body.String())
	}
	select {
	case <-blocking.cancelled:
		t.Fatal("wrong-parent retry cancelled active worker")
	default:
	}
}

func TestRetryTranscriptionRejectsStaleAttemptUpdates(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := transcriptionTestFile(t, "stale-attempt.webm")
	queued := db.QueuedMessage{
		ID:        "stale-attempt",
		Llm:       json.RawMessage(`{"Role":0}`),
		CreatedAt: time.Now().UTC(),
		Model:     "predictable",
		Kind:      db.QueuedMessageKindTranscription,
		State:     db.QueuedMessageStateFailed,
		Error:     "old failure",
		Transcription: &db.QueuedTranscription{
			MediaPath: mediaPath,
		},
	}
	if _, err := database.AppendQueuedMessage(t.Context(), conversation.ConversationID, queued); err != nil {
		t.Fatal(err)
	}
	server.transcriptionMu.Lock()
	server.transcriptionJobs[queued.ID] = transcriptionJob{
		parentID: conversation.ConversationID, attemptID: "old-attempt", cancel: func() {}, done: make(chan struct{}),
	}
	server.transcriptionMu.Unlock()
	probeStarted := make(chan struct{})
	server.mediaRun = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		close(probeStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	retryW := httptest.NewRecorder()
	server.handleRetryQueued(
		retryW,
		httptest.NewRequest(http.MethodPost, "/retry-queued?queued_id="+queued.ID, nil),
		conversation.ConversationID,
	)
	if retryW.Code != http.StatusAccepted {
		t.Fatalf("retry = %d: %s", retryW.Code, retryW.Body.String())
	}
	done := transcriptionDone(t, server, queued.ID)
	<-probeStarted
	server.failQueuedTranscription(
		conversation.ConversationID,
		queued.ID,
		"old-attempt",
		nil,
		context.Canceled,
	)
	current, err := database.GetQueuedMessage(t.Context(), conversation.ConversationID, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != db.QueuedMessageStateWorking || current.Error != "" {
		t.Fatalf("stale attempt changed queue = %#v", current)
	}
	server.cancelQueuedTranscriptions(t.Context(), conversation.ConversationID, queued.ID, func() error { return nil })
	<-done
}

func TestCancelQueuedTranscriptionCancelsWorker(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	blocking := &blockingRecordingTranscriber{
		started: make(chan struct{}), cancelled: make(chan struct{}),
	}
	server.transcriber = blocking
	server.mediaRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "ffprobe" {
			return nil, fmt.Errorf("unexpected command %q", name)
		}
		return []byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"2"}}`), nil
	}
	mediaPath := transcriptionTestFile(t, "cancel.webm")
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"message":%q,"model":"predictable"}`, "/transcription "+mediaPath)
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), conversation.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	receipt := queuedTranscriptionReceipt(t, w, database, conversation.ConversationID)
	done := transcriptionDone(t, server, receipt.ID)
	<-blocking.started

	cancelW := httptest.NewRecorder()
	cancelReq := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversation.ConversationID+"/cancel-queued?queued_id="+receipt.ID, nil)
	server.handleCancelQueued(cancelW, cancelReq, conversation.ConversationID)
	if cancelW.Code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", cancelW.Code, cancelW.Body.String())
	}
	<-blocking.cancelled
	<-done
	if queued := queuedMessages(t, database, conversation.ConversationID); len(queued) != 0 {
		t.Fatalf("queue after cancel = %#v", queued)
	}
	server.transcriptionMu.Lock()
	_, stillRunning := server.transcriptionJobs[receipt.ID]
	server.transcriptionMu.Unlock()
	if stillRunning {
		t.Fatal("transcription worker still registered after queue cancellation")
	}
}

func TestCancelConversationCancelsQueuedTranscriptionWithoutParentLoop(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	server.transcriber = successfulRecordingTranscriber("never reached")
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server.mediaRun = func(ctx context.Context, name string, _ ...string) ([]byte, error) {
		if name != "ffprobe" {
			return nil, fmt.Errorf("unexpected command %q", name)
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	mediaPath := transcriptionTestFile(t, "cancel-parent.webm")
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"message":%q,"model":"predictable"}`, "/transcription "+mediaPath)
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), conversation.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	<-started

	cancelW := httptest.NewRecorder()
	cancelReq := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversation.ConversationID+"/cancel", nil)
	server.handleCancelConversation(cancelW, cancelReq, conversation.ConversationID)
	if cancelW.Code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", cancelW.Code, cancelW.Body.String())
	}
	<-cancelled
	if queued := queuedMessages(t, database, conversation.ConversationID); len(queued) != 0 {
		t.Fatalf("queue after parent cancel = %#v", queued)
	}
}

func TestQueuedTranscriptionRecoveryResumesInterruptedWork(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	server.transcriber = successfulRecordingTranscriber("recovered direct words")
	release := holdTranscriptionProbe(server)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, queued, err := database.CreateQueuedTranscription(t.Context(), parent.ConversationID, db.QueuedMessage{
		ID: "interrupted-queued", CreatedAt: time.Now().UTC(), Model: "predictable",
		Kind: db.QueuedMessageKindTranscription, State: db.QueuedMessageStateWorking,
		Transcription: &db.QueuedTranscription{MediaPath: "/tmp/interrupted.webm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetConversationAgentWorking(t.Context(), parent.ConversationID, true); err != nil {
		t.Fatal(err)
	}

	server.recoverQueuedTranscriptions(t.Context())
	done := transcriptionDone(t, server, queued.ID)
	release()
	<-done
	persisted := queuedMessages(t, database, parent.ConversationID)
	if len(persisted) != 1 || persisted[0].State != db.QueuedMessageStateReady {
		t.Fatalf("resumed queue = %#v", persisted)
	}
	var audit []llm.Message
	if err := json.Unmarshal(persisted[0].Transcription.Audit, &audit); err != nil || len(audit) != 2 {
		t.Fatalf("recovered audit = %#v, err = %v", audit, err)
	}
}

func TestQueuedTranscriptionRecoveryIncludesArchivedParents(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	server.transcriber = successfulRecordingTranscriber("archived words")
	release := holdTranscriptionProbe(server)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, queued, err := database.CreateQueuedTranscription(t.Context(), parent.ConversationID, db.QueuedMessage{
		ID: "archived-queued", CreatedAt: time.Now().UTC(), Model: "predictable",
		Kind: db.QueuedMessageKindTranscription, State: db.QueuedMessageStateWorking,
		Transcription: &db.QueuedTranscription{MediaPath: "/tmp/archived.webm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetConversationAgentWorking(t.Context(), parent.ConversationID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ArchiveConversation(t.Context(), parent.ConversationID); err != nil {
		t.Fatal(err)
	}

	server.recoverQueuedTranscriptions(t.Context())
	done := transcriptionDone(t, server, queued.ID)
	release()
	<-done
	persisted := queuedMessages(t, database, parent.ConversationID)
	if len(persisted) != 1 || persisted[0].ID != queued.ID || persisted[0].State != db.QueuedMessageStateReady {
		t.Fatalf("archived recovered queue = %#v", persisted)
	}
}

func TestQueuedTranscriptionRecoveryIsIdempotent(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	server.transcriber = successfulRecordingTranscriber("recovered words")
	release := holdTranscriptionProbe(server)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, queued, err := database.CreateQueuedTranscription(t.Context(), parent.ConversationID, db.QueuedMessage{
		ID: "recovery-queued", CreatedAt: time.Now().UTC(), Model: "predictable",
		Kind: db.QueuedMessageKindTranscription, State: db.QueuedMessageStateWorking,
		Transcription: &db.QueuedTranscription{MediaPath: "/tmp/recovery.webm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetConversationAgentWorking(t.Context(), parent.ConversationID, true); err != nil {
		t.Fatal(err)
	}

	// The held worker keeps the first job registered for the second scan.
	server.recoverQueuedTranscriptions(t.Context())
	server.recoverQueuedTranscriptions(t.Context())
	done := transcriptionDone(t, server, queued.ID)
	release()
	<-done
	persisted := queuedMessages(t, database, parent.ConversationID)
	if len(persisted) != 1 || persisted[0].ID != queued.ID || persisted[0].State != db.QueuedMessageStateReady {
		t.Fatalf("recovered queue = %#v", persisted)
	}
	if userMessageRowExists(t, database, parent.ConversationID, "recovered words") {
		t.Fatal("recovery drained despite parent working")
	}

	server.mu.Lock()
	manager := server.activeConversations[parent.ConversationID]
	server.mu.Unlock()
	manager.SetAgentWorking(false)
	manager.drainPendingMessages()
	if queued := queuedMessages(t, database, parent.ConversationID); len(queued) != 0 {
		t.Fatalf("queue after recovered drain = %#v", queued)
	}
	messages, err := database.ListMessages(t.Context(), parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range messages {
		if message.Type != string(db.MessageTypeUser) || message.LlmData == nil {
			continue
		}
		var value llm.Message
		if err := json.Unmarshal([]byte(*message.LlmData), &value); err != nil {
			t.Fatal(err)
		}
		for _, content := range value.Content {
			if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "recovered words") {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("recovered transcript count = %d, want 1", count)
	}
}

func TestTranscriptionCommandRejectsInvalidPathBeforeCreatingChild(t *testing.T) {
	server, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"message":"/transcription /etc/passwd","model":"predictable"}`
	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conv.ConversationID+"/chat", strings.NewReader(body))
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, conv.ConversationID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	children, err := database.GetSubagents(context.Background(), conv.ConversationID)
	if err != nil || len(children) != 0 {
		t.Fatalf("children = %#v, %v", children, err)
	}
	if messages, err := database.ListMessages(context.Background(), conv.ConversationID); err != nil || len(messages) != 0 {
		t.Fatalf("parent messages = %#v, %v", messages, err)
	}
}

// Without a transcription route, /transcription must fail before it queues
// a durable item: a failed item would block every later message.
func TestTranscriptionCommandRejectsUnavailableModelBeforeQueueing(t *testing.T) {
	server, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := transcriptionTestFile(t, "recording.webm")
	body := fmt.Sprintf(`{"message":%q,"model":"predictable"}`, "/transcription "+mediaPath)
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), conv.ConversationID)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "OPENAI_API_KEY") {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if queued := queuedMessages(t, database, conv.ConversationID); len(queued) != 0 {
		t.Fatalf("queued = %#v, want none", queued)
	}
	if messages, err := database.ListMessages(t.Context(), conv.ConversationID); err != nil || len(messages) != 0 {
		t.Fatalf("messages = %#v, %v", messages, err)
	}
}

// A failure to look routes up is a server fault, not a missing model.
func TestTranscriptionCommandReportsRouteLookupFailure(t *testing.T) {
	server, database, _ := newTestServer(t)
	server.transcriber = &openAIRecordingTranscriber{resolveModel: func(string) (models.TranscriptionModel, error) {
		return models.TranscriptionModel{}, errors.New("database is locked")
	}}
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"message":%q,"model":"predictable"}`, "/transcription "+transcriptionTestFile(t, "recording.webm"))
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), conv.ConversationID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if queued := queuedMessages(t, database, conv.ConversationID); len(queued) != 0 {
		t.Fatalf("queued = %#v, want none", queued)
	}
}

func TestIndexInitDataReportsTranscriptionAvailability(t *testing.T) {
	server, _, _ := newTestServer(t)
	if got := indexInitData(t, server)["transcription_available"]; got != false {
		t.Fatalf("transcription_available = %#v, want false without a route", got)
	}
	server.transcriber = successfulRecordingTranscriber("words")
	if got := indexInitData(t, server)["transcription_available"]; got != true {
		t.Fatalf("transcription_available = %#v, want true", got)
	}
}
