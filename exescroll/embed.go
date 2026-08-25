package exescroll

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ConfigureCommand arranges for a child Shelley process to reuse the parent's
// embedded exe-scroll executable when the platform supports it.
func ConfigureCommand(cmd *exec.Cmd) error {
	return configureCommand(cmd)
}

// Exec replaces the current process with the embedded exe-scroll binary.
func Exec(args []string) error {
	if len(embeddedBinary) == 0 {
		return fmt.Errorf("exe-scroll is not embedded for this platform")
	}
	argv := append([]string{"exe-scroll"}, args...)
	return execEmbedded(embeddedBinary, argv, os.Environ())
}

func materialize(data []byte, root string) (string, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	dir := filepath.Join(root, digest)
	path := filepath.Join(dir, "exe-scroll")
	if got, err := os.ReadFile(path); err == nil && sha256.Sum256(got) == sha256.Sum256(data) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create exe-scroll cache: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure exe-scroll cache: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "exe-scroll-*")
	if err != nil {
		return "", fmt.Errorf("create cached exe-scroll: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write cached exe-scroll: %w", err)
	}
	if err := tmp.Chmod(0o700); err != nil {
		tmp.Close()
		return "", fmt.Errorf("chmod cached exe-scroll: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("sync cached exe-scroll: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close cached exe-scroll: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if got, readErr := os.ReadFile(path); readErr == nil && sha256.Sum256(got) == sha256.Sum256(data) {
			return path, nil
		}
		return "", fmt.Errorf("publish cached exe-scroll: %w", err)
	}
	return path, nil
}
