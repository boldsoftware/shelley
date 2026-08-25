//go:build linux

package exescroll

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestExecutableCacheRetriesAfterFailure(t *testing.T) {
	var cache executableCache
	attempts := 0
	wantErr := errors.New("temporary memfd failure")
	file, err := os.CreateTemp(t.TempDir(), "shared-executable")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	create := func() (*os.File, error) {
		attempts++
		if attempts == 1 {
			return nil, wantErr
		}
		return file, nil
	}
	if _, err := cache.get(create); !errors.Is(err, wantErr) {
		t.Fatalf("first get error = %v, want %v", err, wantErr)
	}
	got, err := cache.get(create)
	if err != nil {
		t.Fatal(err)
	}
	again, err := cache.get(create)
	if err != nil {
		t.Fatal(err)
	}
	if got != file || again != file || attempts != 2 {
		t.Fatalf("cache result=(%p,%p), attempts=%d", got, again, attempts)
	}
}

func TestConfigureCommandSharesSealedMemfd(t *testing.T) {
	if os.Getenv("SHELLEY_EXE_SCROLL_FD_HELPER") == "1" {
		fd, err := strconv.Atoi(os.Getenv(inheritedFDEnv))
		if err != nil {
			t.Fatal(err)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%d:%d\n", stat.Dev, stat.Ino)
		return
	}

	run := func() string {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestConfigureCommandSharesSealedMemfd$")
		cmd.Env = append(os.Environ(), "SHELLEY_EXE_SCROLL_FD_HELPER=1")
		if err := ConfigureCommand(cmd); err != nil {
			t.Fatal(err)
		}
		shared := cmd.ExtraFiles[len(cmd.ExtraFiles)-1]
		if _, err := shared.WriteAt([]byte{0}, 0); !errors.Is(err, unix.EPERM) {
			t.Fatalf("shared memfd is writable: %v", err)
		}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("inspect inherited memfd: %v\n%s", err, output)
		}
		return strings.TrimSpace(string(output))
	}

	first := run()
	second := run()
	if first == "" || first != second {
		t.Fatalf("memfd identities differ: %q != %q", first, second)
	}
}
