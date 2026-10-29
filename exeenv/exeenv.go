// Package exeenv resolves exe.dev environment-specific service URLs.
package exeenv

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const metadataURL = "http://169.254.169.254/"

// Environment describes the scheme and base domain used by exe.dev services.
type Environment struct {
	scheme  string
	boxHost string
	vmName  string
}

// New returns an environment for a configured scheme and box hostname.
func New(scheme, boxHost string) (Environment, error) {
	if scheme != "http" && scheme != "https" {
		return Environment{}, fmt.Errorf("invalid exe environment scheme %q: must be http or https", scheme)
	}
	parsed, err := url.Parse(scheme + "://" + boxHost)
	if err != nil || boxHost == "" || parsed.Host != boxHost || parsed.Hostname() != boxHost || parsed.Port() != "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Environment{}, fmt.Errorf("invalid exe environment box_host %q: must be a bare hostname", boxHost)
	}
	return Environment{scheme: scheme, boxHost: boxHost}, nil
}

// Resolve VM metadata once; an unavailable or pre-upgrade endpoint uses prod.
type resolver struct {
	once   sync.Once
	client *http.Client
	env    Environment
	err    error
}

var defaultResolver = &resolver{client: func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // the link-local metadata endpoint must never use an HTTP proxy
	return &http.Client{Timeout: time.Second, Transport: transport}
}()}

func (r *resolver) current() (Environment, error) {
	r.once.Do(func() { r.env, r.err = r.fetch() })
	return r.env, r.err
}

func (r *resolver) fetch() (Environment, error) {
	prod := Environment{scheme: "https", boxHost: "exe.xyz"}
	resp, err := r.client.Get(metadataURL)
	if err != nil {
		return prod, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return prod, nil
	}
	var data struct {
		Name          string `json:"name"`
		ReflectionURL string `json:"reflection_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&data); err != nil {
		return Environment{}, fmt.Errorf("decode VM metadata: %w", err)
	}
	env := prod
	if data.ReflectionURL != "" {
		if env, err = fromReflectionURL(data.ReflectionURL); err != nil {
			return env, err
		}
	}
	env.vmName = data.Name
	return env, nil
}

func fromReflectionURL(raw string) (Environment, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Environment{}, fmt.Errorf("invalid reflection_url %q: %w", raw, err)
	}
	boxHost, ok := strings.CutPrefix(u.Host, "reflection.int.")
	if !ok || boxHost == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return Environment{}, fmt.Errorf("invalid reflection_url %q: expected an integration origin", raw)
	}
	env, err := New(u.Scheme, boxHost)
	if err != nil {
		return Environment{}, fmt.Errorf("invalid reflection_url %q: %w", raw, err)
	}
	return env, nil
}

var configured atomic.Pointer[Environment]

// Configure sets the process-wide exe.dev environment used by Current.
func Configure(env Environment) {
	configured.Store(&env)
}

// Current resolves the environment once from VM metadata. Off exe.dev it does
// not query the link-local IP, which may be another cloud's metadata service.
// On exe.dev, unavailable or old metadata falls back to prod for this process.
func Current() (Environment, error) {
	if env := configured.Load(); env != nil {
		return *env, nil
	}
	return currentOnVM("/exe.dev", defaultResolver)
}

func currentOnVM(markerPath string, r *resolver) (Environment, error) {
	if _, err := os.Stat(markerPath); err != nil {
		return Environment{scheme: "https", boxHost: "exe.xyz"}, nil
	}
	return r.current()
}

// VMName returns this VM's exe.dev name, or "" if unknown.
func (e Environment) VMName() string {
	return e.vmName
}

// ReflectionURL returns the reflection integration's base URL.
func (e Environment) ReflectionURL() string {
	return e.scheme + "://reflection.int." + e.boxHost
}

// IntegrationHost returns the hostname for a personal or team integration.
func (e Environment) IntegrationHost(name string, team bool) string {
	scope := "int"
	if team {
		scope = "team"
	}
	return name + "." + scope + "." + e.boxHost
}

// IntegrationURL returns the base URL for a personal or team integration.
func (e Environment) IntegrationURL(name string, team bool) string {
	return e.scheme + "://" + e.IntegrationHost(name, team)
}
