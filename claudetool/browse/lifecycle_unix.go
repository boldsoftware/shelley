//go:build unix

package browse

import (
	"log"
	"os/exec"
	"syscall"
)

// configureBrowserCmd sets platform-specific options on the headless-shell
// command so the entire process group can be cleaned up on shutdown. It is
// passed to chromedp via ModifyCmdFunc.
//
// We must replicate chromedp's own Linux behavior (Pdeathsig: SIGKILL) here
// because providing ModifyCmdFunc replaces chromedp's default
// allocateCmdOptions wholesale.
func configureBrowserCmd(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// New process group so we can SIGKILL the whole tree (zygote, renderers,
	// GPU, utility processes) — chromedp only kills the direct child.
	cmd.SysProcAttr.Setpgid = true
	// If our process dies, the kernel SIGKILLs the direct child (Linux only).
	// Descendants will reparent to PID 1 and live on; killBrowserProcessGroup
	// is the proper cleanup path.
	setPdeathsig(cmd.SysProcAttr)
}

// killBrowserProcessGroup sends SIGKILL to the entire process group led by
// pid. It is a no-op if pid is non-positive. Best-effort; logs but does not
// return errors.
func killBrowserProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	// Negative pid means "process group" to kill(2)/killpg(3).
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		log.Printf("browse: failed to kill headless-shell process group %d: %v", pid, err)
	}
}
