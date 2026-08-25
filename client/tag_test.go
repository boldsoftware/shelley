package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestNormalizeTagList(t *testing.T) {
	got := normalizeTagList([]string{" a ", "", "b", "a", "#c"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeTagList = %v, want %v", got, want)
	}
}

func TestMergeAndRemoveTags(t *testing.T) {
	if got, want := mergeTags([]string{"a"}, []string{"b", "a"}), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeTags = %v, want %v", got, want)
	}
	if got, want := removeTags([]string{"a", "b", "c"}, []string{"b", "z"}), []string{"a", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removeTags = %v, want %v", got, want)
	}
}

// fakeTagServer mimics the two Shelley endpoints the tag commands use:
// GET /api/conversation/<id> (conversation metadata, no messages) and
// POST /api/conversation/<id>/tags.
type fakeTagServer struct {
	tags map[string][]string // conversationID -> tags
	list []map[string]any    // /api/conversations rows
	arch []map[string]any    // /api/conversations/archived rows
}

func (f *fakeTagServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/conversations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(f.list)
	})
	mux.HandleFunc("GET /api/conversations/archived", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(f.arch)
	})
	mux.HandleFunc("GET /api/conversation/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		tagsJSON, err := json.Marshal(f.tags[id])
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"conversation": map[string]any{"conversation_id": id, "tags": string(tagsJSON)},
		})
	})
	mux.HandleFunc("POST /api/conversation/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			Tags []string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.tags[id] = body.Tags
		tagsJSON, _ := json.Marshal(body.Tags)
		json.NewEncoder(w).Encode(map[string]any{
			"conversation_id": id, "tags": string(tagsJSON),
		})
	})
	return mux
}

func newFakeTagClient(t *testing.T, f *fakeTagServer) (*clientConfig, *http.Client, string) {
	t.Helper()
	ts := httptest.NewServer(f.handler())
	t.Cleanup(ts.Close)
	cc := &clientConfig{serverURL: ts.URL}
	httpClient, baseURL, err := cc.newHTTPClient()
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	return cc, httpClient, baseURL
}

func TestFetchAndSetConversationTags(t *testing.T) {
	f := &fakeTagServer{tags: map[string][]string{"conv1": {"alpha"}}}
	cc, httpClient, baseURL := newFakeTagClient(t, f)

	got, err := fetchConversationTags(cc, httpClient, baseURL, "conv1")
	if err != nil {
		t.Fatalf("fetchConversationTags: %v", err)
	}
	if want := []string{"alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}

	updated, err := setConversationTags(cc, httpClient, baseURL, "conv1", []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("setConversationTags: %v", err)
	}
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(updated, want) {
		t.Fatalf("updated = %v, want %v", updated, want)
	}
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(f.tags["conv1"], want) {
		t.Fatalf("server tags = %v, want %v", f.tags["conv1"], want)
	}
}

// An empty/absent tags field must read as "no tags", not an error.
func TestFetchConversationTagsEmpty(t *testing.T) {
	f := &fakeTagServer{tags: map[string][]string{}}
	cc, httpClient, baseURL := newFakeTagClient(t, f)
	got, err := fetchConversationTags(cc, httpClient, baseURL, "nope")
	if err != nil {
		t.Fatalf("fetchConversationTags: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("tags = %v, want empty", got)
	}
}

func TestListAllTagsCountsAndOrder(t *testing.T) {
	f := &fakeTagServer{
		list: []map[string]any{
			{"conversation_id": "a", "tags": `["ios","bug"]`},
			{"conversation_id": "b", "tags": `["ios"]`},
			{"conversation_id": "c", "tags": ``},
		},
		arch: []map[string]any{
			{"conversation_id": "d", "tags": `["ios","archived-only"]`},
		},
	}
	cc, httpClient, baseURL := newFakeTagClient(t, f)

	got, err := listAllTags(cc, httpClient, baseURL, 100)
	if err != nil {
		t.Fatalf("listAllTags: %v", err)
	}
	want := []tagCount{
		{Tag: "ios", Count: 3},
		{Tag: "archived-only", Count: 1},
		{Tag: "bug", Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listAllTags = %+v, want %+v", got, want)
	}
}
