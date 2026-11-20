// Package mcptest has fixtures for Shelley's MCP tests: an MCP server over
// Streamable HTTP, and a fake OAuth authorization server protecting one.
package mcptest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// PNG is what the "image" tool returns.
var PNG = []byte("\x89PNG\r\n\x1a\nfixture")

// Fixture is an MCP server with a few tools.
type Fixture struct {
	*httptest.Server
	// Blocked receives the session ID of each "block" call once it runs.
	Blocked chan string
	srv     *sdk.Server
	python  atomic.Bool // see EndSessionsLikePython

	mu      sync.Mutex
	deleted map[string]chan struct{} // by session ID, closed when the client deletes the session
}

// NewServer starts a Fixture.
func NewServer(t testing.TB) *Fixture {
	f := newFixture()
	f.Server = httptest.NewServer(f.handler())
	t.Cleanup(f.Close)
	return f
}

func newFixture() *Fixture {
	f := &Fixture{Blocked: make(chan string, 10), deleted: map[string]chan struct{}{}}
	s := sdk.NewServer(&sdk.Implementation{Name: "mcptest", Title: "MCP Test", Version: "1.0"}, &sdk.ServerOptions{Instructions: "Test fixture."})
	sdk.AddTool(s, &sdk.Tool{Name: "echo", Description: "Echo text back.\nSecond line.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *sdk.CallToolRequest, in struct {
			Text string `json:"text" jsonschema:"the text to echo"`
		},
		) (*sdk.CallToolResult, any, error) {
			return text(in.Text), nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "add", Description: "Add a and b."},
		func(_ context.Context, _ *sdk.CallToolRequest, in struct {
			A int `json:"a"`
			B int `json:"b"`
		},
		) (*sdk.CallToolResult, struct {
			Sum int `json:"sum"`
		}, error,
		) {
			return nil, struct {
				Sum int `json:"sum"`
			}{in.A + in.B}, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "fail", Description: "Return an error result."},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			r := text("it failed")
			r.IsError = true
			return r, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "image", Description: "Return a PNG."},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.ImageContent{MIMEType: "image/png", Data: PNG}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "session", Description: "Return the MCP session ID."},
		func(_ context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
			return text(req.Session.ID()), nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "header", Description: "Return an HTTP request header."},
		func(_ context.Context, req *sdk.CallToolRequest, in struct {
			Name string `json:"name"`
		},
		) (*sdk.CallToolResult, any, error) {
			return text(req.Extra.Header.Get(in.Name)), nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "block", Description: "Block until canceled."},
		func(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
			f.Blocked <- req.Session.ID()
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-f.deletedChan(req.Session.ID(), false):
				return nil, nil, errors.New("session deleted")
			}
		})
	addApp(s)
	f.srv = s
	return f
}

// RestrictRefresh makes "refresh" the model's alone, telling clients the
// tools changed.
func (f *Fixture) RestrictRefresh() { addRefresh(f.srv, "model") }

func addRefresh(s *sdk.Server, visibility string) {
	sdk.AddTool(s, &sdk.Tool{Name: "refresh", Description: "Called by the view.", Meta: sdk.Meta{"ui": map[string]any{"visibility": []string{visibility}}}},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			return text("refreshed"), nil, nil
		})
}

// ShowHTML is the view of the "show" tool, at ui://mcptest/show.
const ShowHTML = "<!doctype html><title>show</title><p>view</p>"

// addApp adds an MCP App: the "show" tool and its view, a tool only views
// may call, and one only the model may.
func addApp(s *sdk.Server) {
	const mime = "text/html;profile=mcp-app"
	sdk.AddTool(s, &sdk.Tool{Name: "show", Description: "Show text in a view.", Meta: sdk.Meta{"ui": map[string]any{"resourceUri": "ui://mcptest/show"}}},
		func(_ context.Context, req *sdk.CallToolRequest, in struct {
			Text string `json:"text"`
		},
		) (*sdk.CallToolResult, any, error) {
			if req.Session.InitializeParams().Capabilities.Extensions["io.modelcontextprotocol/ui"] == nil {
				return nil, nil, errors.New("the client doesn't support MCP Apps")
			}
			return text("showing " + in.Text), nil, nil
		})
	addRefresh(s, "app")
	sdk.AddTool(s, &sdk.Tool{Name: "secret", Description: "Only for the model.", Meta: sdk.Meta{"ui": map[string]any{"visibility": []string{"model"}}}},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			return text("secret"), nil, nil
		})
	s.AddResource(&sdk.Resource{URI: "ui://mcptest/show", Name: "show", MIMEType: mime},
		func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{
				URI: "ui://mcptest/show", MIMEType: mime, Text: ShowHTML,
				Meta: sdk.Meta{"ui": map[string]any{
					"csp":           map[string]any{"connectDomains": []string{"https://api.example.com", "https://x.example; script-src *"}},
					"permissions":   map[string]any{"clipboardWrite": map[string]any{}},
					"prefersBorder": false,
				}},
			}}}, nil
		})
	// Its _meta is in the resource list, and its HTML a blob.
	listed := &sdk.Resource{
		URI: "ui://mcptest/listed", Name: "listed", MIMEType: mime,
		Meta: sdk.Meta{"ui": map[string]any{"csp": map[string]any{"resourceDomains": []string{"https://cdn.example.com"}}}},
	}
	s.AddResource(listed,
		func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: "ui://mcptest/listed", MIMEType: mime, Blob: []byte(ShowHTML)}}}, nil
		})
}

// handler serves f. go-sdk waits for a deleted session's calls to return,
// and a client closing a session with a call in flight may not get the
// call's cancellation out first, so deleting a session ends its "block" calls.
func (f *Fixture) handler() http.Handler {
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return f.srv }, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("Mcp-Session-Id")
		if id != "" && f.python.Load() && !f.open(id) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,"message":"Session not found"}}`)
			return
		}
		if r.Method == http.MethodDelete {
			f.deletedChan(id, true)
		}
		h.ServeHTTP(w, r)
	})
}

// deletedChan returns the channel closed when session id is deleted,
// closing it if del.
func (f *Fixture) deletedChan(id string, del bool) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.deleted[id]
	if c == nil {
		c = make(chan struct{})
		f.deleted[id] = c
	}
	if del {
		select {
		case <-c:
		default:
			close(c)
		}
	}
	return c
}

// EndSessions ends the open sessions, as a server that restarts forgets
// them.
func (f *Fixture) EndSessions() {
	for ss := range f.srv.Sessions() {
		ss.Close()
	}
}

// EndSessionsLikePython ends the open sessions, and from now on answers
// requests for unknown sessions as the Python SDK does: with a 404 and a
// JSON-RPC error.
func (f *Fixture) EndSessionsLikePython() {
	f.python.Store(true)
	f.EndSessions()
}

func (f *Fixture) open(id string) bool {
	for ss := range f.srv.Sessions() {
		if ss.ID() == id {
			return true
		}
	}
	return false
}

func text(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}
