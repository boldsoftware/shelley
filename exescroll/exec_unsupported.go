//go:build !linux && !darwin

package exescroll

import (
	"fmt"
	"os/exec"
)

func configureCommand(cmd *exec.Cmd) error {
	return nil
}

func execEmbedded(data []byte, argv, env []string) error {
	return fmt.Errorf("embedded exe-scroll is unsupported on this platform")
}
