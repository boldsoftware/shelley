package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/llm/predictable"
)

// Texted media must survive delivery as local uploads, including image-only
// messages, and retries must not fetch an expired provider URL again.
func TestChannelAttachments(t *testing.T) {
	for _, text := range []string{"", "look at these"} {
		t.Run(fmt.Sprintf("text=%q", text), func(t *testing.T) {
			t.Parallel()
			var downloads atomic.Int64
			media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				fmt.Fprint(w, "image bytes "+r.URL.Path)
			}))
			t.Cleanup(media.Close)
			model := predictable.NewService()
			server, database, h := channelTestServer(t, model)
			body := map[string]any{
				"type": "message", "id": "photo-1", "chat_id": "photos", "web": true, "text": text,
				"attachments": []map[string]any{
					{"url": media.URL + "/one.png?signature=secret", "filename": "../../one]\n.png", "mime_type": "image/png"},
					{"url": media.URL + "/two.jpg?signature=secret", "mime_type": "image/jpeg"},
				},
			}
			if w := postJSON(t, h, "/api/channels/messages", body); w.Code != http.StatusNoContent {
				t.Fatalf("receive: %d %s", w.Code, w.Body.String())
			}
			assistantID := chatAssistant(t, server, "photos")
			messages := channelUserMessages(t, database, assistantID)
			if len(messages) != 1 {
				t.Fatalf("stored messages = %q", messages)
			}
			message := strings.TrimPrefix(messages[0], "photo-1: ")
			pathLine := message
			if text != "" {
				if !strings.HasPrefix(message, text+"\n\n") {
					t.Fatalf("caption lost: %q", message)
				}
				pathLine = strings.TrimPrefix(message, text+"\n\n")
			}
			tokens := strings.Split(pathLine, " ")
			if len(tokens) != 2 {
				t.Fatalf("attachment paths = %q", pathLine)
			}
			for i, token := range tokens {
				if !strings.HasPrefix(token, "[") || !strings.HasSuffix(token, "]") {
					t.Fatalf("not an upload reference: %q", token)
				}
				path := token[1 : len(token)-1]
				if filepath.Dir(path) != browse.UploadDir {
					t.Fatalf("path escaped upload directory: %q", path)
				}
				t.Cleanup(func() { os.Remove(path) })
				content, err := os.ReadFile(path)
				want := []string{"image bytes /one.png", "image bytes /two.jpg"}[i]
				if err != nil || string(content) != want {
					t.Fatalf("local attachment = %q, %v; want %q", content, err, want)
				}
			}
			waitFor(t, 5*time.Second, func() bool {
				for _, request := range model.GetRecentRequests() {
					if strings.Contains(lastRequestUserText(request), pathLine) {
						return true
					}
				}
				return false
			})
			media.Close()
			if w := postJSON(t, h, "/api/channels/messages", body); w.Code != http.StatusNoContent {
				t.Fatalf("retry after URL expiry: %d %s", w.Code, w.Body.String())
			}
			if got := downloads.Load(); got != 2 {
				t.Fatalf("downloads = %d, want 2", got)
			}
			if got := channelUserMessages(t, database, assistantID); len(got) != 1 || got[0] != messages[0] {
				t.Fatalf("retry duplicated message: %q", got)
			}
		})
	}
}

// A partial download must not reach the conversation. Retrying the delivery
// once the provider recovers must deliver all attachments exactly once.
func TestChannelAttachmentFailureRetries(t *testing.T) {
	t.Parallel()
	var recovered atomic.Bool
	filename := "retry-" + uuid.NewString() + ".png"
	firstPath := filepath.Join(browse.UploadDir, filename)
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/second.png" && !recovered.Load() {
			http.Error(w, "not available", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, "image")
	}))
	t.Cleanup(media.Close)
	server, database, h := channelTestServer(t, nil)
	body := map[string]any{
		"type": "message", "id": "photo-1", "chat_id": "retry-photos", "web": true, "text": "caption",
		"first_reply": "received",
		"attachments": []map[string]any{
			{"url": media.URL + "/first.png", "filename": filename},
			{"url": media.URL + "/second.png"},
		},
	}

	t.Cleanup(func() { os.Remove(firstPath) })
	if w := postJSON(t, h, "/api/channels/messages", body); w.Code < 500 {
		t.Fatalf("failed download acknowledged: %d %s", w.Code, w.Body.String())
	}
	assistantID := server.assistantID()
	if got := channelUserMessages(t, database, assistantID); len(got) != 0 {
		t.Fatalf("partial delivery = %q", got)
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("partial upload was not removed: %v", err)
	}
	recovered.Store(true)
	if w := postJSON(t, h, "/api/channels/messages", body); w.Code != http.StatusNoContent {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	messages := channelUserMessages(t, database, assistantID)
	if len(messages) != 1 || !strings.HasPrefix(messages[0], "photo-1: caption\n\n[") {
		t.Fatalf("retried delivery = %q", messages)
	}
	for _, token := range strings.Split(strings.SplitN(messages[0], "\n\n", 2)[1], " ") {
		path := strings.Trim(token, "[]")
		t.Cleanup(func() { os.Remove(path) })
	}
}

func TestChannelAttachmentIncompleteDownload(t *testing.T) {
	t.Parallel()
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, "incomplete")
	}))
	t.Cleanup(media.Close)
	server, _, _ := channelTestServer(t, nil)
	filename := uuid.NewString() + ".png"
	path := filepath.Join(browse.UploadDir, filename)
	t.Cleanup(func() { os.Remove(path) })
	if _, err := server.downloadChannelAttachment(t.Context(), channelAttachment{
		URL: media.URL, Filename: filename,
	}); err == nil {
		t.Fatal("truncated download succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("incomplete upload was not removed: %v", err)
	}
}
