package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/skills"
)

func TestPendingDrainCoalescesWakeup(t *testing.T) {
	manager := &ConversationManager{}
	owner, done := manager.beginPendingDrain()
	if !owner {
		t.Fatal("first drain did not claim ownership")
	}
	secondOwner, secondDone := manager.beginPendingDrain()
	if secondOwner {
		t.Fatal("concurrent drain claimed duplicate ownership")
	}
	if secondDone != done {
		t.Fatal("concurrent drain did not return the active completion token")
	}
	if !manager.finishPendingDrainPass(done) {
		t.Fatal("concurrent wakeup did not request another drain pass")
	}
	select {
	case <-done:
		t.Fatal("drain completed before the coalesced pass")
	default:
	}
	if manager.finishPendingDrainPass(done) {
		t.Fatal("drain requested an extra pass without another wakeup")
	}
	<-done
}

func TestSubagentPromptCacheKeyUsesPromptAndModel(t *testing.T) {
	system := []llm.SystemContent{{Type: "text", Text: "stable prompt"}}
	first := subagentPromptCacheKey(system, "model-a")
	if first != subagentPromptCacheKey(system, "model-a") {
		t.Fatal("identical prompts and models produced different cache keys")
	}
	if first == subagentPromptCacheKey([]llm.SystemContent{{Type: "text", Text: "changed prompt"}}, "model-a") {
		t.Fatal("different prompts unexpectedly share a cache key")
	}
	if first == subagentPromptCacheKey(system, "model-b") {
		t.Fatal("different models unexpectedly share a cache key")
	}
}

func TestSystemPromptDisplayDataIncludesSourceMetadata(t *testing.T) {
	t.Parallel()
	displayData := systemPromptDisplayData(
		claudetool.ToolSetConfig{
			DisableAllTools: true,
			ToolOverrides:   map[string]string{"bash": "on"},
		},
		[]skills.Skill{
			{Name: "file-skill", Description: "From disk.", Path: "/tmp/file-skill/SKILL.md"},
			{Name: "schedule", Description: "Built in."},
		},
	)

	encoded, err := json.Marshal(displayData)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools []struct {
			Name       string `json:"name"`
			SourcePath string `json:"source_path"`
			Origin     string `json:"origin"`
		} `json:"tools"`
		Skills []struct {
			Name       string `json:"name"`
			Activate   string `json:"activate"`
			SourcePath string `json:"source_path"`
			Origin     string `json:"origin"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Tools) != 1 || got.Tools[0].Name != "bash" {
		t.Fatalf("tools = %+v, want bash only", got.Tools)
	}
	if got.Tools[0].SourcePath != "claudetool/bash.go" || got.Tools[0].Origin != "Shelley" {
		t.Errorf("bash metadata = %+v", got.Tools[0])
	}
	if len(got.Skills) != 2 {
		t.Fatalf("skills = %+v", got.Skills)
	}
	if got.Skills[0].SourcePath != "/tmp/file-skill/SKILL.md" || got.Skills[0].Origin != "File" {
		t.Errorf("file skill metadata = %+v", got.Skills[0])
	}
	if got.Skills[1].SourcePath != "skills/builtin/schedule/SKILL.md" || got.Skills[1].Origin != "Built into Shelley" {
		t.Errorf("built-in skill metadata = %+v", got.Skills[1])
	}
	if got.Skills[1].Activate != "shelley skill cat schedule" {
		t.Errorf("schedule activation = %q", got.Skills[1].Activate)
	}
}

func TestHydrateGeneratesSystemPromptWithSubagentTool(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	ctx := t.Context()

	// Create a new conversation
	h.NewConversation("Hello", "")
	convID := h.ConversationID()

	// The system prompt should have been created during NewConversation (via handleNewConversation -> getOrCreateConversationManager -> Hydrate)
	// Let's verify it has the subagent tool in its display data.

	var messages []generated.Message
	err := h.db.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(ctx, convID)
		return qerr
	})
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}

	var systemMsg *generated.Message
	for _, msg := range messages {
		if msg.Type == string(db.MessageTypeSystem) {
			systemMsg = &msg
			break
		}
	}

	if systemMsg == nil {
		t.Fatal("System message not found")
	}

	if systemMsg.DisplayData == nil {
		t.Fatal("System message has no display data")
	}

	var displayData struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(*systemMsg.DisplayData), &displayData); err != nil {
		t.Fatalf("Failed to unmarshal display data: %v", err)
	}

	hasSubagent := false
	for _, tool := range displayData.Tools {
		if tool.Name == "subagent" {
			hasSubagent = true
			break
		}
	}

	if !hasSubagent {
		t.Errorf("System prompt display data should include 'subagent' tool")
		t.Logf("Found tools: %v", displayData.Tools)
	}
}

func TestHydrateSystemPromptDisplayDataRespectsToolOverrides(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	chatBody := `{"message":"Hello","model":"predictable","conversation_options":{"tool_overrides":{"bash":"off"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/new", strings.NewReader(chatBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleNewConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	messages, err := h.db.QueriesTxRes(t.Context(), func(q *generated.Queries) ([]generated.Message, error) {
		return q.ListMessages(t.Context(), resp.ConversationID)
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}

	var systemMsg *generated.Message
	for _, msg := range messages {
		if msg.Type == string(db.MessageTypeSystem) {
			systemMsg = &msg
			break
		}
	}
	if systemMsg == nil {
		t.Fatal("system message not found")
	}
	if systemMsg.DisplayData == nil {
		t.Fatal("system message has no display data")
	}

	var displayData struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(*systemMsg.DisplayData), &displayData); err != nil {
		t.Fatalf("unmarshal display data: %v", err)
	}

	var hasBash, hasPatch bool
	for _, tool := range displayData.Tools {
		switch tool.Name {
		case "bash":
			hasBash = true
		case "patch":
			hasPatch = true
		}
	}
	if hasBash {
		t.Fatalf("display data should not include disabled bash tool: %+v", displayData.Tools)
	}
	if !hasPatch {
		t.Fatalf("display data should include default-on patch tool: %+v", displayData.Tools)
	}
}

func TestSystemPromptDisplayDataUsesIntegrationSkillMetadata(t *testing.T) {
	t.Parallel()
	displayData := systemPromptDisplayData(claudetool.ToolSetConfig{DisableAllTools: true}, []skills.Skill{{
		Name:        "remote-skill",
		Description: "Remote.",
		Activate:    "curl -fsS https://remote.int.example/",
		Source:      "https://remote.int.example/",
		Origin:      "Integration",
	}})
	encoded, err := json.Marshal(displayData)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Skills []struct {
			Activate   string `json:"activate"`
			SourcePath string `json:"source_path"`
			Origin     string `json:"origin"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 1 || got.Skills[0].Activate != "curl -fsS https://remote.int.example/" || got.Skills[0].SourcePath != "https://remote.int.example/" || got.Skills[0].Origin != "Integration" {
		t.Fatalf("integration skill metadata = %+v", got.Skills)
	}
}
