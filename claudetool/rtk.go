package claudetool

import (
	"log/slog"
	"os/exec"
	"strings"
	"sync"
)

// rtkState caches whether RTK is available in PATH.
var (
	rtkOnce   sync.Once
	rtkBinary string // empty if not found
)

// rtkPath returns the path to the rtk binary, or "" if not installed.
func rtkPath() string {
	rtkOnce.Do(func() {
		path, err := exec.LookPath("rtk")
		if err != nil {
			return
		}
		rtkBinary = path
		slog.Info("rtk detected, enabling command optimization", "path", path)
	})
	return rtkBinary
}

// rtkRewrite attempts to rewrite a command using `rtk rewrite`.
// Returns the rewritten command and true if RTK rewrote it,
// or the original command and false otherwise.
func rtkRewrite(command string) (string, bool) {
	path := rtkPath()
	if path == "" {
		return command, false
	}

	out, err := exec.Command(path, "rewrite", command).Output()
	if err != nil {
		// Exit code != 0 means no rewrite available (1), deny (2), or ask (3).
		// In all cases, use the original command.
		return command, false
	}

	rewritten := strings.TrimRight(string(out), "\n")
	if rewritten == "" || rewritten == command {
		return command, false
	}

	slog.Debug("rtk rewrote command", "original", command, "rewritten", rewritten)
	return rewritten, true
}
