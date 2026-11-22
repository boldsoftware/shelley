package test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ported from ui/e2e/assistant-preview.spec.ts.

// previewStream answers the page's /api/stream2 EventSources itself, one
// connection after another, and sends only what the test says.
type previewStream struct {
	s     *shelley
	id    string
	mu    sync.Mutex
	conns []*previewConn
}

type previewConn struct {
	events chan string
	end    chan struct{}
}

// conn waits for the page to have opened connection i (from 0).
func (ps *previewStream) conn(i int) *previewConn {
	var c *previewConn
	ps.s.poll(15*time.Second, func() error {
		ps.mu.Lock()
		defer ps.mu.Unlock()
		if len(ps.conns) <= i {
			return fmt.Errorf("the page has opened %d of /api/stream2, not %d", len(ps.conns), i+1)
		}
		c = ps.conns[i]
		return nil
	})
	return c
}

func (ps *previewStream) send(i int, event any) {
	ps.conn(i).events <- "data: " + mustJSON(event) + "\n\n"
}

// working sends connection i a conversation list reset, from a fresh
// snapshot, that marks the conversation working or idle, as hash.
func (ps *previewStream) working(i int, v bool, hash string) {
	var snapshot struct {
		Conversations []map[string]any `json:"conversations"`
	}
	ps.s.api("GET", "/api/conversations/snapshot", nil, &snapshot)
	for _, row := range snapshot.Conversations {
		if row["conversation_id"] == ps.id {
			row["working"], row["agent_working"] = v, v
		}
	}
	ps.send(i, map[string]any{"conversation_list_patch": map[string]any{
		"reset":    true,
		"new_hash": hash,
		"at":       time.Now().UTC().Format(time.RFC3339Nano),
		"patch":    []any{map[string]any{"op": "replace", "path": "", "value": snapshot.Conversations}},
	}})
}

// previewMessage is what previewReply reads of a message.
type previewMessage struct {
	apiMessage
	MessageID string  `json:"message_id"`
	LLMData   *string `json:"llm_data"`
}

// previewReply is the newest finished agent turn's text and message ID,
// once the conversation has two.
func previewReply(s *shelley, id string) (text, messageID string) {
	s.poll(30*time.Second, func() error {
		var got struct {
			Messages []previewMessage `json:"messages"`
		}
		s.api("GET", "/api/conversation/"+id, nil, &got)
		turns := slices.DeleteFunc(got.Messages, func(m previewMessage) bool { return !m.endsTurn() })
		if len(turns) < 2 {
			return errors.New("the second turn has not finished")
		}
		last := turns[len(turns)-1]
		var llm struct {
			Content []struct {
				Type int
				Text string
			}
		}
		if last.LLMData != nil {
			if err := json.Unmarshal([]byte(*last.LLMData), &llm); err != nil {
				return err
			}
		}
		var b strings.Builder
		for _, part := range llm.Content {
			if part.Type == 2 {
				b.WriteString(part.Text)
			}
		}
		if b.Len() == 0 {
			return errors.New("the reply has no text")
		}
		text, messageID = b.String(), last.MessageID
		return nil
	})
	return text, messageID
}

// previewPage records the newest conversation list patch's hash as
// window.__previewPatch.
const previewPage = `const NativeEventSource = window.EventSource;
window.EventSource = class extends NativeEventSource {
  constructor(url, options) {
    super(url, options);
    this.addEventListener('message', (event) => {
      const data = JSON.parse(event.data);
      if (data.conversation_list_patch) window.__previewPatch = data.conversation_list_patch.new_hash;
    });
  }
};
window.__frames = () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve('framed'))));`

