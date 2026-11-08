package modelproviders

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

func TestGeminiDiscoveryAuthPaginationAndFiltering(t *testing.T) {
	pages := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		if r.URL.Path != "/v1beta/models" || r.Header.Get("x-goog-api-key") != "secret" ||
			r.Header.Get("Authorization") != "" || r.URL.Query().Has("key") {
			t.Errorf("incorrect Gemini discovery request: path=%s headers=%v query=%s", r.URL.Path, r.Header, r.URL.RawQuery)
		}
		switch pages {
		case 1:
			if r.URL.Query().Has("pageToken") {
				t.Error("first page had a cursor")
			}
			fmt.Fprint(w, `{"models":[
				{"name":"models/gemini-3.8-flash","displayName":"Gemini Flash","supportedGenerationMethods":["generateContent"]},
				{"name":"models/gemini-3.1-pro-preview","supportedGenerationMethods":["embedContent"]},
				{"name":"models/unknown-model","supportedGenerationMethods":["generateContent"]}
			],"nextPageToken":"next +/="}`)
		case 2:
			if r.URL.Query().Get("pageToken") != "next +/=" {
				t.Errorf("second page cursor = %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"models":[
				{"name":"models/gemini-3.8-flash","supportedGenerationMethods":["generateContent"]},
				{"name":"models/gemini-3.1-pro-preview","displayName":"Gemini Pro","supportedGenerationMethods":["generateContent"]}
			]}`)
		default:
			t.Error("unexpected extra page")
			http.Error(w, "unexpected page", http.StatusInternalServerError)
		}
	}))
	defer upstream.Close()
	manager := New(nil, nil, upstream.Client())
	got, err := manager.discover(t.Context(), db.ModelProvider{
		ProviderType: "gemini", Endpoint: upstream.URL + "/v1beta", APIKey: "secret",
	})
	want := []db.ProviderModel{{ID: "gemini-3.8-flash", Name: "Gemini Flash"}, {ID: "gemini-3.1-pro-preview", Name: "Gemini Pro"}}
	if err != nil || pages != 2 || !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %+v, pages = %d, error = %v", got, pages, err)
	}
}

func TestGeminiDiscoveryRejectsMalformedPages(t *testing.T) {
	for _, body := range []string{
		`{`, `{}`, `{"models":null}`, `{"models":{}}`, `{"models":[null]}`,
		`{"models":[{"name":"gemini-3.8-flash"}]}`,
		`{"models":[{"name":"models/"}]}`,
		`{"models":[{"name":42}]}`,
		`{"models":[],"nextPageToken":42}`,
	} {
		t.Run(body, func(t *testing.T) {
			if _, _, _, err := parseGeminiModelPage([]byte(body)); !errors.Is(err, ErrDiscovery) {
				t.Fatalf("error = %v, want discovery failure", err)
			}
		})
	}
	items, more, cursor, err := parseGeminiModelPage([]byte(`{"models":[]}`))
	if err != nil || len(items) != 0 || more || cursor != "" {
		t.Fatalf("empty catalog: %v, %t, %q, %v", items, more, cursor, err)
	}
}

func TestDiscoveryAuthAndPagination(t *testing.T) {
	for _, tc := range []struct {
		protocol, cursorKey, authHeader, authValue string
	}{
		{"anthropic", "after_id", "x-api-key", "secret"},
		{"openai", "after", "Authorization", "Bearer secret"},
		{"openai-responses", "after", "Authorization", "Bearer secret"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			pages := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pages++
				if r.URL.Path != "/v1/models" || r.Header.Get(tc.authHeader) != tc.authValue {
					t.Errorf("request path or authorization incorrect: %s", r.URL.Path)
				}
				if pages == 1 {
					if r.URL.Query().Has(tc.cursorKey) {
						t.Errorf("first page had cursor")
					}
					fmt.Fprint(w, `{"data":[{"id":"gpt-6.1-sol"}],"has_more":true,"last_id":"first"}`)
				} else {
					if r.URL.Query().Get(tc.cursorKey) != "first" {
						t.Errorf("second page cursor: %q", r.URL.RawQuery)
					}
					fmt.Fprint(w, `{"data":[{"id":"gpt-6.1-sol"},{"id":"gpt-5.6-sol"}]}`)
				}
			}))
			defer upstream.Close()
			m := New(nil, nil, upstream.Client())
			if _, err := m.discover(t.Context(), db.ModelProvider{ProviderType: tc.protocol, Endpoint: upstream.URL + "/v1", APIKey: "secret"}); err != nil {
				t.Fatal(err)
			}
			if pages != 2 {
				t.Fatalf("pages = %d", pages)
			}
		})
	}
}

func TestDiscoveryDoesNotFollowRedirectOrExposeResponse(t *testing.T) {
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/models", http.StatusFound)
		fmt.Fprint(w, "secret response body")
	}))
	defer upstream.Close()
	m := New(nil, nil, upstream.Client())
	_, err := m.discover(t.Context(), db.ModelProvider{ProviderType: "openai", Endpoint: upstream.URL, APIKey: "secret"})
	if err == nil || !errors.Is(err, ErrDiscovery) || redirected || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect: err=%v, forwarded=%t", err, redirected)
	}

	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"gpt-6.1-sol"}]}`)
	}))
	defer large.Close()
	left := int64(2)
	_, _, _, err = fetchModelPage(t.Context(), large.Client(), "openai", large.URL, "secret", &left)
	if err == nil || !errors.Is(err, ErrDiscovery) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("bounded page: %v", err)
	}
}
