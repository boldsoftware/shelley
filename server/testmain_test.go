package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/exeenv"
)

func TestMain(m *testing.M) {
	env, err := exeenv.New("https", "exe.xyz")
	if err != nil {
		panic(err)
	}
	exeenv.Configure(env) // server tests must not reach the VM metadata service
	exeDevDefaultPortHTTPClient = &http.Client{Transport: defaultPortTestTransport{}}
	// Default to a failing reflection client so server tests never make real
	// network calls. Tests that exercise reflection override this explicitly.
	setReflectionHTTPClient(&http.Client{Transport: defaultPortTestTransport{}})
	refuseLinkLocalDials()
	// Isolate from any host-wide git config (e.g. core.hooksPath that
	// enforces commit-message policy on agent-driven commits) so tests
	// that invoke `git commit` behave deterministically.
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	// Run from an empty temp dir, not the package dir. Conversations created
	// with cwd="" fall back to os.Getwd(), and system-prompt generation then
	// walks the whole surrounding git repo (guidance-file + skills walks).
	// Inside a large monorepo checkout that is millions of stat calls across
	// the suite; each one is also recorded in go test's testlog, which `go
	// test` re-hashes and writes to the build cache after the binary exits
	// (observed: a 500MB testlog adding 2+ minutes of dead time in CI after
	// the last test finished). An empty cwd keeps those walks trivially
	// small without changing what the tests exercise; tests that care about
	// specific trees pass an explicit cwd already.
	tmp, err := os.MkdirTemp("", "shelley-server-test-cwd-")
	if err != nil {
		panic(err)
	}
	if err := os.Chdir(tmp); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// refuseLinkLocalDials makes http.DefaultTransport, and so every client
// without its own transport, refuse link-local addresses such as
// 169.254.169.254, where every *.int.exe.xyz name resolves. Off exe.dev
// nothing listens there and a stray request hangs for its client's whole
// timeout; on exe.dev it reaches a real integration. Tests that exercise an
// integration point their client at a local server.
func refuseLinkLocalDials() {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Through an HTTP proxy the dialer would only see the proxy's address.
	transport.Proxy = nil
	// The timeouts are DefaultTransport's own.
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: refuseLinkLocal}
	transport.DialContext = dialer.DialContext
	http.DefaultTransport = transport
}

// refuseLinkLocal is a net.Dialer Control hook. It sees the resolved address
// of each attempt, so it catches any name that resolves to link-local.
func refuseLinkLocal(_, address string, _ syscall.RawConn) error {
	if ap, err := netip.ParseAddrPort(address); err == nil && ap.Addr().IsLinkLocalUnicast() {
		return fmt.Errorf("server tests must not reach link-local %s (the exe.dev integration proxy)", address)
	}
	return nil
}

type defaultPortTestTransport struct{}

func (defaultPortTestTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("reflection disabled in tests")
}

// A server test's accidental request to an integration fails at once rather
// than reaching the exe.dev integration proxy; see refuseLinkLocalDials.
func TestRequestsToIntegrationsFailFast(t *testing.T) {
	t.Parallel()
	// A client timeout, so a regression fails here rather than hanging (on
	// CI) until the dialer gives up. The IP, not an integration name, so
	// the test needs no DNS.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post("http://169.254.169.254/typing", "application/json", strings.NewReader("{}"))
	if err == nil {
		resp.Body.Close()
		t.Fatalf("request reached the integration proxy: %s", resp.Status)
	}
	if !strings.Contains(err.Error(), "must not reach link-local") {
		t.Fatalf("err = %v, want the link-local refusal", err)
	}
	for address, refused := range map[string]bool{
		"[fe80::1%eth0]:443": true,
		"127.0.0.1:443":      false,
		"[::1]:443":          false,
	} {
		if err := refuseLinkLocal("tcp", address, nil); (err != nil) != refused {
			t.Errorf("refuseLinkLocal(%q) = %v, want refused %t", address, err, refused)
		}
	}
}
