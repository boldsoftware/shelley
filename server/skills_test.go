package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/skills"
)

func skillsRequest(t *testing.T, handler http.Handler, cwd, id string) (*httptest.ResponseRecorder, SkillsResponse) {
	t.Helper()
	query := url.Values{"cwd": {cwd}}
	if id != "" {
		query.Set("conversation_id", id)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/skills?"+query.Encode(), nil))
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("catalog responses must not be cached across contexts")
	}
	var response SkillsResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Skills == nil {
			t.Fatal("skills must be an array, not null")
		}
	}
	return w, response
}

func writeCatalogSkill(t *testing.T, root, name, description, when string) string {
	t.Helper()
	path := filepath.Join(root, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: "+name+"\ndescription: "+description+"\nwhen: "+when+"\n---\nPRIVATE INSTRUCTION BODY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSkillsDraftDiscovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server, database, _ := newTestServer(t)
	cwd := t.TempDir()
	if out, err := exec.Command("git", "init", cwd).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	selected := filepath.Join(cwd, "nested")
	if err := os.Mkdir(selected, 0o755); err != nil {
		t.Fatal(err)
	}
	file := writeCatalogSkill(t, filepath.Join(cwd, ".skills"), "catalog-file", "Project skill.", "")
	writeCatalogSkill(t, filepath.Join(cwd, ".skills"), "catalog-hidden", "Filtered skill.", "unknown-environment")
	writeCatalogSkill(t, filepath.Join(cwd, ".skills"), "catalog-exe", "VM skill.", "exe.dev")
	user := writeCatalogSkill(t, filepath.Join(os.Getenv("HOME"), ".config", "agents", "skills"), "catalog-user", "User skill.", "")
	integration := skills.Skill{Name: "catalog-remote", Description: "Remote skill.", Activate: "curl -s https://remote.example/", Source: "https://remote.example/", Origin: "Integration", Body: "PRIVATE REMOTE BODY"}
	integrations := []skills.Skill{integration, {Name: "catalog-file", Description: "Shadowed remote skill."}}
	server.integrationSkills = newIntegrationSkillCache(server.logger, func(context.Context, []skills.Skill) ([]skills.Skill, error) {
		return integrations, nil
	})
	draft, err := database.CreateDraftConversation(t.Context(), &cwd, nil, db.ConversationOptions{}, "unsent")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", draft.ConversationID} {
		t.Run("conversation="+id, func(t *testing.T) {
			w, response := skillsRequest(t, http.HandlerFunc(server.handleSkills), selected, id)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			want := make([]SkillCatalogEntry, 0)
			for _, skill := range collectSkills(selected, "", integrations, skills.Env{ExeDev: isExeDev()}) {
				want = append(want, skillCatalogEntry(skill))
			}
			if !reflect.DeepEqual(response.Skills, want) {
				t.Fatalf("catalog differs from prompt discovery:\ngot %+v\nwant %+v", response.Skills, want)
			}
			byName := make(map[string]SkillCatalogEntry)
			for _, skill := range response.Skills {
				byName[skill.Name] = skill
			}
			for _, tc := range []struct{ name, path, origin string }{
				{"catalog-file", file, "File"},
				{"catalog-user", user, "File"},
				{"catalog-remote", integration.Source, "Integration"},
				{"schedule", "skills/builtin/schedule/SKILL.md", "Built into Shelley"},
			} {
				got, ok := byName[tc.name]
				if !ok || got.SourcePath != tc.path || got.Origin != tc.origin {
					t.Errorf("%s source = %+v", tc.name, got)
				}
			}
			if _, ok := byName["catalog-hidden"]; ok {
				t.Error("unknown environment skill was not filtered")
			}
			if _, ok := byName["catalog-exe"]; ok != isExeDev() {
				t.Error("exe.dev filtering differs from current environment")
			}
			if strings.Contains(w.Body.String(), "PRIVATE") || strings.Contains(w.Body.String(), `"body"`) {
				t.Fatal("instruction bodies leaked")
			}
		})
	}
	messages, err := database.ListMessages(t.Context(), draft.ConversationID)
	if err != nil || len(messages) != 0 || len(server.activeConversations) != 0 {
		t.Fatalf("read created conversation state: messages=%d managers=%d err=%v", len(messages), len(server.activeConversations), err)
	}
}

