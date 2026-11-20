package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP Apps (https://modelcontextprotocol.io/extensions/apps): tools that
// render an interactive HTML view, which Shelley's UI hosts.
const (
	AppsExtension = "io.modelcontextprotocol/ui"
	AppMIMEType   = "text/html;profile=mcp-app"
)

// ToolUI is what a tool's _meta says about its view.
type ToolUI struct {
	// ResourceURI is the ui:// resource of the tool's view, if it has one.
	ResourceURI string
	// Visibility is who may call the tool: "model", "app" (its views), or
	// both.
	Visibility []string
}

// ToolUIOf reads t's _meta.ui, or the deprecated _meta["ui/resourceUri"].
func ToolUIOf(t *sdk.Tool) ToolUI {
	var meta struct {
		UI struct {
			ResourceURI string   `json:"resourceUri"`
			Visibility  []string `json:"visibility"`
		} `json:"ui"`
		ResourceURI string `json:"ui/resourceUri"`
	}
	if b, err := json.Marshal(t.Meta); err == nil {
		json.Unmarshal(b, &meta) // a malformed _meta has no view
	}
	u := ToolUI{ResourceURI: meta.UI.ResourceURI, Visibility: meta.UI.Visibility}
	if u.ResourceURI == "" {
		u.ResourceURI = meta.ResourceURI
	}
	if !strings.HasPrefix(u.ResourceURI, "ui://") {
		u.ResourceURI = ""
	}
	if u.Visibility == nil {
		u.Visibility = []string{"model", "app"}
	}
	return u
}

// VisibleTo reports whether caller ("model" or "app") may call the tool.
func (u ToolUI) VisibleTo(caller string) bool {
	return slices.Contains(u.Visibility, caller)
}

// AppCSP is the origins a view needs, from its resource's _meta.ui.csp.
type AppCSP struct {
	ConnectDomains  []string `json:"connectDomains,omitempty"`
	ResourceDomains []string `json:"resourceDomains,omitempty"`
	FrameDomains    []string `json:"frameDomains,omitempty"`
	BaseURIDomains  []string `json:"baseUriDomains,omitempty"`
}

// AppPermissions is the browser permissions a view asks for.
type AppPermissions struct {
	Camera         *struct{} `json:"camera,omitempty"`
	Microphone     *struct{} `json:"microphone,omitempty"`
	Geolocation    *struct{} `json:"geolocation,omitempty"`
	ClipboardWrite *struct{} `json:"clipboardWrite,omitempty"`
}

// App is a view's UI resource, ready to render.
type App struct {
	URI  string `json:"uri"`
	HTML string `json:"html"`
	// CSP is what the view may load, with only well-formed origins kept.
	CSP AppCSP `json:"csp"`
	// Policy is the Content-Security-Policy to render the view with.
	Policy        string         `json:"policy"`
	Permissions   AppPermissions `json:"permissions"`
	PrefersBorder *bool          `json:"prefersBorder,omitempty"`
}

type appMeta struct {
	CSP           AppCSP         `json:"csp"`
	Permissions   AppPermissions `json:"permissions"`
	PrefersBorder *bool          `json:"prefersBorder"`
}

// ReadResource reads the resource at uri.
func (m *Manager) ReadResource(ctx context.Context, cfg Server, scope, uri string) (*sdk.ReadResourceResult, error) {
	s, ctx, done, err := m.acquire(ctx, cfg, scope)
	if err != nil {
		return nil, err
	}
	defer done()
	return m.read(s, ctx, uri)
}

func (m *Manager) read(s *session, ctx context.Context, uri string) (*sdk.ReadResourceResult, error) {
	res, err := s.cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, fmt.Errorf("reading %s from %s: %w", uri, s.cfg.Name, m.failed(s, ctx, err))
	}
	return res, nil
}

