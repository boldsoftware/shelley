package server

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/skills"
)

func TestDiscoverIntegrationSkills(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}

	var requestedMu sync.Mutex
	var requested []string
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestedMu.Lock()
		requested = append(requested, req.URL.String())
		requestedMu.Unlock()
		body := ""
		status := http.StatusOK
		switch req.URL.String() {
		case "https://reflection.int.example.test/integrations":
			body = `{"integrations":[
				{"name":"z-personal","type":"file","attributes":["type:shelley-skill"]},
				{"name":"a-team","type":"file","team":true,"attributes":["type:shelley-skill","other:value"]},
				{"name":"beta","type":"file","attributes":["type:shelley-skill"]},
				{"name":"broken","type":"file","attributes":["type:shelley-skill"]},
				{"name":"missing","type":"file","attributes":["type:shelley-skill"]},
				{"name":"ignored-near-match","type":"file","attributes":["prefix:type:shelley-skill"]},
				{"name":"bad;touch-x","type":"file","attributes":["type:shelley-skill"]},
				{"name":"ignored-type","type":"http-proxy","attributes":["type:shelley-skill"]}
			]}`
		case "https://a-team.team.example.test/":
			body = "---\nname: shared\ndescription: First duplicate.\nlicense: MIT\nmetadata:\n  owner: team\n---\nteam body\n"
		case "https://beta.int.example.test/":
			body = "---\nname: beta\ndescription: Beta skill.\n---\nbeta body\n"
		case "https://broken.int.example.test/":
			body = "not frontmatter"
		case "https://missing.int.example.test/":
			status = http.StatusNotFound
			body = "missing"
		case "https://z-personal.int.example.test/":
			body = "---\nname: shared\ndescription: Later duplicate.\n---\npersonal body\n"
		default:
			return nil, errors.New("unexpected request: " + req.URL.String())
		}
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	got := discoverIntegrationSkills(t.Context(), httpc, logger, env)
	if len(got) != 2 {
		t.Fatalf("skills = %+v", got)
	}
	if got[0].Name != "beta" || got[1].Name != "shared" {
		t.Fatalf("skill order = [%s, %s], want [beta, shared]", got[0].Name, got[1].Name)
	}
	shared := got[1]
	if shared.Description != "Later duplicate." {
		t.Fatalf("shared metadata = %+v", shared)
	}
	if shared.Source != "https://z-personal.int.example.test/" || shared.Origin != "Integration" || shared.ActivationCommand() != "curl -fsS --max-time 5 -- https://z-personal.int.example.test/" {
		t.Fatalf("shared source metadata = %+v", shared)
	}

	for _, unexpected := range []string{"ignored-near-match", "bad;touch-x", "ignored-type"} {
		for _, request := range requested {
			if strings.Contains(request, unexpected) {
				t.Fatalf("non-matching integration fetched: %s", request)
			}
		}
	}
	for _, warning := range []string{"malformed reflection entry", "malformed SKILL.md", "skill fetch failed", "duplicate skill name"} {
		if !strings.Contains(logs.String(), warning) {
			t.Errorf("logs missing %q:\n%s", warning, logs.String())
		}
	}
}

func TestDiscoverIntegrationSkillsReflectionFailureWarns(t *testing.T) {
	env, err := exeenv.New("http", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	got := discoverIntegrationSkills(t.Context(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}, slog.New(slog.NewTextHandler(&logs, nil)), env)
	if len(got) != 0 {
		t.Fatalf("skills = %+v", got)
	}
	if !strings.Contains(logs.String(), "reflection fetch failed") {
		t.Fatalf("warning missing: %s", logs.String())
	}
}

func TestServerPassesIntegrationSkillSnapshotToConversationManagers(t *testing.T) {
	server, database, _ := newTestServer(t)
	server.integrationSkills = []skills.Skill{{Name: "snapshot-skill", Description: "Snapshot."}}

	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parentManager, err := server.getOrCreateConversationManager(t.Context(), parent.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(parentManager.integrationSkills) != 1 || parentManager.integrationSkills[0].Name != "snapshot-skill" {
		t.Fatalf("top-level snapshot = %+v", parentManager.integrationSkills)
	}

	subagent, err := database.CreateSubagentConversation(t.Context(), "snapshot-subagent", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	subagentManager, err := server.getOrCreateSubagentConversationManager(t.Context(), subagent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subagentManager.integrationSkills) != 1 || subagentManager.integrationSkills[0].Name != "snapshot-skill" {
		t.Fatalf("subagent snapshot = %+v", subagentManager.integrationSkills)
	}
}

func TestShouldDiscoverIntegrationSkills(t *testing.T) {
	injected := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unused")
	})}
	if shouldDiscoverIntegrationSkills(true, http.DefaultClient) {
		t.Fatal("ordinary tests must not use the default client for reflection discovery")
	}
	if !shouldDiscoverIntegrationSkills(true, injected) {
		t.Fatal("tests with an injected client must be able to exercise discovery")
	}
	if !shouldDiscoverIntegrationSkills(false, http.DefaultClient) {
		t.Fatal("production startup must discover with the default client")
	}
}
