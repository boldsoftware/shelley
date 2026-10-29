package server

import (
	"encoding/json"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// A project is a profile: its conversations take, and follow, the profile's settings.
func TestProjectProfiles(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	e.want(201, "POST", "/api/profiles", `{"name":"Terse","model":"model-b","tool_overrides":{"bash":"off"},"system_prompt":"Be terse."}`)
	cwd := t.TempDir()

	// A new project's profile copies the default profile's settings, or
	// copy_settings_from's, once.
	e.want(400, "POST", "/api/projects", `{"name":"Docs","copy_settings_from":"Nope"}`)
	e.want(409, "POST", "/api/projects", `{"name":"Terse"}`)
	e.want(201, "POST", "/api/projects", `{"name":"Plain","cwd":"`+cwd+`"}`)
	e.want(201, "POST", "/api/projects", `{"name":"Docs","cwd":"`+cwd+`","instructions":"Write docs.","copy_settings_from":"Terse"}`)
	e.want(200, "PUT", "/api/profiles/Terse", `{"model":"model-a","system_prompt":"Changed."}`)
	p := e.profiles()
	if !p["Plain"].Project || p["Plain"].Default || !p["Plain"].Settings.equal(p["Default"].Settings) || p["Default"].Project {
		t.Fatalf("Plain: %+v, Default: %+v", p["Plain"], p["Default"])
	}
	if docs := p["Docs"]; !docs.Project || docs.Model != "model-b" || docs.SystemPrompt != "Be terse." || docs.ToolOverrides["bash"] != "off" {
		t.Fatalf("Docs: %+v", docs)
	}
	if body := e.want(409, "DELETE", "/api/profiles/Docs", ""); !strings.Contains(body, "project") {
		t.Fatalf("delete of a project's profile: %s", body)
	}

	startConversation := func() string {
		t.Helper()
		var created struct {
			ConversationID string `json:"conversation_id"`
		}
		if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/new", `{"project":"Docs","message":"hello"}`)), &created); err != nil {
			t.Fatal(err)
		}
		e.waitTurn(created.ConversationID, 0)
		return created.ConversationID
	}
	settingsOf := func(id string) ConversationSettings {
		t.Helper()
		var settings ConversationSettings
		if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/conversation/"+id+"/settings", "")), &settings); err != nil {
			t.Fatal(err)
		}
		return settings
	}
	projectOf := func(id string) string {
		t.Helper()
		metadata, err := e.database.GetWorkspaceConversation(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return metadata.Project
	}
	turn := func(id string) *llm.Request {
		t.Helper()
		n := len(listMessages(t, e.database, id))
		e.want(202, "POST", "/api/conversation/"+id+"/chat", `{"message":"echo: again"}`)
		e.waitTurn(id, n)
		return e.lastTurnRequest()
	}

	id := startConversation()
	if st := settingsOf(id); st.Profile != "Docs" || st.Model != "model-b" || st.ToolOverrides["bash"] != "off" {
		t.Fatalf("conversation settings: %+v", st)
	}
	req := e.lastTurnRequest()
	if system := systemText(req); !strings.HasPrefix(system, "Be terse.") || !strings.Contains(system, "<project_instructions>\nWrite docs.") || hasTool(req, "bash") {
		t.Fatalf("first turn: system %q, bash %v", system, hasTool(req, "bash"))
	}
	// Outside workspace mode a project brings its instructions, not the
	// workspace; in it, both.
	if system := systemText(req); strings.Contains(system, "<workspace>") {
		t.Fatalf("workspace prompt outside workspace mode: %q", system)
	}
	e.want(200, "POST", "/feature-flags", `{"name":"workspace","value":true}`)
	if system := systemText(turn(id)); !strings.Contains(system, "<workspace>") || !strings.Contains(system, "<project_instructions>\nWrite docs.") {
		t.Fatalf("workspace mode prompt: %q", system)
	}

	// Conversations don't switch profiles into or out of a project; they move.
	e.want(409, "POST", "/api/conversation/"+id+"/settings", `{"profile":"Default"}`)
	var plain struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+cwd+`"}`)), &plain); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(plain.ConversationID, 0)
	if _, err := e.database.GetWorkspaceConversation(t.Context(), plain.ConversationID); err == nil {
		t.Fatal("a conversation in the default profile joined a project")
	}
	e.want(409, "POST", "/api/conversation/"+plain.ConversationID+"/settings", `{"profile":"Docs"}`)

	// Conversations follow edits of the project's profile unless their settings
	// were changed.
	own := startConversation()
	e.want(200, "POST", "/api/conversation/"+own+"/settings", `{"tool_overrides":{"browser":"off"}}`)
	e.want(200, "PUT", "/api/profiles/Docs", `{"model":"model-a","system_prompt":"Be brief."}`)
	req = turn(id)
	if system := systemText(req); !strings.HasPrefix(system, "Be brief.") || !strings.Contains(system, "<project_instructions>\nWrite docs.") || !hasTool(req, "bash") {
		t.Fatalf("following conversation: system %q, bash %v", system, hasTool(req, "bash"))
	}
	if st := settingsOf(id); st.Profile != "Docs" || st.Model != "model-a" {
		t.Fatalf("following conversation settings: %+v", st)
	}
	if system := systemText(turn(own)); !strings.HasPrefix(system, "Be terse.") {
		t.Fatalf("edited conversation followed: %q", system)
	}

	// Moving a conversation relabels it.
	db.AddTestProject(t, e.database, "my-project")
	e.want(200, "PUT", "/api/conversation/"+id+"/project", `{"project":"my-project"}`)
	if st := settingsOf(id); st.Profile != "my-project" || projectOf(id) != "my-project" {
		t.Fatalf("moved conversation: %+v in %q", st, projectOf(id))
	}
	if system := systemText(turn(id)); strings.Contains(system, "Be brief.") || !strings.Contains(system, "<project_instructions>\nInstructions of my-project.") {
		t.Fatalf("moved conversation's system prompt: %q", system)
	}

	// A conversation in no project gets the project's instructions from its
	// first turn after a move.
	if system := systemText(turn(plain.ConversationID)); strings.Contains(system, "<project_instructions>") {
		t.Fatalf("conversation in no project: %q", system)
	}
	e.want(200, "PUT", "/api/conversation/"+plain.ConversationID+"/project", `{"project":"my-project"}`)
	if system := systemText(turn(plain.ConversationID)); !strings.Contains(system, "<project_instructions>\nInstructions of my-project.") {
		t.Fatalf("conversation moved into a project: %q", system)
	}

	// Renaming a project renames its profile, and relabels its conversations.
	e.want(200, "PUT", "/api/projects/Docs", `{"name":"Documentation","cwd":"`+cwd+`","instructions":"Write docs."}`)
	if p := e.profiles(); !p["Documentation"].Project || p["Documentation"].SystemPrompt != "Be brief." {
		t.Fatalf("renamed profile: %+v", p)
	} else if _, ok := p["Docs"]; ok {
		t.Fatal("old profile name left")
	}
	if st := settingsOf(own); st.Profile != "Documentation" || projectOf(own) != "Documentation" {
		t.Fatalf("renamed project's conversation: %+v in %q", st, projectOf(own))
	}

	// A conversation started in a project's profile is a member of it, with
	// its instructions.
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+cwd+`","conversation_options":{"profile":"Documentation"}}`)), &plain); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(plain.ConversationID, 0)
	if projectOf(plain.ConversationID) != "Documentation" || !strings.Contains(systemText(e.lastTurnRequest()), "<project_instructions>\nWrite docs.") {
		t.Fatalf("conversation in a project's profile: in %q", projectOf(plain.ConversationID))
	}
	// A project's profile can't be the default.
	e.want(409, "POST", "/api/profiles/Documentation/default", "")
}