func TestSkillsConversationSnapshot(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	cwd := t.TempDir()
	parent, err := database.CreateConversation(t.Context(), nil, true, &cwd, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "child", parent.ConversationID, &cwd)
	if err != nil {
		t.Fatal(err)
	}
	// Snapshot reads must never consult current integrations or filesystem.
	server.integrationSkills = &integrationSkillCache{discover: func(context.Context, []skills.Skill) ([]skills.Skill, error) {
		t.Fatal("conversation snapshot rediscovered integrations")
		return nil, nil
	}}
	for _, conversation := range []*generated.Conversation{parent, child} {
		t.Run(conversation.ConversationID, func(t *testing.T) {
			id := conversation.ConversationID
			add := func(prompt string, metadata any, excluded bool) {
				t.Helper()
				_, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
					ConversationID: id, Type: db.MessageTypeSystem, LLMData: llm.UserStringMessage(prompt), DisplayData: metadata, ExcludedFromContext: excluded,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			add(wrapSkillCatalog(skills.ToPromptXML([]skills.Skill{{Name: "old-generation"}})), nil, false)
			if err := database.QueriesTx(t.Context(), func(q *generated.Queries) error {
				_, err := q.IncrementConversationGeneration(t.Context(), id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			add(wrapSkillCatalog(skills.ToPromptXML([]skills.Skill{{Name: "older-current-prompt"}})), nil, false)
			original := []skills.Skill{
				{Name: "retained", Description: "Original.", Path: "/gone/SKILL.md"},
				{Name: "removed-by-hook", Description: "Removed."},
				{Name: "changed-by-hook", Activate: "old command", Source: "old source", Origin: "Integration"},
			}
			postHook := []skills.Skill{
				{Name: "retained", Description: "Post-hook description."},
				{Name: "changed-by-hook", Description: "Changed.", Activate: "new command"},
				{Name: "added-by-hook", Description: "Added < & >.", Activate: "curl -s https://new.example/?x=1&y=2"},
			}
			add("PRIVATE SYSTEM TEXT\n"+wrapSkillCatalog(skills.ToPromptXML(postHook)), systemPromptDisplayData(claudetool.ToolSetConfig{DisableAllTools: true}, original), false)
			add(wrapSkillCatalog(skills.ToPromptXML([]skills.Skill{{Name: "excluded"}})), nil, true)
			before, err := database.ListMessages(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			w, response := skillsRequest(t, http.HandlerFunc(server.handleSkills), cwd, id)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			want := []SkillCatalogEntry{
				{Name: "retained", Description: "Post-hook description.", Activate: "shelley skill cat retained", SourcePath: "/gone/SKILL.md", Origin: "File"},
				{Name: "changed-by-hook", Description: "Changed.", Activate: "new command"},
				{Name: "added-by-hook", Description: "Added < & >.", Activate: "curl -s https://new.example/?x=1&y=2"},
			}
			if !reflect.DeepEqual(response.Skills, want) {
				t.Fatalf("got %+v, want %+v", response.Skills, want)
			}
			if strings.Contains(w.Body.String(), "PRIVATE") {
				t.Fatal("full prompt leaked")
			}
			after, err := database.ListMessages(t.Context(), id)
			if err != nil || !reflect.DeepEqual(before, after) || len(server.activeConversations) != 0 {
				t.Fatalf("catalog read changed conversation state: %v", err)
			}
		})
	}
}

func TestSkillsErrors(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	cwd := t.TempDir()
	file := filepath.Join(cwd, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	create := func(prompt any) string {
		t.Helper()
		conversation, err := database.CreateConversation(t.Context(), nil, true, &cwd, nil, db.ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if prompt != nil {
			if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{ConversationID: conversation.ConversationID, Type: db.MessageTypeSystem, LLMData: prompt}); err != nil {
				t.Fatal(err)
			}
		}
		return conversation.ConversationID
	}
	missingPrompt := create(nil)
	oldPrompt := create(llm.UserStringMessage(wrapSkillCatalog(skills.ToPromptXML([]skills.Skill{{Name: "old"}}))))
	if err := database.QueriesTx(t.Context(), func(q *generated.Queries) error {
		_, err := q.IncrementConversationGeneration(t.Context(), oldPrompt)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	malformedPrompt := create(llm.UserStringMessage(wrapSkillCatalog("<available_skills><skill>")))
	malformedMessage := create("not an LLM message")
	noSkills := create(llm.UserStringMessage("A hook replaced the entire catalog."))
	for _, tc := range []struct {
		name, cwd, id string
		status        int
	}{
		{"missing cwd", "", "", 400},
		{"relative cwd", ".", "", 400},
		{"missing directory", filepath.Join(cwd, "missing"), "", 400},
		{"file cwd", file, "", 400},
		{"unknown conversation", cwd, "nonexistent", 404},
		{"missing prompt", cwd, missingPrompt, 409},
		{"only old generation prompt", cwd, oldPrompt, 409},
		{"malformed prompt", cwd, malformedPrompt, 500},
		{"malformed message", cwd, malformedMessage, 500},
		{"hook removed skills", cwd, noSkills, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, response := skillsRequest(t, http.HandlerFunc(server.handleSkills), tc.cwd, tc.id)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s; want %d", w.Code, w.Body.String(), tc.status)
			}
			if tc.status == 200 && len(response.Skills) != 0 {
				t.Fatalf("absent catalog must not rediscover: %+v", response.Skills)
			}
		})
	}
}

func TestSkillsRoute(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	w, _ := skillsRequest(t, mux, "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("GET route status = %d", w.Code)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/skills", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", w.Code)
	}
}

func wrapSkillCatalog(catalog string) string {
	return "<skills>\nWhen a task matches a skill's description, run its activation command to read the instructions.\n" + catalog + "\n</skills>"
}

func TestParsePromptSkills(t *testing.T) {
	t.Parallel()
	valid := skills.ToPromptXML([]skills.Skill{{Name: "test", Description: "Use <this> & 'that'.", Activate: "printf 'a&b'"}})
	wrapped := wrapSkillCatalog(valid)
	example := wrapSkillCatalog(skills.ToPromptXML([]skills.Skill{{Name: "example", Description: "Not a real skill."}}))
	guidance := func(text string) string {
		return "<guidance>\n<root_guidance file=\"/project/AGENTS.md\">\n" + text + "\n</root_guidance>\n</guidance>\n"
	}
	for _, tc := range []struct {
		name, prompt string
		count        int
		wantError    bool
	}{
		{"empty", "", 0, false},
		{"arbitrary prompt markup", "<bad tag>\n" + wrapped + "\n<not XML", 1, false},
		{"literal opening in guidance", guidance("The catalog uses <available_skills>.") + wrapped, 1, false},
		{"complete example in guidance", guidance(example) + wrapped, 1, false},
		{"quoted guidance closing tag", guidance("The closing tag is:\n</guidance>\n"+example) + wrapped, 1, false},
		{"unclosed fence in guidance", guidance("```xml\n"+example) + wrapped, 1, false},
		{"hook removed actual catalog", guidance(example), 0, false},
		{"hook removed catalog contents", guidance(example) + wrapSkillCatalog(""), 0, false},
		{"hook replaced actual catalog", example + "\n" + wrapped, 1, false},
		{"fenced example before actual", "```xml\n" + example + "\n```\n" + wrapped, 1, false},
		{"fenced example after actual", wrapped + "\n~~~~xml\n" + example + "\n~~~~~", 1, false},
		{"unwrapped example", valid, 0, false},
		{"inline opening", "The <skills> and <available_skills> tags delimit skills.", 0, false},
		{"empty block", wrapSkillCatalog("<available_skills></available_skills>"), 0, false},
		{"unterminated wrapper", "<skills>\n" + valid, 0, true},
		{"unterminated catalog", wrapSkillCatalog("<available_skills>"), 0, true},
		{"malformed xml", wrapSkillCatalog("<available_skills><skill></available_skills>"), 0, true},
		{"malformed actual after valid guidance", guidance(example) + wrapSkillCatalog("<available_skills><skill>"), 0, true},
		{"malformed final section", wrapped + "\n<skills>\n<available_skills>", 0, true},
		{"missing activation", wrapSkillCatalog("<available_skills><skill><name>x</name></skill></available_skills>"), 0, true},
		{"missing name", wrapSkillCatalog("<available_skills><skill><activate>x</activate></skill></available_skills>"), 0, true},
		{"duplicate", wrapSkillCatalog(valid + valid), 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePromptSkills(tc.prompt)
			if (err != nil) != tc.wantError || len(got) != tc.count {
				t.Fatalf("got %+v, %v", got, err)
			}
			if !tc.wantError && got == nil {
				t.Fatal("empty catalog must not be nil")
			}
			if len(got) == 1 && (got[0].Name != "test" || got[0].Description != "Use <this> & 'that'." || got[0].Activate != "printf 'a&b'") {
				t.Fatalf("wrong catalog or XML entities not decoded: %+v", got)
			}
		})
	}
}

func TestParsePromptSkillsTemplates(t *testing.T) {
	t.Parallel()
	catalog := skills.ToPromptXML([]skills.Skill{{Name: "actual", Description: "Actual skill."}})
	example := wrapSkillCatalog(skills.ToPromptXML([]skills.Skill{{Name: "example"}}))
	for _, tc := range []struct {
		name, source string
		data         any
	}{
		{"top-level", systemPromptTemplate, SystemPromptData{
			SkillsXML: catalog,
			Codebase: &CodebaseInfo{
				InjectFiles: []string{"AGENTS.md", "CLAUDE.md"},
				InjectFileContents: map[string]string{
					"AGENTS.md": "The catalog uses <available_skills>.\n</guidance>\n" + example,
					"CLAUDE.md": "Here is another example:\n" + example,
				},
			},
		}},
		{"child", subagentSystemPromptTemplate, SubagentSystemPromptData{SkillsXML: catalog, WorkingDirectory: "/project", ShelleyDBPath: "/tmp/shelley.db"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := template.New(tc.name).Parse(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			var prompt strings.Builder
			if err := tmpl.Execute(&prompt, tc.data); err != nil {
				t.Fatal(err)
			}
			got, err := parsePromptSkills(prompt.String())
			if err != nil || len(got) != 1 || got[0].Name != "actual" {
				t.Fatalf("template catalog = %+v, err=%v", got, err)
			}
		})
	}
}
