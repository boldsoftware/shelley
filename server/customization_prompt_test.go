package server

import (
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

func TestShelleyConversationReceivesVerificationWorkflowInShelleyProject(t *testing.T) {
	s, database, _ := newTestServer(t)
	db.EnableWorkspaceMode(t, s.db)
	if _, err := s.ensureWorkspace(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Shelley's project is the one in the canonical customization checkout.
	cwd := t.TempDir()
	s.customizations.deps.canonicalDir = func() (string, error) { return cwd, nil }
	shelley, err := database.GetProject(t.Context(), "Shelley")
	if err != nil {
		t.Fatal(err)
	}
	shelley.Cwd = cwd
	if err := database.UpdateProject(t.Context(), "Shelley", *shelley); err != nil {
		t.Fatal(err)
	}
	conversation, err := database.CreateConversation(t.Context(), nil, true, &cwd, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(t.Context(), conversation.ConversationID, "Shelley"); err != nil {
		t.Fatal(err)
	}
	if workspace := workspaceResponse[db.Workspace](t, workspaceAPI(s, "GET", "/api/workspace", ""), http.StatusOK); workspace.ShelleyProject != "Shelley" {
		t.Fatalf("shelley_project=%q", workspace.ShelleyProject)
	}
	instructions, err := s.workspaceInstructions(t.Context(), conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		shelleyCustomizationPrompt,
		"/customization/verify",
		"/customization/restart",
		"artifact_sha256",
		"ID/customization until",
	} {
		if !strings.Contains(instructions, required) {
			t.Fatalf("missing workflow instruction %q", required)
		}
	}
	db.AddTestProject(t, database, "my-project")
	if err := database.MoveConversation(t.Context(), conversation.ConversationID, "my-project"); err != nil {
		t.Fatal(err)
	}
	other, err := s.workspaceInstructions(t.Context(), conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(other, shelleyCustomizationPrompt) {
		t.Fatal("Shelley-specific verification required on a non-Shelley conversation")
	}
}
