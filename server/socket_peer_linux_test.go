package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A request over the local socket is temporary when the process that sent
// it runs in a temporary turn: its environment has SHELLEY_TEMPORARY=1.
func TestSocketPeerTemporary(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(t.TempDir(), "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		ConnContext: withSocketPeer,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			temporary, err := requestTemporary(r, false)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if temporary {
				io.WriteString(w, "temporary")
			}
		}),
	}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })

	for _, env := range []string{"", temporaryEnv} {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSocketPeerHelper$")
		cmd.Env = append(os.Environ(), "SOCKET_PEER_HELPER="+sock, env)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper: %v %s", err, out)
		}
		if got, want := string(out) == "temporary", env != ""; got != want {
			t.Errorf("env %q: helper got %q", env, out)
		}
	}
}

// TestSocketPeerHelper is the process of TestSocketPeerTemporary that
// connects to its socket.
func TestSocketPeerHelper(t *testing.T) {
	sock := os.Getenv("SOCKET_PEER_HELPER")
	if sock == "" {
		t.Skip("helper process")
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := client.Get("http://shelley/")
	if err != nil {
		os.Stdout.WriteString(err.Error())
		os.Exit(1)
	}
	body, _ := io.ReadAll(resp.Body)
	os.Stdout.Write(body)
	os.Exit(0)
}
