package test

import (
	"net"
	"os"
	"sync/atomic"
	"testing"
)

// Off exe.dev, reflection.int.exe.xyz resolves to the link-local
// 169.254.169.254, where nothing answers. A server that asked it about its
// VM (its emoji, its notify integration) as it served its first page took 3s
// to load it, which was most of every e2e test's time in CI.
func TestE2EFirstPageOffExeDevAsksNoOne(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run the e2e tests")
	}
	if _, err := os.Stat("/exe.dev"); err == nil {
		t.Skip("on exe.dev, shelley asks reflection about its VM")
	}
	t.Parallel()
	if err := initE2E(); err != nil {
		t.Fatal(err)
	}
	// The server's outbound requests go through this proxy, which counts
	// and drops them.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var asked atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			asked.Add(1)
			c.Close()
		}
	}()

	s, err := newShelley(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	proxy := "http://" + ln.Addr().String()
	s.setenv("HTTPS_PROXY="+proxy, "HTTP_PROXY="+proxy, "NO_PROXY=", "no_proxy=")
	if err := s.try(func() { s.send("GET", "/new", "", nil) }); err != nil {
		t.Fatalf("%v\nserver log:\n%s", err, s.logTail())
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("serving its first page, shelley made %d outbound connections", n)
	}
}
