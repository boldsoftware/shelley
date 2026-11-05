package server

import (
	"encoding/json"
	"testing"
)

// A project's page_url comes back from the API, and a PUT without it keeps it.
func TestProjectPageURLAPI(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	e.want(201, "POST", "/api/projects", `{"name":"hub","instructions":"","cwd":"","page_url":"https://hub.exe.dev"}`)
	page := func() string {
		t.Helper()
		var workspace struct {
			Projects []struct {
				Name    string `json:"name"`
				PageURL string `json:"page_url"`
			} `json:"projects"`
		}
		if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/workspace", "")), &workspace); err != nil {
			t.Fatal(err)
		}
		for _, project := range workspace.Projects {
			if project.Name == "hub" {
				return project.PageURL
			}
		}
		t.Fatal("no hub project")
		return ""
	}
	if got := page(); got != "https://hub.exe.dev" {
		t.Fatalf("page_url %q", got)
	}
	e.want(200, "PUT", "/api/projects/hub", `{"name":"hub","instructions":"Hub","cwd":""}`)
	if got := page(); got != "https://hub.exe.dev" {
		t.Fatalf("page_url after a PUT without it %q", got)
	}
	e.want(400, "PUT", "/api/projects/hub", `{"name":"hub","instructions":"","cwd":"","page_url":"hub.exe.dev"}`)
	e.want(200, "PUT", "/api/projects/hub", `{"name":"hub","instructions":"","cwd":"","page_url":""}`)
	if got := page(); got != "" {
		t.Fatalf("cleared page_url %q", got)
	}
}
