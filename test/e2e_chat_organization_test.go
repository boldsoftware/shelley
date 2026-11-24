package test

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestE2ESlashRenameSuggestion(t *testing.T) {
	e2e(t, useful(0), `Bare /rename opens the current name without asking the model. Navigate to /c/rename-original and wait for [data-testid="message-input"]. Fill it with "/rename" and click [data-testid="send-button"]. Wait for #slash-rename-name and verify its value is "rename-original". Call "suggestion-count" expecting "0".
Click "Suggest a new name" and wait for #slash-rename-name to contain the input value "rename-suggested". Call "suggestion-count" expecting "1", and "stored-name" expecting "rename-original". Click "Save" and wait for the dialog to close. Call "stored-name" expecting "rename-suggested". Input values are the element's value property, not text content.`, func(s *shelley) {
		c := s.conversation("echo: rename fixture", "rename-original")
		path := "/api/conversation/" + c.ID
		mockOrganizationSuggestion(s, path+"/rename-suggestion", map[string]string{"slug": "rename-suggested"})
		s.call("stored-name", func() string {
			var current struct {
				Conversation struct{ Slug string } `json:"conversation"`
			}
			s.api("GET", path, nil, &current)
			return current.Conversation.Slug
		})
	})
}

func TestE2ESlashTagSuggestion(t *testing.T) {
	e2e(t, useful(0), `Bare /tag opens the current tags without asking the model. Navigate to /c/tag-original and wait for [data-testid="message-input"]. Fill it with "/tag" and click [data-testid="send-button"]. Wait for the "Edit tags" dialog; it shows "existing-tag". Call "suggestion-count" expecting "0".
Click "Suggest tags" and wait for "suggested-tag" in the dialog. Call "suggestion-count" expecting "1", and "stored-tags" expecting '["existing-tag"]'. Click "Save" and wait for the dialog to close. Call "stored-tags" expecting '["existing-tag","suggested-tag"]'.`, func(s *shelley) {
		c := s.conversation("echo: tag fixture", "tag-original")
		s.tagFilterSetTags(c.ID, "existing-tag")
		path := "/api/conversation/" + c.ID
		mockOrganizationSuggestion(s, path+"/tag-suggestions", map[string][]string{"tags": {"suggested-tag"}})
		s.call("stored-tags", func() string {
			var current struct {
				Conversation struct{ Tags string } `json:"conversation"`
			}
			s.api("GET", path, nil, &current)
			return current.Conversation.Tags
		})
	})
}

func mockOrganizationSuggestion(s *shelley, path string, result any) {
	var requests atomic.Int32
	s.route("POST "+path, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeJSON(w, result)
	})
	s.call("suggestion-count", func() string { return strconv.Itoa(int(requests.Load())) })
}
