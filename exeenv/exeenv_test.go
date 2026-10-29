package exeenv

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func metadataResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestCurrentOutsideExeVMDoesNotProbeMetadata(t *testing.T) {
	r := &resolver{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected metadata request outside exe.dev: %s", req.URL)
		return nil, nil
	})}}
	env, err := currentOnVM(filepath.Join(t.TempDir(), "missing-exe-dev-marker"), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.ReflectionURL(); got != "https://reflection.int.exe.xyz" {
		t.Errorf("ReflectionURL() = %q, want prod", got)
	}
}

func TestCurrentInsideExeVMProbesMetadata(t *testing.T) {
	calls := 0
	r := &resolver{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return metadataResponse(`{"reflection_url":"http://reflection.int.exe.cloud"}`), nil
	})}}
	env, err := currentOnVM(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.ReflectionURL(); got != "http://reflection.int.exe.cloud" {
		t.Errorf("ReflectionURL() = %q, want local", got)
	}
	if calls != 1 {
		t.Errorf("metadata requests = %d, want 1", calls)
	}
}

func TestMetadataEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{"production", "https://reflection.int.exe.xyz", "https://llm.team.exe.xyz"},
		{"local", "http://reflection.int.exe.cloud", "http://llm.team.exe.cloud"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := resolver{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.String() != "http://169.254.169.254/" {
					t.Fatalf("metadata request URL = %q", req.URL)
				}
				return metadataResponse(`{"name":"my-box","source_ip":"10.42.0.42","reflection_url":"` + tc.url + `"}`), nil
			})}}
			for range 2 {
				env, err := r.current()
				if err != nil {
					t.Fatal(err)
				}
				if got := env.ReflectionURL(); got != tc.url {
					t.Errorf("ReflectionURL() = %q, want %q", got, tc.url)
				}
				if got := env.IntegrationURL("llm", true); got != tc.want {
					t.Errorf("IntegrationURL(llm, true) = %q, want %q", got, tc.want)
				}
				if got := env.VMName(); got != "my-box" {
					t.Errorf("VMName() = %q, want my-box", got)
				}
			}
			if calls != 1 {
				t.Errorf("metadata requests = %d, want one cached success", calls)
			}
		})
	}
}

func TestMetadataFallbackStaysProd(t *testing.T) {
	calls := 0
	r := resolver{
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("metadata unavailable")
			}
			return metadataResponse(`{"reflection_url":"http://reflection.int.exe.cloud"}`), nil
		})},
	}
	for range 3 {
		env, err := r.current()
		if err != nil {
			t.Fatal(err)
		}
		if got := env.ReflectionURL(); got != "https://reflection.int.exe.xyz" {
			t.Errorf("fallback ReflectionURL() = %q", got)
		}
	}
	if calls != 1 {
		t.Errorf("metadata requests = %d, want a single attempt", calls)
	}
}

func TestMetadataMissingFieldFallsBackToProd(t *testing.T) {
	r := resolver{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return metadataResponse(`{"name":"old-box","source_ip":"10.42.0.42"}`), nil
	})}}
	env, err := r.current()
	if err != nil {
		t.Fatal(err)
	}
	if got := env.ReflectionURL(); got != "https://reflection.int.exe.xyz" {
		t.Errorf("fallback ReflectionURL() = %q", got)
	}
}

func TestMetadataUnavailableStatusFallsBackToProd(t *testing.T) {
	r := resolver{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("not ready"))}, nil
	})}}
	env, err := r.current()
	if err != nil {
		t.Fatal(err)
	}
	if got := env.ReflectionURL(); got != "https://reflection.int.exe.xyz" {
		t.Errorf("fallback ReflectionURL() = %q", got)
	}
}

func TestMetadataMalformedURLDoesNotSilentlyFallBack(t *testing.T) {
	for _, badURL := range []string{"ftp://reflection.int.exe.xyz", "https://other.example", "https://reflection.int.exe.xyz/path", "https://reflection.int.exe.xyz:443"} {
		t.Run(badURL, func(t *testing.T) {
			r := resolver{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return metadataResponse(`{"reflection_url":"` + badURL + `"}`), nil
			})}}
			if _, err := r.current(); err == nil {
				t.Fatalf("accepted invalid reflection_url %q", badURL)
			}
		})
	}
}

func TestMalformedMetadataDoesNotSilentlyFallBack(t *testing.T) {
	r := resolver{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return metadataResponse(`{"reflection_url":`), nil
	})}}
	if _, err := r.current(); err == nil {
		t.Fatal("malformed metadata should return an error")
	}
}

func TestNewBuildsConfiguredEnvironment(t *testing.T) {
	env, err := New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := env.ReflectionURL(); got != "https://reflection.int.example.test" {
		t.Fatalf("ReflectionURL() = %q", got)
	}
	if got := env.IntegrationURL("llm", true); got != "https://llm.team.example.test" {
		t.Fatalf("IntegrationURL() = %q", got)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		scheme  string
		boxHost string
		wantErr string
	}{
		{name: "missing scheme", boxHost: "example.test", wantErr: "scheme"},
		{name: "unsupported scheme", scheme: "ftp", boxHost: "example.test", wantErr: "scheme"},
		{name: "missing box host", scheme: "https", wantErr: "box_host"},
		{name: "URL as box host", scheme: "https", boxHost: "https://example.test", wantErr: "box_host"},
		{name: "path in box host", scheme: "https", boxHost: "example.test/path", wantErr: "box_host"},
		{name: "port in box host", scheme: "https", boxHost: "example.test:443", wantErr: "box_host"},
		{name: "space in box host", scheme: "https", boxHost: "example test", wantErr: "box_host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.scheme, tt.boxHost)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("New(%q, %q) error = %v, want error containing %q", tt.scheme, tt.boxHost, err, tt.wantErr)
			}
		})
	}
}

func TestCurrentPrefersConfiguredEnvironment(t *testing.T) {
	old := configured.Load()
	t.Cleanup(func() { configured.Store(old) })

	env, err := New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	Configure(env)

	got, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if got.ReflectionURL() != env.ReflectionURL() {
		t.Fatalf("Current().ReflectionURL() = %q, want %q", got.ReflectionURL(), env.ReflectionURL())
	}
}
