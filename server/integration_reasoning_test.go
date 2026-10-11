package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/models"
	"shelley.exe.dev/modelsources"
)

// The same model can have different controls at two endpoints, and two models
// at one endpoint can have different controls. Build them together so that a
// model-wide or endpoint-wide override would give the wrong choices.
func TestThinkingControlsBelongToModelEndpointPair(t *testing.T) {
	const muse = "meta/muse-spark-1.3-contributor"
	type pair struct{ endpoint, model string }
	want := map[pair]struct {
		levels                   []string
		defaultEffort, maxEffort string
	}{
		{"a.example", muse}:          {[]string{"low", "medium", "high", "xhigh"}, "medium", "xhigh"},
		{"b.example", muse}:          {[]string{"low", "high"}, "low", "high"},
		{"a.example", "other-model"}: {[]string{"low"}, "low", "low"},
	}
	var sources []modelsources.Source
	for _, endpoint := range []struct{ host, discovery string }{
		{"a.example", `[
   {"id":"opencode/meta/muse-spark-1.3-contributor","provider":"opencode","native_id":"meta/muse-spark-1.3-contributor","apis":["openai_responses"],"upstream":{"reasoning_levels":["low","medium","high","xhigh"]}},
   {"id":"opencode/other-model","provider":"opencode","native_id":"other-model","apis":["openai_responses"],"upstream":{"reasoning_levels":["low"]}}
  ]`},
		{"b.example", `[
   {"id":"opencode/meta/muse-spark-1.3-contributor","provider":"opencode","native_id":"meta/muse-spark-1.3-contributor","apis":["openai_responses"],"upstream":{"reasoning_levels":["low","high"]}}
  ]`},
	} {
		integration := &modelsources.LLMIntegrationConfig{Name: endpoint.host, Host: endpoint.host, URL: "https://" + endpoint.host}
		if err := json.Unmarshal([]byte(endpoint.discovery), &integration.Models); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, modelsources.LLMIntegration(integration, "-"+endpoint.host))
	}
	built := modelsources.Build(models.All(), sources, &http.Client{}, nil)
	mgr, err := models.NewManager(&models.Config{Models: built})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{llmManager: mgr, logger: slog.Default()}
	rec := httptest.NewRecorder()
	s.handleModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	var got []ModelInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want three model/endpoint pairs, got %+v", got)
	}
	for _, m := range got {
		key := pair{strings.TrimPrefix(m.BaseURL, "https://"), m.APIModelName}
		expected, ok := want[key]
		if !ok {
			t.Fatalf("unexpected or duplicate model/endpoint pair: %+v", key)
		}
		if !m.SupportsReasoning || !reflect.DeepEqual(m.ReasoningLevels, expected.levels) || m.DefaultReasoningLevel != expected.defaultEffort {
			t.Errorf("%+v: levels=%v default=%q; want levels=%v default=%q", key, m.ReasoningLevels, m.DefaultReasoningLevel, expected.levels, expected.defaultEffort)
		}
		if msg := validateModelReasoningLevel(&m, expected.maxEffort); msg != "" {
			t.Errorf("%+v rejects advertised effort: %s", key, msg)
		}
		if msg := validateModelReasoningLevel(&m, "max"); msg == "" {
			t.Errorf("%+v accepts unadvertised max", key)
		}
		if rounded, changed := roundModelReasoningLevel(&m, "max"); rounded != expected.maxEffort || !changed {
			t.Errorf("%+v: switching from max gives %q, want %q", key, rounded, expected.maxEffort)
		}
		delete(want, key)
	}
}
