package gem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/llm"
)

func TestGeminiDoPreservesToolSchemas(t *testing.T) {
	for _, tt := range []struct{ name, schema string }{
		{"nested patch", `{
			"type":"object","additionalProperties":false,"required":["path","patches"],
			"properties":{
				"path":{"type":"string"},
				"patches":{"type":"array","minItems":1,"items":{
					"type":"object","additionalProperties":false,"required":["operation","newText"],
					"properties":{"operation":{"type":"string","enum":["replace","append_eof"]},"newText":{"type":"string"}}
				}}
			}
		}`},
		{"iframe files map", `{
			"type":"object","required":["path"],
			"properties":{"path":{"type":"string"},"files":{"type":"object","description":"Inject <style> & other assets","additionalProperties":{"type":"string"}}}
		}`},
		{"custom schema", `{
			"type":"object","$defs":{"value":{"anyOf":[{"type":"string"},{"type":"null"}]}},
			"properties":{
				"value":{"$ref":"#/$defs/value"},
				"count":{"type":"integer","minimum":1,"maximum":9007199254740993,"enum":[1,2],"default":1},
				"enabled":{"type":"boolean","enum":[true,false]},
				"label":{"type":"string","pattern":"^[a-z]+$","format":"hostname"}
			}
		}`},
		{"empty schema", `{}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			captured := make(chan json.RawMessage, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				captured <- body
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, geminiSSE(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`))
			}))
			defer server.Close()
			service := &Service{URL: server.URL, APIKey: "test", Model: DefaultModel, HTTPC: server.Client()}
			_, err := service.Do(t.Context(), &llm.Request{
				Messages: []llm.Message{llm.UserStringMessage("Use the tool.")},
				Tools: []*llm.Tool{{
					Name: "schema_test", Description: "Test schema preservation", InputSchema: json.RawMessage(tt.schema),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Tools []struct {
					Declarations []map[string]json.RawMessage `json:"functionDeclarations"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(<-captured, &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Tools) != 1 || len(body.Tools[0].Declarations) != 1 {
				t.Fatalf("wrong tool declarations: %+v", body.Tools)
			}
			declaration := body.Tools[0].Declarations[0]
			if _, exists := declaration["parameters"]; exists {
				t.Fatal("legacy parameters field still present")
			}
			var schemas [2]any
			for i, raw := range []json.RawMessage{json.RawMessage(tt.schema), declaration["parametersJsonSchema"]} {
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				if err := decoder.Decode(&schemas[i]); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(schemas[0], schemas[1]) {
				t.Fatalf("tool schema changed:\ngot  %s\nwant %s", declaration["parametersJsonSchema"], tt.schema)
			}
			if string(declaration["name"]) != `"schema_test"` || string(declaration["description"]) != `"Test schema preservation"` {
				t.Fatalf("tool identity changed: %v", declaration)
			}
		})
	}
}

func TestConvertToolSchemasRejectsInvalidSchema(t *testing.T) {
	for _, schema := range []string{"", `{`, `{} {}`, `[]`, `"object"`, `1`, `true`, `null`} {
		t.Run(schema, func(t *testing.T) {
			decls, err := convertToolSchemas([]*llm.Tool{{Name: "bad_schema", InputSchema: json.RawMessage(schema)}})
			if err == nil || !strings.Contains(err.Error(), "bad_schema") {
				t.Fatalf("declarations=%v, error=%v; want named schema error", decls, err)
			}
			if len(decls) != 0 {
				t.Fatalf("returned declarations after schema error: %v", decls)
			}
		})
	}
}

func TestGeminiDoPropagatesToolSchemaRejection(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unsupported tool schema", http.StatusBadRequest)
	}))
	defer server.Close()
	service := &Service{URL: server.URL, APIKey: "test", Model: DefaultModel, HTTPC: server.Client()}
	_, err := service.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{llm.UserStringMessage("Use the tool.")},
		Tools:    []*llm.Tool{{Name: "rejected", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported tool schema") {
		t.Fatalf("error = %v, want provider rejection", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests = %d, want no retry or schema fallback", got)
	}
}
