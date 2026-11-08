package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// handleExeVMTerminal handles GET /api/exe-vms/{name}/terminal, a websocket
// speaking /api/exec-ws's protocol to a login shell on a VM of the prod tab.
// It runs ssh here in a PTY, as the user's terminals on VM conversations do,
// authenticated by the ssh-agent of the exe.dev integration that lists the
// VM. The browser names a listed VM, never a host. The shell lasts as long
// as the websocket: closing it hangs up ssh, which ends the session.
func (s *Server) handleExeVMTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.requireDevProdTabs(w, r) {
		return
	}
	conn, cols, rows, ok := s.acceptTerminalWS(w, r)
	if !ok {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "internal error")
	ctx := r.Context()
	client, err := s.startExeVMShell(ctx, r.PathValue("name"), cols, rows)
	if err != nil {
		wsjson.Write(ctx, conn, ExecMessage{Type: "error", Data: err.Error()})
		conn.Close(websocket.StatusNormalClosure, "no shell")
		return
	}
	defer client.Close()
	s.bridgeWS(ctx, conn, client, "")
}

// startExeVMShell starts ssh to listed VM name in a PTY. Its errors are one
// line for the user.
func (s *Server) startExeVMShell(ctx context.Context, name string, cols, rows uint16) (*exeVMShell, error) {
	// Fresh: the VM may have been started or stopped since it was listed.
	list, err := s.exeVMs(ctx, true)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(list.VMs, func(vm exeVM) bool { return vm.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("no exe.dev integration of this host lists a VM named %s", name)
	}
	vm := list.VMs[i]
	switch {
	case vm.Status != "running":
		return nil, fmt.Errorf("can't open a terminal on %s: it is %s, not running", vm.Name, vm.Status)
	case !vm.Shell:
		return nil, fmt.Errorf("exe.dev doesn't let the integration %s open a shell on %s", vm.Integration, vm.Name)
	case vm.SSHDest == "":
		return nil, fmt.Errorf("exe.dev lists no SSH destination for %s", vm.Name)
	}
	agent, err := s.agentSocket(ctx, vm.Integration)
	if err != nil {
		return nil, err
	}
	dir, err := s.conversationVMDir()
	if err != nil {
		return nil, err
	}
	logFile, err := os.CreateTemp(dir, vm.Name+".terminal-*.log")
	if err != nil {
		return nil, err
	}
	logFile.Close()
	// No configuration: neither the user's nor conversation VMs' (whose
	// control masters and readiness checks are theirs) applies. All exe.dev
	// VMs share one host key, so trusting the first one seen is safe enough.
	argv := []string{
		s.sshProgram(), "-F", "none", "-tt", "-E", logFile.Name(), "-o", "LogLevel=ERROR",
		"-o", "IdentityAgent=" + agent, "-o", "UserKnownHostsFile=" + filepath.Join(dir, vm.Name+".known_hosts"),
		"-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4", vm.SSHDest,
	}
	pty, err := startDirectPTY("exec "+shellJoin(argv), dir, cols, rows, nil)
	if err != nil {
		os.Remove(logFile.Name())
		return nil, err
	}
	return &exeVMShell{directPTYClient: pty, vm: vm, log: logFile.Name()}, nil
}

// exeVMShell is ssh's terminal, which turns ssh's own failure into why.
type exeVMShell struct {
	*directPTYClient
	vm  exeVM
	log string // ssh's errors (-E)
}

func (c *exeVMShell) Recv() (terminalMessage, error) {
	m, err := c.directPTYClient.Recv()
	// ssh exits 255 when it fails, having logged why.
	if err != nil || m.kind != terminalExit || m.exitCode != 255 {
		return m, err
	}
	log, err := os.ReadFile(c.log)
	if err != nil {
		return terminalMessage{}, err
	}
	lines := strings.Split(string(bytes.TrimSpace(log)), "\n")
	last := strings.TrimPrefix(strings.TrimSpace(lines[len(lines)-1]), "ssh: ")
	switch {
	case last == "":
		return m, nil // the shell's own 255
	case strings.Contains(string(log), "Permission denied"):
		return terminalMessage{kind: terminalFailed, data: fmt.Appendf(nil, "exe.dev denied the integration %s a shell on %s: %s", c.vm.Integration, c.vm.Name, last)}, nil
	default:
		return terminalMessage{kind: terminalFailed, data: fmt.Appendf(nil, "couldn't reach %s: %s", c.vm.Name, last)}, nil
	}
}

func (c *exeVMShell) Close() error {
	os.Remove(c.log)
	return c.directPTYClient.Close()
}
