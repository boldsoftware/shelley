//go:build !unix

package browse

import "os/exec"

func configureBrowserCmd(cmd *exec.Cmd) {}

func killBrowserProcessGroup(int) {}
