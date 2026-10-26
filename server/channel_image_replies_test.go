package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/image/tiff"

	"shelley.exe.dev/claudetool"
)

// Local image attachments must be uploaded before one message is delivered,
// with its caption, image order and reply target intact. A failed upload must
// not deliver even the caption, and is not an uncertain message delivery.
func TestChannelImageReplies(t *testing.T) {
	for _, mode := range []string{"caption", "image-only", "tiff", "metadata-refused", "upload-refused", "upload-interrupted", "upload-redirected", "second-upload-refused", "invalid-upload-answer", "send-refused", "send-response-lost"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			data, contentType := pngBytes, "image/png"
			names := []string{"first.png", "second.png"}
			if mode == "tiff" {
				var b bytes.Buffer
				if err := tiff.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 1)), nil); err != nil {
					t.Fatal(err)
				}
				data, contentType, names = b.Bytes(), "image/tiff", []string{"first.tiff", "second.tiff"}
			}
			for _, name := range names {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			var uploads [][]byte
			var sent struct {
				Text          string   `json:"text"`
				AttachmentIDs []string `json:"attachment_ids"`
				ReplyTo       string   `json:"reply_to"`
			}
			var deliveries int
			var gateway *httptest.Server
			gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/attachments":
					var meta struct {
						Filename    string `json:"filename"`
						ContentType string `json:"content_type"`
						SizeBytes   int64  `json:"size_bytes"`
					}
					if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
						t.Error(err)
					}
					wantName := names[len(uploads)]
					if meta.Filename != wantName || meta.ContentType != contentType || meta.SizeBytes != int64(len(data)) {
						t.Errorf("upload metadata = %+v, want %s %s %d bytes", meta, wantName, contentType, len(data))
					}
					if mode == "metadata-refused" {
						w.WriteHeader(http.StatusBadGateway)
						fmt.Fprint(w, `{"error":"upstream_error","message":"provider unavailable"}`)
						return
					}
					if mode == "invalid-upload-answer" {
						fmt.Fprint(w, `{}`)
						return
					}
					id := fmt.Sprintf("image-%d", len(uploads)+1)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"attachment_id": id, "upload_url": gateway.URL + "/upload/" + id,
						"required_headers": map[string]string{"Content-Type": contentType, "X-Upload-Token": "signed"},
					})
				case "/upload/image-1", "/upload/image-2":
					if mode == "upload-redirected" {
						http.Redirect(w, r, gateway.URL+"/accepted", http.StatusSeeOther)
						return
					}
					if mode == "upload-refused" || (mode == "second-upload-refused" && len(uploads) == 1) {
						http.Error(w, "storage refused", http.StatusForbidden)
						return
					}
					if mode == "upload-interrupted" {
						w.Header().Set("Content-Length", "100")
						fmt.Fprint(w, "incomplete")
						return
					}
					if r.Method != http.MethodPut || r.ContentLength != int64(len(data)) ||
						r.Header.Get("Content-Type") != contentType || r.Header.Get("X-Upload-Token") != "signed" ||
						r.Header.Get("Authorization") != "" {
						t.Errorf("invalid signed upload: method=%s length=%d headers=%v", r.Method, r.ContentLength, r.Header)
					}
					b, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					uploads = append(uploads, b)
					w.WriteHeader(http.StatusNoContent)
				case "/accepted":
					fmt.Fprint(w, "nothing was uploaded")
				case "/send":
					deliveries++
					if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
						t.Error(err)
					}
					if mode == "send-refused" {
						w.WriteHeader(http.StatusServiceUnavailable)
						fmt.Fprint(w, `{"error":"line_paused","message":"paused"}`)
						return
					}
					if mode == "send-response-lost" {
						w.Header().Set("Content-Length", "100")
					}
					fmt.Fprint(w, `{"message_id":"outgoing-image"}`)
				case "/typing":
					fmt.Fprint(w, `{"ok":true}`)
				default:
					t.Errorf("unexpected gateway request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(gateway.Close)
			server, database, _ := channelTestServer(t, nil)
			conv, err := server.channelConversation(t.Context(), gateway.URL, "image-reply", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := server.receiveChannelEvent(t.Context(), gateway.URL, channelEvent{
				Type: "message", ID: "incoming-photo", ChatID: "image-reply", Text: "hello",
			}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, func() bool {
				c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
				return err == nil && !c.AgentWorking
			})
			typed, err := database.ListTypedUserMessages(t.Context(), conv.ConversationID)
			if err != nil || len(typed) != 1 {
				t.Fatalf("user messages = %d: %v", len(typed), err)
			}
			text := "here are the images"
			if mode == "image-only" {
				text = ""
			}
			tool := claudetool.MessageUserTool(userMessageFinder{db: database, conversationID: conv.ConversationID},
				channelChat{s: server, conversationID: conv.ConversationID}, claudetool.NewMutableWorkingDir(dir))
			input, _ := json.Marshal(claudetool.MessageUserInput{
				Text: text, Attachments: names, ReplyTo: typed[0].SequenceID, EndTurn: true,
			})
			out := tool.Run(t.Context(), input)
			mu.Lock()
			defer mu.Unlock()
			if mode != "caption" && mode != "image-only" && mode != "tiff" && mode != "send-refused" && mode != "send-response-lost" {
				if out.Error == nil || out.EndTurn || deliveries != 0 ||
					!strings.Contains(out.Error.Error(), "nothing was sent") || strings.Contains(out.Error.Error(), "may have been delivered") {
					t.Fatalf("failed upload: out=%+v deliveries=%d", out, deliveries)
				}
				return
			}
			if mode == "send-refused" || mode == "send-response-lost" {
				want := "the chat refused"
				if mode == "send-response-lost" {
					want = "may have been delivered"
				}
				if out.Error == nil || out.EndTurn || deliveries != 1 || len(uploads) != 2 ||
					!strings.Contains(out.Error.Error(), want) || strings.Contains(out.Error.Error(), "nothing was sent") {
					t.Fatalf("failed image send: out=%+v deliveries=%d uploads=%d", out, deliveries, len(uploads))
				}
				return
			}
			if out.Error != nil || !out.EndTurn {
				t.Fatalf("send image: %+v", out)
			}
			if deliveries != 1 || sent.Text != text || sent.ReplyTo != "incoming-photo" ||
				!slices.Equal(sent.AttachmentIDs, []string{"image-1", "image-2"}) {
				t.Fatalf("delivered %d messages: %+v", deliveries, sent)
			}
			if len(uploads) != 2 || !bytes.Equal(uploads[0], data) || !bytes.Equal(uploads[1], data) {
				t.Fatalf("uploaded images differ: %q", uploads)
			}
			if d := out.Display.(claudetool.MessageUserDisplay); len(d.Attachments) != 2 || d.TargetMessageID != typed[0].MessageID {
				t.Fatalf("sent attachments unavailable for UI previews: %+v", d)
			}
		})
	}
}
