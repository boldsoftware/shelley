package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

const optionsWithInternalFields = `{
	"tool_overrides":{"bash":"off"},
	"kind":"btw_reader",
	"parent_pointer":{"generation":40,"sequence_id":41}
}`

func TestClientInternalConversationOptionsAreRejected(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Server, *db.DB) *httptest.ResponseRecorder
	}{
		{
			name: "new conversation",
			run: func(server *Server, _ *db.DB) *httptest.ResponseRecorder {
				body := `{"message":"echo: hello","model":"predictable","conversation_options":` + optionsWithInternalFields + `}`
				w := httptest.NewRecorder()
				server.handleNewConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
				return w
			},
		},
		{
			name: "draft",
			run: func(server *Server, _ *db.DB) *httptest.ResponseRecorder {
				body := `{"draft":"hello","model":"predictable","conversation_options":` + optionsWithInternalFields + `}`
				w := httptest.NewRecorder()
				server.handleCreateDraft(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
				return w
			},
		},
		{
			name: "chat",
			run: func(server *Server, database *db.DB) *httptest.ResponseRecorder {
				conversation, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
				if err != nil {
					t.Fatal(err)
				}
				body := `{"message":"echo: hello","model":"predictable","conversation_options":` + optionsWithInternalFields + `}`
				w := httptest.NewRecorder()
				server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), conversation.ConversationID)
				return w
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, database, _ := newTestServer(t)
			w := test.run(server, database)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "internal conversation options") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
