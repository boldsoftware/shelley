package exescroll

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedBinaryRuns(t *testing.T) {
	if os.Getenv("SHELLEY_EXE_SCROLL_TEST_HELPER") == "1" {
		if err := Exec([]string{"--version"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedBinaryRuns$")
	cmd.Env = append(os.Environ(), "SHELLEY_EXE_SCROLL_TEST_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("embedded exe-scroll --version: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("exe-scroll 0.1.0")) {
		t.Fatalf("unexpected version output %q", out)
	}
}

func TestMaterializeCachesVerifiedBinary(t *testing.T) {
	root := t.TempDir()
	data := []byte("fake executable")
	path, err := materialize(data, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		t.Fatalf("materialized outside cache: %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want 700", info.Mode().Perm())
	}

	again, err := materialize(data, root)
	if err != nil {
		t.Fatal(err)
	}
	if again != path {
		t.Fatalf("cache path changed: %q != %q", again, path)
	}
}
