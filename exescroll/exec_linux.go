//go:build linux

package exescroll

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const inheritedFDEnv = "SHELLEY_EXE_SCROLL_FD"

type executableCache struct {
	mu   sync.Mutex
	file *os.File
}

var sharedExecutable executableCache

func (c *executableCache) get(create func() (*os.File, error)) (*os.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file != nil {
		return c.file, nil
	}
	file, err := create()
	if err != nil {
		return nil, err
	}
	c.file = file
	return file, nil
}

func configureCommand(cmd *exec.Cmd) error {
	file, err := sharedExecutableFile()
	if err != nil {
		return err
	}
	childFD := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, file)
	cmd.Env = setInheritedFD(cmd.Env, childFD)
	return nil
}

func setInheritedFD(env []string, fd int) []string {
	if env == nil {
		env = os.Environ()
	}
	prefix := inheritedFDEnv + "="
	filtered := env[:0]
	for _, value := range env {
		if !strings.HasPrefix(value, prefix) {
			filtered = append(filtered, value)
		}
	}
	return append(filtered, prefix+strconv.Itoa(fd))
}

func removeInheritedFD(env []string) []string {
	prefix := inheritedFDEnv + "="
	filtered := env[:0]
	for _, value := range env {
		if !strings.HasPrefix(value, prefix) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func sharedExecutableFile() (*os.File, error) {
	return sharedExecutable.get(func() (*os.File, error) {
		return newMemfd(embeddedBinary)
	})
}

func newMemfd(data []byte) (*os.File, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("exe-scroll is not embedded for this platform")
	}
	fd, err := unix.MemfdCreate("shelley-exe-scroll", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("memfd_create exe-scroll: %w", err)
	}
	file := os.NewFile(uintptr(fd), "shelley-exe-scroll")
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open exe-scroll memfd")
	}
	if err := writeAll(file, data); err != nil {
		file.Close()
		return nil, fmt.Errorf("write exe-scroll memfd: %w", err)
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		file.Close()
		return nil, fmt.Errorf("chmod exe-scroll memfd: %w", err)
	}
	seals := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		file.Close()
		return nil, fmt.Errorf("seal exe-scroll memfd: %w", err)
	}
	return file, nil
}

func execEmbedded(data []byte, argv, env []string) error {
	if rawFD := os.Getenv(inheritedFDEnv); rawFD != "" {
		fd, err := strconv.Atoi(rawFD)
		if err != nil || fd < 3 {
			return fmt.Errorf("invalid %s %q", inheritedFDEnv, rawFD)
		}
		file := os.NewFile(uintptr(fd), "inherited-shelley-exe-scroll")
		if file == nil {
			return fmt.Errorf("open inherited exe-scroll memfd")
		}
		unix.CloseOnExec(fd)
		return execMemfd(file, argv, removeInheritedFD(env))
	}
	file, err := newMemfd(data)
	if err != nil {
		return err
	}
	return execMemfd(file, argv, env)
}

func execMemfd(file *os.File, argv, env []string) error {
	path := fmt.Sprintf("/proc/self/fd/%d", file.Fd())
	if err := syscall.Exec(path, argv, env); err != nil {
		_ = file.Close()
		return fmt.Errorf("exec embedded exe-scroll: %w", err)
	}
	return nil
}
