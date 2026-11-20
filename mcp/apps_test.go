package mcp

import (
	"slices"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolUIOf(t *testing.T) {
	for _, tc := range []struct {
		meta       sdk.Meta
		uri        string
		visibility []string
	}{
		{nil, "", []string{"model", "app"}},
		{sdk.Meta{"ui": map[string]any{"resourceUri": "ui://a/b"}}, "ui://a/b", []string{"model", "app"}},
		{sdk.Meta{"ui/resourceUri": "ui://old"}, "ui://old", []string{"model", "app"}},
		{sdk.Meta{"ui": map[string]any{"resourceUri": "ui://new"}, "ui/resourceUri": "ui://old"}, "ui://new", []string{"model", "app"}},
		{sdk.Meta{"ui": map[string]any{"resourceUri": "https://x"}}, "", []string{"model", "app"}},
		{sdk.Meta{"ui": map[string]any{"visibility": []string{"app"}}}, "", []string{"app"}},
		{sdk.Meta{"ui": "garbage"}, "", []string{"model", "app"}},
	} {
		u := ToolUIOf(&sdk.Tool{Meta: tc.meta})
		if u.ResourceURI != tc.uri || !slices.Equal(u.Visibility, tc.visibility) {
			t.Errorf("%v: %+v", tc.meta, u)
		}
	}
}

func TestAppPolicy(t *testing.T) {
	c := AppCSP{
		ConnectDomains:  []string{"https://api.example.com", "wss://ws.example.com:8443", "https://x; script-src *", "*", "'unsafe-eval'"},
		ResourceDomains: []string{"https://*.cdn.example.com/lib/", "example.org"},
	}.sanitized()
	const want = "default-src 'none'; " +
		"script-src 'unsafe-inline' 'unsafe-eval' blob: data: https://*.cdn.example.com/lib/ example.org; " +
		"worker-src blob: https://*.cdn.example.com/lib/ example.org; " +
		"style-src 'unsafe-inline' https://*.cdn.example.com/lib/ example.org; " +
		"img-src data: blob: https://*.cdn.example.com/lib/ example.org; " +
		"font-src data: https://*.cdn.example.com/lib/ example.org; " +
		"media-src data: blob: https://*.cdn.example.com/lib/ example.org; " +
		"connect-src https://api.example.com wss://ws.example.com:8443; " +
		"frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
	if got := c.policy(); got != want {
		t.Fatalf("policy:\n%s\nwant:\n%s", got, want)
	}
}
