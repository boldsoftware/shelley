package claudetool

import (
	"os/exec"
	"sync"
	"testing"
)

func TestRtkRewrite_NoRTK(t *testing.T) {
	// Reset the cached state so we re-probe.
	rtkOnce = sync.Once{}
	rtkBinary = ""

	// Temporarily override PATH to ensure rtk is not found.
	t.Setenv("PATH", "/nonexistent")

	cmd, changed := rtkRewrite("git status")
	if changed {
		t.Error("expected no rewrite when rtk is not installed")
	}
	if cmd != "git status" {
		t.Errorf("expected original command, got %q", cmd)
	}
}

func TestRtkRewrite_WithRTK(t *testing.T) {
	// Only run if rtk is actually installed.
	if _, err := exec.LookPath("rtk"); err != nil {
		t.Skip("rtk not installed")
	}

	// Reset the cached state.
	rtkOnce = sync.Once{}
	rtkBinary = ""

	// git status should be rewritable.
	cmd, changed := rtkRewrite("git status")
	if !changed {
		t.Error("expected rtk to rewrite 'git status'")
	}
	if cmd == "git status" {
		t.Error("expected rewritten command to differ from original")
	}
	t.Logf("rtk rewrote 'git status' -> %q", cmd)

	// Reset for next sub-test.
	rtkOnce = sync.Once{}
	rtkBinary = ""

	// A command with no RTK equivalent should pass through unchanged.
	cmd2, changed2 := rtkRewrite("echo hello")
	if changed2 {
		t.Errorf("expected no rewrite for 'echo hello', got %q", cmd2)
	}
}

func TestRtkPath_Caching(t *testing.T) {
	// Reset state.
	rtkOnce = sync.Once{}
	rtkBinary = ""

	// First call probes, second uses cache.
	p1 := rtkPath()
	p2 := rtkPath()
	if p1 != p2 {
		t.Errorf("rtkPath not stable: %q vs %q", p1, p2)
	}
}
