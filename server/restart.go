package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"shelley.exe.dev/db"
)

// startupExecutable is the path this process was started from. It is recorded
// at startup because on Linux os.Executable reads /proc/self/exe, which names
// the renamed-aside old binary once an upgrade has replaced the file.
var startupExecutable, startupExecutableErr = os.Executable()

// upgradeTarget returns the file an upgrade replaces: the running binary,
// with symlinks such as Homebrew's bin/shelley -> Caskroom/... resolved.
func upgradeTarget() (string, error) {
	if startupExecutableErr != nil {
		return "", startupExecutableErr
	}
	return filepath.EvalSymlinks(startupExecutable)
}

// restartAfterUpgrade runs the binary DoUpgrade installed.
//
// Under socket activation, exiting is enough: systemd holds the listening
// socket and starts the new binary on the next connection. Otherwise
// (a terminal, tmux, launchd, a systemd unit without a socket), nothing is
// sure to start Shelley again, so the process replaces itself: same PID,
// terminal, arguments, and environment. Listeners are close-on-exec, and the
// new binary binds them again. If that fails, the old binary keeps serving.
func (s *Server) restartAfterUpgrade() {
	if s.SocketActivated {
		os.Exit(0)
	}
	err := startupExecutableErr
	if err == nil {
		// Exec lowers the open-files limit to the one Shelley started with
		// (Go raises it at startup) and does not raise it again on failure.
		var nofile syscall.Rlimit
		getErr := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &nofile)
		err = syscall.Exec(startupExecutable, os.Args, os.Environ())
		if getErr == nil {
			syscall.Setrlimit(syscall.RLIMIT_NOFILE, &nofile)
		}
	}
	s.logger.Error("Failed to restart after upgrade; still running the old binary", "error", err)
	// Turns in flight now carry on here, so a later, unrelated restart must
	// not resume whatever is in flight then.
	if err := s.db.DeleteSetting(context.Background(), db.ResumeAfterUpgradeSettingKey); err != nil {
		s.logger.Error("Failed to cancel resume after upgrade", "error", err)
	}
}

// ReapInheritedChildren reaps the children this process has when it starts.
// A fresh process has none, but one that replaced itself in
// restartAfterUpgrade keeps the old binary's children (background jobs,
// terminal sessions) while their waiters are gone; unreaped, they would
// linger as zombies, which look alive to kill(pid, 0).
//
// Call it before starting any process: os/exec must be the only waiter for
// the children this binary starts.
func ReapInheritedChildren() error {
	pids, err := childPIDs()
	for _, pid := range pids {
		go func() {
			var status syscall.WaitStatus
			for {
				_, err := syscall.Wait4(pid, &status, 0, nil)
				if !errors.Is(err, syscall.EINTR) {
					return
				}
			}
		}()
	}
	return err
}
