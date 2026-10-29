package server

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"shelley.exe.dev/db"
)

// vmSSHInclude is the line Shelley adds to the top of ~/.ssh/config, so
// that every ssh (and scp, rsync, git) on this host, the agent's included,
// knows the conversation VMs of every Shelley instance here by name.
const vmSSHInclude = "Include ~/.ssh/shelley/*.conf"

// vmSSHConfigPath is this instance's SSH configuration for its conversation VMs,
// named for its database so instances don't overwrite each other's.
func (s *Server) vmSSHConfigPath() (string, error) {
	dir := s.conversationVMs.sshConfigDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".ssh", "shelley")
	}
	hash, err := s.dbHash()
	return filepath.Join(dir, hash+".conf"), err
}

// writeVMSSHConfig writes the SSH configuration of the VMs of active
// conversations: host NAME (and its address) authenticates with the
// integration's ssh-agent and multiplexes over the control master
// prepareConversationVMNow starts. A VM that isn't ready fails to connect with a
// message saying why, instead of an unknown host. It also starts the agent
// forwarders the configuration names, and adds vmSSHInclude to
// ~/.ssh/config.
func (s *Server) writeVMSSHConfig(ctx context.Context) error {
	s.conversationVMs.configMu.Lock()
	defer s.conversationVMs.configMu.Unlock()
	vms, err := s.db.ListConversationVMs(ctx)
	if err != nil {
		return err
	}
	path, err := s.vmSSHConfigPath()
	if err != nil {
		return err
	}
	byName := map[string]db.ConversationVM{}
	for _, vm := range vms {
		if vm.ConversationArchived || vm.Status == db.ConversationVMDeleted || vm.Status == db.ConversationVMKept {
			continue
		}
		byName[vm.Name] = vm
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 {
		// Without conversation VMs, Shelley leaves ~/.ssh alone.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	dir, err := s.conversationVMDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "# Conversation VMs of the Shelley instance using %s.\n# Shelley rewrites this file; edits are lost.\n", s.toolSetConfig.DBPath)
	for _, name := range names {
		vm := byName[name]
		agent, err := s.agentSocket(ctx, vm.Integration)
		if err != nil {
			return err
		}
		control, err := s.conversationVMControlPath(name)
		if err != nil {
			return err
		}
		// A VM not yet copied has no address yet; its block only says why.
		user, host, ok := strings.Cut(vm.SSHDest, "@")
		if !ok {
			user, host = "", vm.SSHDest
		}
		hosts := name
		if host != "" {
			hosts += "," + host
		}
		// Every instance's configuration is included; this one's blocks
		// apply only while it runs, since its agent forwarders are here.
		fmt.Fprintf(&out, "\nMatch host %s exec \"kill -0 %d 2>/dev/null\"\n", hosts, os.Getpid())
		if host != "" {
			fmt.Fprintf(&out, "\tHostName %s\n", host)
		}
		if user != "" {
			fmt.Fprintf(&out, "\tUser %s\n", user)
		}
		fmt.Fprintf(&out, "\tIdentityAgent %s\n", agent)
		fmt.Fprintf(&out, "\tUserKnownHostsFile %s\n", filepath.Join(dir, name+".known_hosts"))
		fmt.Fprintf(&out, "\tControlPath %s\n", control)
		// Only Shelley starts masters: one started by a command would hold
		// that command's output open.
		out.WriteString("\tControlMaster no\n\tStrictHostKeyChecking accept-new\n\tBatchMode yes\n\tConnectTimeout 10\n\tServerAliveInterval 15\n\tServerAliveCountMax 4\n")
		status := filepath.Join(dir, name+".status")
		if vm.Status == db.ConversationVMReady {
			os.Remove(status)
			continue
		}
		if err := os.WriteFile(status, []byte(conversationVMStatusMessage(vm)+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(&out, "\tProxyCommand sh -c 'cat %s >&2; exit 1'\n", status)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := removeDeadSSHConfigs(filepath.Dir(path)); err != nil {
		return err
	}
	if s.conversationVMs.sshConfigDir != "" {
		return nil
	}
	return ensureSSHInclude()
}

// sshConfigPID finds the instance in a configuration's Match lines.
var sshConfigPID = regexp.MustCompile(`exec "kill -0 (\d+) `)

// removeDeadSSHConfigs removes other instances' configurations once their
// process is gone (inert by then, but clutter, e.g. from tests and previews).
func removeDeadSSHConfigs(dir string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.conf"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		m := sshConfigPID.FindSubmatch(data)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(string(m[1]))
		if err != nil {
			return err
		}
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// conversationVMStatusMessage says why vm can't be reached yet.
func conversationVMStatusMessage(vm db.ConversationVM) string {
	switch {
	case vm.Status == db.ConversationVMFailed:
		return fmt.Sprintf("Shelley could not connect to VM %s: %s. Shelley retries at the conversation's next turn or ssh_target call.", vm.Name, vm.Error)
	default:
		return fmt.Sprintf("Shelley is still creating VM %s (a copy of %s), which takes a minute or two; retry shortly.", vm.Name, vm.Base)
	}
}

// ensureSSHInclude adds vmSSHInclude to the top of ~/.ssh/config, where
// it applies to every host, unless it's there. A symlinked config (e.g. from
// a dotfiles repository) is written through, keeping the link.
func ensureSSHInclude() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		return err
	}
	path := filepath.Join(home, ".ssh", "config")
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == vmSSHInclude {
			return nil
		}
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	updated := "# Shelley's conversation VMs (see ~/.ssh/shelley).\n" + vmSSHInclude + "\n\n" + string(data)
	tmp := path + ".shelley.tmp"
	if err := os.WriteFile(tmp, []byte(updated), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
