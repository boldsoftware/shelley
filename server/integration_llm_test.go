package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/exeenv"
)

func TestHandleIntegrationLLMModels(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case env.ReflectionURL() + "/integrations":
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
				`{"integrations":[{"name":"llm","type":"llm"},{"name":"gh","type":"github","help":"git clone https://github.int.example.test/owner/repo.git\\ngh repo view owner/repo; git clone https://github.int.example.test/owner/repo.git https://github.team.example.test/other/x.git"}]}`,
			)), Header: make(http.Header)}, nil
		case env.IntegrationURL("llm", false) + "/models.json":
			modelCalls++
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
				`{"schema_version":1,"providers":{"anthropic":{"mode":"managed","display_name":"Anthropic"},"openai":{"mode":"api_key"},"deepgram":{"mode":"managed"}},"models":[{"id":"anthropic/claude-sonnet-5","provider":"anthropic","apis":["anthropic_messages"],"upstream":{"secret":"TOP_SECRET"}},{"id":"openai/gpt-5.5","provider":"openai","apis":["openai_responses"]}]}`,
			)), Header: make(http.Header)}, nil
		default:
			t.Fatalf("unexpected integration request: %s", req.URL)
			return nil, nil
		}
	})}
	request := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handleIntegrationsIn(rec, httptest.NewRequest(http.MethodGet, path, nil), env, client)
		return rec
	}
	if rec := request("/api/integrations"); rec.Code != http.StatusOK || modelCalls != 0 {
		t.Fatalf("list fetched models: status=%d calls=%d", rec.Code, modelCalls)
	}
	if rec := request("/api/integrations?details=other"); rec.Code != http.StatusNotFound || modelCalls != 0 {
		t.Fatalf("unattached integration fetched models: status=%d calls=%d", rec.Code, modelCalls)
	}
	if rec := request("/api/integrations?details=gh"); rec.Code != http.StatusOK || modelCalls != 0 ||
		!strings.Contains(rec.Body.String(), `"repositories":[{"name":"owner/repo","url":"https://github.com/owner/repo","clone_command":"git clone https://github.int.example.test/owner/repo.git"}]`) {
		t.Fatalf("GitHub details: status=%d calls=%d body=%s", rec.Code, modelCalls, rec.Body.String())
	}
	rec := request("/api/integrations?details=llm")
	if rec.Code != http.StatusOK || modelCalls != 1 ||
		!strings.Contains(rec.Body.String(), `"model_counts":[{"provider":"anthropic","mode":"managed","chat":1},{"provider":"deepgram","mode":"managed"},{"provider":"openai","mode":"api_key","chat":1}]`) ||
		strings.Contains(rec.Body.String(), "TOP_SECRET") ||
		strings.Contains(rec.Body.String(), "claude-sonnet-5") {
		t.Fatalf("selected LLM models: status=%d calls=%d body=%s", rec.Code, modelCalls, rec.Body.String())
	}
}
