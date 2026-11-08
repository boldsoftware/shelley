package modelproviders

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/models"
)

func testManager(t *testing.T, httpc *http.Client) (*Manager, *db.DB) {
	t.Helper()
	database, cleanup := db.NewTestDB(t)
	t.Cleanup(cleanup)
	registry, err := models.NewManager(&models.Config{DB: database})
	if err != nil {
		t.Fatal(err)
	}
	return New(database, registry, httpc), database
}

func TestProviderLifecycleKeepsLastSuccessfulSnapshot(t *testing.T) {
	failing := false
	listing := `{"data":[{"id":"gpt-6.1-sol","name":"Sol","ignored":{"capabilities":"raw"}},{"id":"embedding-unknown"}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("discovery request path/auth = %q, %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if failing {
			http.Error(w, "secret and upstream details must stay private", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, listing)
	}))
	defer upstream.Close()
	m, database := testManager(t, upstream.Client())
	input := Input{ProviderType: "openai-responses", Endpoint: upstream.URL + "/v1/", APIKey: "secret", DisplayName: "Work"}
	created, err := m.Create(t.Context(), input)
	if err != nil || created.ProviderID == "" || created.HasAPIKey == false || created.ModelCount != 1 {
		t.Fatalf("create: %+v, %v", created, err)
	}
	input.Endpoint = upstream.URL + "/v1"
	if _, err := m.Create(t.Context(), input); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	input.APIKey = ""
	input.DisplayName = "Renamed"
	failing = true // A label-only edit must not contact the provider.
	edited, err := m.Update(t.Context(), created.ProviderID, input)
	if err != nil || edited.ModelCount != 1 || edited.DisplayName != "Renamed" {
		t.Fatalf("label edit: %+v, %v", edited, err)
	}
	if _, err := m.Refresh(t.Context(), created.ProviderID); err == nil || !errors.Is(err, ErrDiscovery) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "upstream details") {
		t.Fatalf("failed refresh returned %v", err)
	}
	saved, err := database.GetModelProvider(t.Context(), created.ProviderID)
	if err != nil || saved.APIKey != "secret" || saved.DisplayName != "Renamed" || saved.Version != 2 ||
		len(saved.Models) != 1 || saved.Models[0].Name != "Sol" {
		t.Fatalf("failed refresh changed snapshot: %+v, %v", saved, err)
	}
	failing = false
	listing = `{"data":[{"id":"gpt-6.1-sol"},{"id":"gpt-5.6-sol"}]}`
	refreshed, err := m.Refresh(t.Context(), created.ProviderID)
	if err != nil || refreshed.ModelCount != 2 || len(m.registry.GetAvailableModels()) != 2 {
		t.Fatalf("successful refresh did not replace catalog: %+v, %v", refreshed, err)
	}
	saved, err = database.GetModelProvider(t.Context(), created.ProviderID)
	if err != nil || saved.Version != 3 || len(saved.Models) != 2 {
		t.Fatalf("refresh was not persisted: %+v, %v", saved, err)
	}
	input.Endpoint = "https://another.example/v1"
	if _, err := m.Update(t.Context(), created.ProviderID, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("origin changed without key: %v", err)
	}
	if err := m.Delete(t.Context(), created.ProviderID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(t.Context(), created.ProviderID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refresh deleted account: %v", err)
	}
}

func TestProviderRefreshLosesStaleWrite(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			close(entered)
			<-release
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-6.1-sol"}]}`)
	}))
	defer upstream.Close()
	defer unblock()
	m, database := testManager(t, upstream.Client())
	input := Input{ProviderType: "openai-responses", Endpoint: upstream.URL + "/v1", APIKey: "secret", DisplayName: "Before"}
	created, err := m.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.Refresh(context.Background(), created.ProviderID)
		result <- err
	}()
	<-entered
	input.APIKey = ""
	input.DisplayName = "After"
	if _, err := m.Update(t.Context(), created.ProviderID, input); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-result; !errors.Is(err, ErrConflict) {
		t.Fatalf("stale refresh: %v", err)
	}
	saved, err := database.GetModelProvider(t.Context(), created.ProviderID)
	if err != nil || saved.Version != 2 || saved.DisplayName != "After" {
		t.Fatalf("stale refresh overwrote update: %+v, %v", saved, err)
	}
}