func TestE2EAssistantPreviewCatchUpRetiresStreamedAnswer(t *testing.T) {
	// The real predictable-model reply is stored by the server, but the
	// page's stream gets only its partial delta; after a reconnect, the REST
	// catch-up brings the stored row, and the old answer must not flash back
	// into the live preview while the next send is in flight.
	e2e(t, useful(0), `A REST catch-up retires a streamed answer before the next send. The test answers the page's /api/stream2 EventSources itself, connection after connection, and sends them only what the call steps say, for the conversation /c/{{SLUG}} that was started with "Hello". "Mark the conversation C on connection N with hash H" means: run the call step named C; then wait until window.__previewPatch === H (eval with expect "true"); then eval await window.__frames() (two animation frames). 1. Navigate to /c/{{SLUG}}. 2. The element with data-testid "message-input" should be visible (allow 30s). 3. ".messages-container" should contain "Hello! I'm Shelley, your AI assistant." (allow 15s). 4. Call "connections" (the number of stream connections the page opened); it must return "1" (expect, which polls). 5. Call "answer" (allow 30s): it sends "echo: answer lost on reconnect" through the API and waits for the reply "answer lost on reconnect", which the page's stream does not get. 6. Mark the conversation "working on 1" with hash "preview-working-1". 7. Call "delta": connection 1 gets the reply's text as a stream delta. 8. ".streaming-message" should contain "answer lost on reconnect". 9. Mark the conversation "idle on 1" with hash "preview-idle-1". 10. ".streaming-message" should be hidden. 11. Call "end": it ends connection 1, and the page reconnects. 12. Call "connections"; it must return "2" (expect, allow 30s). 13. Call "catch-up" (whether the page has sent GET /api/conversation/<id>?last_sequence_id=... since the end); it must return "seen" (expect, allow 30s). 14. The first element matching '.messages-container [data-testid="message"]:not(.message-user)' whose text contains "answer lost on reconnect" (has_text) should be visible (allow 15s): the stored reply row. 15. ".streaming-message" should be hidden. 16. Fill the "message-input" element with "echo: next turn" and click the element with data-testid "send-button". 17. Call "chat started"; it must return "true" (expect, allow 15s): the server has the send, whose response the test holds. 18. Mark the conversation "working on 2" with hash "preview-working-2". 19. ".streaming-message" must be hidden (assert_hidden). 20. Call "release" (the held response goes on).`, func(s *shelley) {
		c := s.conversation("Hello", "")
		s.v("SLUG", c.Slug)
		ps := &previewStream{s: s, id: c.ID}
		s.initScript(previewPage)
		var mark int
		s.route("GET /api/stream2", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusOK)
			rc := http.NewResponseController(w)
			rc.Flush()
			conn := &previewConn{events: make(chan string, 100), end: make(chan struct{})}
			ps.mu.Lock()
			ps.conns = append(ps.conns, conn)
			ps.mu.Unlock()
			for {
				select {
				case e := <-conn.events:
					fmt.Fprint(w, e)
					rc.Flush()
				case <-conn.end:
					return
				case <-r.Context().Done():
					return
				}
			}
		})
		s.call("connections", func() string {
			ps.mu.Lock()
			defer ps.mu.Unlock()
			return strconv.Itoa(len(ps.conns))
		})
		var reply, replyID string
		s.call("answer", func() string {
			s.sendChat(c.ID, "echo: answer lost on reconnect")
			reply, replyID = previewReply(s, c.ID)
			if reply != "answer lost on reconnect" {
				s.fail("the reply is %q", reply)
			}
			return "answered"
		})
		markAs := func(name string, conn int, working bool, hash string) {
			s.call(name, func() string {
				ps.working(conn, working, hash)
				return "sent"
			})
		}
		markAs("working on 1", 0, true, "preview-working-1")
		markAs("idle on 1", 0, false, "preview-idle-1")
		markAs("working on 2", 1, true, "preview-working-2")
		s.call("delta", func() string {
			ps.send(0, map[string]any{
				"conversation_id": c.ID,
				"stream_delta":    map[string]any{"type": "text", "text": reply, "index": 1, "seq": 0, "message_id": replyID},
			})
			return "sent"
		})
		s.call("end", func() string {
			mark = len(s.requestLog())
			close(ps.conn(0).end)
			return "ended"
		})
		catchUp := "GET /api/conversation/" + c.ID + "?last_sequence_id="
		s.call("catch-up", func() string {
			if slices.ContainsFunc(s.requestLog()[mark:], func(r string) bool { return strings.HasPrefix(r, catchUp) }) {
				return "seen"
			}
			return "none"
		})
		// The send reaches the server, which starts the next turn, but its
		// response waits for "release".
		started := s.signal("chat started")
		release := s.gate("release")
		s.route("POST /api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
			resp, err := s.forward(r)
			if err != nil {
				abort(w, r)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				abort(w, r)
			}
			started.Store(true)
			if !release.wait(r) {
				return
			}
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)
			w.Write(body)
		})
	})
}
