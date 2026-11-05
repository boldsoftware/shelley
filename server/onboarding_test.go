package server

import (
	"context"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
)

func TestOnboarding(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	prod, err := exeenv.New("https", "exe.xyz")
	if err != nil {
		t.Fatal(err)
	}
	staging, err := exeenv.New("https", "exe-staging.xyz")
	if err != nil {
		t.Fatal(err)
	}
	integrations := []reflectionIntegration{{Name: "box-exe", Type: "exe-dev", Team: true}}
	env := prod
	s.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return env, integrations, nil
	}
	get := func() onboarding {
		t.Helper()
		return workspaceResponse[onboarding](t, workspaceAPI(s, "GET", "/api/onboarding", ""), 200)
	}
	// The cards are workspace mode's.
	if code := workspaceAPI(s, "GET", "/api/onboarding", "").Code; code != 404 {
		t.Fatalf("outside workspace mode: %d", code)
	}
	db.EnableWorkspaceMode(t, s.db)
	if err := s.loadWorkspace(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := onboarding{TextNumber: exeTextLine, ExeDev: "box-exe", ExeDevHost: "box-exe.team.exe.xyz", VMHost: "NAME.exe.xyz"}
	if got := get(); got != want {
		t.Fatalf("not connected: %+v", got)
	}
	integrations = append(integrations, reflectionIntegration{Name: "messages", Type: "messages"})
	want.TextConnected = true
	if got := get(); got != want {
		t.Fatalf("connected: %+v", got)
	}
	// The texting line is production exe.dev's; hosts follow the environment.
	env = staging
	want = onboarding{ExeDev: "box-exe", ExeDevHost: "box-exe.team.exe-staging.xyz", VMHost: "NAME.exe-staging.xyz"}
	if got := get(); got != want {
		t.Fatalf("staging: %+v", got)
	}
	// Without an exe-dev integration, web apps use this VM's ports.
	integrations = nil
	if got := get(); got != (onboarding{}) {
		t.Fatalf("no exe-dev integration: %+v", got)
	}
}