func TestProviderCheckDoesNotSave(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("check request path/auth = %q, %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-6.1-sol","name":"Sol"},{"id":"embedding-unknown"}]}`)
	}))
	defer upstream.Close()
	m, database := testManager(t, upstream.Client())
	input := Input{ProviderType: "openai", Endpoint: upstream.URL + "/v1", APIKey: "secret", DisplayName: "Work"}
	found, err := m.Check(t.Context(), "", input)
	if err != nil || len(found) != 1 || found[0].ID != "gpt-6.1-sol" {
		t.Fatalf("check: %+v, %v", found, err)
	}
	rows, err := database.GetModelProviders(t.Context())
	if err != nil || len(rows) != 0 || len(m.registry.GetAvailableModels()) != 0 {
		t.Fatalf("check persisted a connection or changed the catalog: %+v, %v", rows, err)
	}
	created, err := m.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.APIKey = ""
	input.DisplayName = "Not saved"
	if _, err := m.Check(t.Context(), created.ProviderID, input); err != nil {
		t.Fatalf("check with the saved key: %v", err)
	}
	saved, err := database.GetModelProvider(t.Context(), created.ProviderID)
	if err != nil || saved.Version != 1 || saved.DisplayName != "Work" || saved.APIKey != "secret" {
		t.Fatalf("check changed the connection: %+v, %v", saved, err)
	}
	input.Endpoint = "https://another.example/v1"
	if _, err := m.Check(t.Context(), created.ProviderID, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("check reused the saved key on a new origin: %v", err)
	}
}

func TestGeminiProviderLifecycle(t *testing.T) {
	failing := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" || r.Header.Get("x-goog-api-key") != "secret" {
			t.Errorf("incorrect Gemini discovery request")
		}
		if failing {
			http.Error(w, "private upstream error", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"models":[{"name":"models/gemini-3.8-flash","displayName":"Flash","supportedGenerationMethods":["generateContent"]}]}`)
	}))
	defer upstream.Close()
	manager, database := testManager(t, upstream.Client())
	input := Input{ProviderType: "gemini", Endpoint: upstream.URL + "/v1beta", APIKey: "secret"}
	checked, err := manager.Check(t.Context(), "", input)
	if err != nil || len(checked) != 1 {
		t.Fatalf("check: %+v, %v", checked, err)
	}
	created, err := manager.Create(t.Context(), input)
	if err != nil || created.ProviderType != "gemini" || created.ModelCount != 1 || !created.HasAPIKey {
		t.Fatalf("create: %+v, %v", created, err)
	}
	id := models.ProviderRouteID(created.ProviderID, "gemini-3.8-flash")
	info := manager.registry.GetModelInfo(id)
	if info == nil || info.Provider != models.ProviderGemini || info.APIType != string(models.APITypeGemini) {
		t.Fatalf("incorrect Gemini route: %+v", info)
	}
	failing = true
	if _, err := manager.Refresh(t.Context(), created.ProviderID); !errors.Is(err, ErrDiscovery) {
		t.Fatalf("failed refresh: %v", err)
	}
	saved, err := database.GetModelProvider(t.Context(), created.ProviderID)
	if err != nil || len(saved.Models) != 1 || saved.Models[0].ID != "gemini-3.8-flash" {
		t.Fatalf("failed refresh lost snapshot: %+v, %v", saved, err)
	}
	if err := manager.Delete(t.Context(), created.ProviderID); err != nil {
		t.Fatal(err)
	}
	if manager.registry.HasModel(id) {
		t.Fatal("deleted Gemini provider remained in the registry")
	}
}
