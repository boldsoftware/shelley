//go:build darwin

package exescroll

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func configureCommand(cmd *exec.Cmd) error {
	return nil
}

func execEmbedded(data []byte, argv, env []string) error {
	cache, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("find user cache directory: %w", err)
	}
	path, err := materialize(data, filepath.Join(cache, "shelley", "exe-scroll"))
	if err != nil {
		return err
	}
	if err := syscall.Exec(path, argv, env); err != nil {
		return fmt.Errorf("exec cached exe-scroll: %w", err)
	}
	return nil
}