// ReadApp reads the view at uri. Its _meta.ui comes from the content,
// or else from the server's resource list.
func (m *Manager) ReadApp(ctx context.Context, cfg Server, scope, uri string) (*App, error) {
	s, ctx, done, err := m.acquire(ctx, cfg, scope)
	if err != nil {
		return nil, err
	}
	defer done()
	res, err := m.read(s, ctx, uri)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(res.Contents, func(c *sdk.ResourceContents) bool { return c.MIMEType == AppMIMEType })
	if i < 0 {
		return nil, fmt.Errorf("%s on %s is not %s", uri, cfg.Name, AppMIMEType)
	}
	c := res.Contents[i]
	meta := c.Meta["ui"]
	if meta == nil {
		for r, err := range s.cs.Resources(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("listing the resources of %s: %w", cfg.Name, m.failed(s, ctx, err))
			}
			if r.URI == uri {
				meta = r.Meta["ui"]
				break
			}
		}
	}
	var am appMeta
	if b, err := json.Marshal(meta); err == nil {
		json.Unmarshal(b, &am) // malformed metadata asks for nothing
	}
	html := c.Text
	if html == "" {
		html = string(c.Blob)
	}
	csp := am.CSP.sanitized()
	return &App{URI: uri, HTML: html, CSP: csp, Policy: csp.policy(), Permissions: am.Permissions, PrefersBorder: am.PrefersBorder}, nil
}

// cspSource matches the CSP source expressions a view may declare: a host,
// optionally with a scheme, a leading wildcard label, a port and a path.
var cspSource = regexp.MustCompile(`^((https?|wss?)://)?(\*\.)?[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*(:([0-9]+|\*))?(/[A-Za-z0-9._~%!$&()*+=:@/-]*)?$`)

func (c AppCSP) sanitized() AppCSP {
	keep := func(domains []string) []string {
		var ok []string
		for _, d := range domains {
			if cspSource.MatchString(d) {
				ok = append(ok, d)
			}
		}
		return ok
	}
	return AppCSP{keep(c.ConnectDomains), keep(c.ResourceDomains), keep(c.FrameDomains), keep(c.BaseURIDomains)}
}

// policy is the spec's restrictive default CSP plus c's origins. Views
// render in an opaque origin, so there is no 'self' to allow. Forms may
// submit only to scripts, not to another origin.
func (c AppCSP) policy() string {
	src := func(base string, domains []string) string {
		parts := slices.Concat(strings.Fields(base), domains)
		if len(parts) == 0 {
			return "'none'"
		}
		return strings.Join(parts, " ")
	}
	return strings.Join([]string{
		"default-src 'none'",
		// Keywords, unlike origins, let a view reach nothing new: it already
		// runs any script it likes. CesiumJS and the three.js example need eval,
		// and CesiumJS starts workers from blob: URLs, as the reference host
		// allows. A blob: worker is held to this policy, which it inherits;
		// leave data: out of worker-src rather than rely on browsers doing
		// the same for data: workers.
		"script-src " + src("'unsafe-inline' 'unsafe-eval' blob: data:", c.ResourceDomains),
		"worker-src " + src("blob:", c.ResourceDomains),
		"style-src " + src("'unsafe-inline'", c.ResourceDomains),
		"img-src " + src("data: blob:", c.ResourceDomains),
		"font-src " + src("data:", c.ResourceDomains),
		"media-src " + src("data: blob:", c.ResourceDomains),
		"connect-src " + src("", c.ConnectDomains),
		"frame-src " + src("", c.FrameDomains),
		"object-src 'none'",
		"base-uri " + src("", c.BaseURIDomains),
		"form-action 'none'",
	}, "; ")
}

// AppServerID names the server at s's URL, which may hold credentials, so a
// view shown from it isn't later fed to another server registered under the
// same name.
func (s Server) AppServerID() string {
	sum := sha256.Sum256([]byte(s.URL))
	return hex.EncodeToString(sum[:8])
}
