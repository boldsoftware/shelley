package models

import (
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm/ant"
)

func TestCustomModelServiceSnapshots(t *testing.T) {
	database, cleanup := db.NewTestDB(t)
	t.Cleanup(cleanup)
	params := generated.UpdateModelParams{
		ModelID: "custom", DisplayName: "Custom", ProviderType: "anthropic",
		Endpoint: "https://fixture.invalid", ApiKey: "old", ModelName: "claude-opus-4-7",
	}
	if _, err := database.CreateModel(t.Context(), generated.CreateModelParams{
		ModelID: params.ModelID, DisplayName: params.DisplayName, ProviderType: params.ProviderType,
		Endpoint: params.Endpoint, ApiKey: params.ApiKey, ModelName: params.ModelName,
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(&Config{DB: database})
	if err != nil {
		t.Fatal(err)
	}
	_, _, previous, err := manager.GetServiceSnapshot(params.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		change  func()
		changed bool
	}{
		{"label", func() { params.DisplayName = "Renamed" }, false},
		{"key", func() { params.ApiKey = "rotated" }, true},
		{"endpoint", func() { params.Endpoint = "https://other.invalid" }, true},
		{"wire model", func() { params.ModelName = "claude-sonnet-5-5" }, true},
		{"protocol", func() { params.ProviderType = "openai-responses" }, true},
	} {
		tc.change()
		if _, err := database.UpdateModel(t.Context(), params); err != nil {
			t.Fatal(err)
		}
		// Do not publish: snapshots must observe persisted changes even then.
		svc, _, revision, err := manager.GetServiceSnapshot(params.ModelID)
		if err != nil || (revision != previous) != tc.changed {
			t.Fatalf("%s: revision=%d previous=%d err=%v", tc.name, revision, previous, err)
		}
		if raw, ok := svc.(*loggingService).service.(*reasoningService).Service.(*ant.Service); ok &&
			(raw.APIKey != params.ApiKey || raw.Model != params.ModelName) {
			t.Fatalf("%s: snapshot service has stale credentials or model", tc.name)
		}
		previous = revision
	}
	if err := database.DeleteModel(t.Context(), params.ModelID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := manager.GetServiceSnapshot(params.ModelID); err == nil {
		t.Fatal("deleted custom model resolved from stale registry")
	}
}
