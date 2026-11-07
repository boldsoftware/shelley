package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// Conversation VMs: a project with a VM does its conversations' work on exe.dev VMs: each
// conversation on its own VM, copied from the project's base VM, so concurrent
// conversations never share a checkout, caches, ports, or processes; or, with a
// shared VM, all of its conversations on that one existing VM. The agent loop and
// every tool stay on this host: the agent reaches the VM over SSH, which
// Shelley configures so that `ssh VM 'command'`, scp, rsync and git just
// work (see writeVMSSHConfig), and the conversation's working directory here is a
// scratch directory (see vmScratchDir). Project actions run on the VM.
//
// Everything goes through one exe.dev integration acting as the user (an
// exe-dev integration attached to this host): its /exec endpoint copies and
// lists VMs, and its ssh-agent (TLS, ALPN ssh-agent) authenticates one SSH
// control master per VM, which every connection multiplexes over.

type conversationVMState struct {
	mu      sync.Mutex
	locks   map[string]*sync.Mutex        // per VM name: serializes preparation and deletion
	agents  map[string]string             // integration -> local agent socket
	hosts   map[string]string             // integration -> its host (see exeDevIntegrationHost)
	retries map[int64]conversationVMRetry // per conversation VM: failed cleanup
	// cleanup wakes the VM cleanup worker (see cleanupConversationVMs).
	cleanup chan struct{}
	// active is closed once this instance has conversation VMs, which starts
	// the cleanup worker (see activateConversationVMs).
	active     chan struct{}
	activeOnce sync.Once
	// configMu serializes writeVMSSHConfig.
	configMu sync.Mutex
	// exe, if set, replaces the exe.dev integration (tests).
	exe func(ctx context.Context, integration, command string) ([]byte, error)
	// ssh, if set, replaces the ssh program, and sshConfigDir the directory
	// ~/.ssh/config includes (tests).
	ssh, sshConfigDir string
}

// prepareConversationVM starts provisioning a conversation's VM and its SSH connection, so
// it is usually ready by the time the agent uses it; ssh explains why
// otherwise (see writeVMSSHConfig). New conversations and every turn of a VM conversation
// call it. Shutdown cancels it and waits for it, like project actions, and
// once shutdown waits none starts.
func (s *Server) prepareConversationVM(conversationID string) {
	s.activateConversationVMs()
	s.goWorker(func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-s.shutdownCh:
				cancel()
			case <-ctx.Done():
			}
		}()
		if _, err := s.prepareConversationVMNow(ctx, conversationID); err != nil {
			s.logger.Warn("Conversation VM preparation failed", "conversationID", conversationID, "error", err)
		}
	})
}

// conversationVMArchived refuses work on vm while its conversation is archived:
// archiving deletes an own VM, and drops any VM from the SSH configuration.
func conversationVMArchived(vm *db.ConversationVM) error {
	switch {
	case !vm.ConversationArchived:
		return nil
	case vm.Shared:
		return fmt.Errorf("%w: this conversation is archived; unarchive it to continue on the shared VM %s", db.ErrConflict, vm.Name)
	case vm.Base == "":
		return fmt.Errorf("%w: this conversation is archived, which deletes its VM %s; unarchive it to continue on a new, empty VM",
			db.ErrConflict, vm.Name)
	default:
		return fmt.Errorf("%w: this conversation is archived, which deletes its VM %s; unarchive it to continue on a fresh copy of %s",
			db.ErrConflict, vm.Name, vm.Base)
	}
}

// prepareConversationVMNow creates the conversation's VM if needed and starts its SSH
// control master, recording the outcome on the conversation (and in the UI and the
// SSH configuration). It refuses an archived conversation's VM: archiving
// deletes an own VM, and nothing may create it again until the conversation
// is unarchived. Shared VMs are never created or deleted.
func (s *Server) prepareConversationVMNow(ctx context.Context, conversationID string) (*db.ConversationVM, error) {
	vm, err := s.db.GetConversationVM(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("load conversation VM: %w", err)
	}
	lock := s.conversationVMLock(vm.Name)
	lock.Lock()
	defer lock.Unlock()
	if vm, err = s.db.GetConversationVM(ctx, conversationID); err != nil {
		return nil, fmt.Errorf("load conversation VM: %w", err)
	}
	if err := conversationVMArchived(vm); err != nil {
		return nil, err
	}
	if vm.Status == db.ConversationVMReady && s.conversationVMMasterRunning(ctx, *vm) {
		return vm, nil
	}
	err = s.connectConversationVM(ctx, vm)
	if err != nil {
		vm.Status, vm.Error = db.ConversationVMFailed, err.Error()
	} else {
		vm.Status, vm.Error = db.ConversationVMReady, ""
	}
	if updateErr := s.db.UpdateConversationVM(context.WithoutCancel(ctx), *vm); updateErr != nil {
		return nil, errors.Join(err, fmt.Errorf("record conversation VM %s: %w", vm.Name, updateErr))
	}
	s.notifyWorkspaceChanged()
	if configErr := s.writeVMSSHConfig(context.WithoutCancel(ctx)); configErr != nil {
		err = errors.Join(err, configErr)
	}
	if err != nil {
		return nil, fmt.Errorf("conversation VM %s: %w", vm.Name, err)
	}
	return vm, nil
}

// connectConversationVM copies the base VM unless the conversation's VM exists, recording
// the copy's identity, then starts the control master, waiting for a new
// VM's SSH to come up. A shared VM must already exist. An SSH host never
// involves exe.dev.
func (s *Server) connectConversationVM(ctx context.Context, vm *db.ConversationVM) error {
	if vm.SSHHost() {
		vm.SSHDest = vm.Name
		return s.startConversationVMMaster(ctx, vm, time.Now())
	}
	dir, err := s.conversationVMDir()
	if err != nil {
		return err
	}
	found, err := s.findExeDevVM(ctx, vm.Integration, vm.Name)
	if err != nil {
		return err
	}
	if found == nil && vm.Shared {
		return fmt.Errorf("shared VM %s is not listed by integration %s; Shelley never creates it", vm.Name, vm.Integration)
	}
	// Only a new VM's SSH may take a while to come up.
	deadline := time.Now()
	if found != nil && !vm.Shared && vm.VMCreatedAt == "" {
		// A copy cut short (e.g. by a restart) that exe.dev completed.
		vm.VMCreatedAt = copiedVMCreatedAt(*vm, found)
	}
	if found == nil {
		deadline = deadline.Add(2 * time.Minute)
		// Recorded first: whatever happens to this call, a VM exe.dev
		// creates from now on is Shelley's to delete.
		vm.CopyRequestedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.db.UpdateConversationVM(context.WithoutCancel(ctx), *vm); err != nil {
			return fmt.Errorf("record conversation VM %s: %w", vm.Name, err)
		}
		create := "cp " + vm.Base + " " + vm.Name + " --json"
		if vm.Base == "" {
			create = "new --name=" + vm.Name + " --comment='Shelley conversation " + vm.ConversationID + "' --json"
			// A copy keeps its base's tags; a new VM gets the project's
			// tag, which brings integrations added in Edit project.
			tag, err := s.conversationProjectTag(ctx, vm)
			if err != nil {
				return err
			}
			if tag != "" {
				create += " --tag=" + tag
			}
		}
		if _, err := s.exeDev(ctx, vm.Integration, create); err != nil {
			return err
		}
		// A new VM's host key is new too, even if the name was used before.
		os.Remove(filepath.Join(dir, vm.Name+".known_hosts"))
		if found, err = s.findExeDevVM(ctx, vm.Integration, vm.Name); err != nil {
			return err
		}
		if found == nil {
			return fmt.Errorf("exe.dev %s reported success, but the VM is not listed", create)
		}
		if found.CreatedAt == "" {
			return fmt.Errorf("exe.dev ls %s lists no creation time, so Shelley could not delete it later", vm.Name)
		}
		vm.VMCreatedAt = found.CreatedAt
	}
	if found.SSHDest == "" {
		return fmt.Errorf("exe.dev ls %s lists no ssh_dest", vm.Name)
	}
	vm.SSHDest, vm.HTTPSURL = found.SSHDest, found.HTTPSURL
	if vm.Status != db.ConversationVMReady {
		// Connecting: e.g. no longer deleted after unarchiving, so the SSH
		// configuration lists it again.
		vm.Status, vm.Error = db.ConversationVMPending, ""
	}
	// Recorded now: the VM is Shelley's to delete even if connecting fails,
	// and the SSH configuration needs its address.
	if err := s.db.UpdateConversationVM(context.WithoutCancel(ctx), *vm); err != nil {
		return fmt.Errorf("record conversation VM %s: %w", vm.Name, err)
	}
	if err := s.writeVMSSHConfig(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	return s.startConversationVMMaster(ctx, vm, deadline)
}

// startConversationVMMaster starts vm's SSH control master unless it is
// running, retrying until deadline.
func (s *Server) startConversationVMMaster(ctx context.Context, vm *db.ConversationVM, deadline time.Time) error {
	// Another conversation's preparation already connected a shared VM.
	if s.conversationVMMasterRunning(ctx, *vm) {
		return nil
	}
	dir, err := s.conversationVMDir()
	if err != nil {
		return err
	}
	options, err := s.conversationVMSSHOptions(*vm)
	if err != nil {
		return err
	}
	if !vm.SSHHost() {
		// An exe.dev VM that isn't ready yet has a configuration explaining
		// that to anyone else connecting; Shelley's connections bypass it.
		options = append(options, "-o", "ProxyCommand=none")
	}
	logPath := filepath.Join(dir, vm.Name+".log")
	for {
		os.Remove(logPath)
		args := append([]string{"-o", "ControlMaster=yes", "-o", "ControlPersist=4h"}, options...)
		cmd := exec.CommandContext(ctx, s.sshProgram(), append(args, "-N", "-f", "-E", logPath, vm.Name)...)
		// -f leaves the master running; it must not hold pipes of ours. It
		// outlives a restart (the next process reuses it) and exits when
		// idle, e.g. once a preview instance is gone; ssh connects
		// directly without it, and the next turn starts a new one.
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
		err := cmd.Run()
		if err == nil {
			return s.setUpConversationVM(ctx, vm)
		}
		log, _ := os.ReadFile(logPath)
		err = fmt.Errorf("ssh %s: %w: %s", vm.Name, err, bytes.TrimSpace(log))
		if time.Now().After(deadline) || ctx.Err() != nil {
			return err
		}
		s.logger.Info("Waiting for conversation VM SSH", "vm", vm.Name, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// setUpConversationVM readies an own VM once its master runs: a new, empty
// VM gets its working directory, which a copy of a base has already, and
// both get git's global identity set to the conversation's commit identity,
// if any, so every commit there is the user's, whoever runs git. A shared VM
// or SSH host is someone else's to configure: there the agent's commands
// carry the identity in their environment instead.
func (s *Server) setUpConversationVM(ctx context.Context, vm *db.ConversationVM) error {
	if vm.Shared {
		return nil
	}
	identity, err := s.db.ConversationCommitIdentity(ctx, vm.ConversationID)
	if err != nil {
		return err
	}
	if vm.Base != "" && identity.IsZero() {
		return nil
	}
	target, err := s.conversationVMTarget(vm)
	if err != nil {
		return err
	}
	argv := target.Command(conversationVMSetupScript, strconv.FormatBool(vm.Base == ""), vm.Dir, identity.Name, identity.Email)
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("set up VM %s: %w: %s", vm.Name, err, bytes.TrimSpace(out))
	}
	return nil
}

// conversationVMSetupScript: arguments new ("true" for a new, empty VM),
// dir, then the commit name and email ("" for none). A VM without git has
// no identity to set.
const conversationVMSetupScript = `set -e
if [ "$1" = true ]; then mkdir -p -- "$2"; fi
if [ -n "$3" ] && command -v git >/dev/null; then
  git config --global user.name "$3"
  git config --global user.email "$4"
fi`

func (s *Server) sshProgram() string {
	if s.conversationVMs.ssh != "" {
		return s.conversationVMs.ssh
	}
	return "ssh"
}

// copiedVMCreatedAt returns found's creation time if Shelley's copy of vm
// created it: Shelley asked to copy it, and it did not exist before (allowing
// for clock skew between here and exe.dev). Otherwise it returns "".
func copiedVMCreatedAt(vm db.ConversationVM, found *exeDevVM) string {
	requested, err := time.Parse(time.RFC3339Nano, vm.CopyRequestedAt)
	if err != nil || !createdSince(found.CreatedAt, requested) {
		return ""
	}
	return found.CreatedAt
}

// createdSince reports whether exe.dev's creation time createdAt is not
// before requested, allowing for clock skew between here and exe.dev.
func createdSince(createdAt string, requested time.Time) bool {
	created, err := time.Parse(time.RFC3339, createdAt)
	return err == nil && !created.Before(requested.Add(-time.Minute))
}

// conversationVMMasterRunning reports whether vm's control master is up.
// Like closeConversationVMConnection, it names the control socket itself:
// the SSH configuration lists only active conversations' VMs.
func (s *Server) conversationVMMasterRunning(ctx context.Context, vm db.ConversationVM) bool {
	control, err := s.conversationVMControlPath(vm)
	if err != nil {
		return false
	}
	return exec.CommandContext(ctx, s.sshProgram(), "-F", os.DevNull, "-o", "ControlPath="+control, "-O", "check", vm.Name).Run() == nil
}

// conversationVMControlPath is vm's control socket. Its name is a hash: a
// socket path must fit in about 100 bytes, VM names take up to 42 (ssh
// destinations 255), and ssh appends 17 more while it creates the socket. An
// SSH host's is distinct from an exe.dev VM's of the same name: they are
// different machines.
func (s *Server) conversationVMControlPath(vm db.ConversationVM) (string, error) {
	dir, err := s.conversationVMDir()
	if err != nil {
		return "", err
	}
	key := vm.Name
	if vm.SSHHost() {
		key = "ssh:" + vm.Name
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".ctl"), nil
}

// shellJoin quotes args for a POSIX shell.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// vmScratchDir is the working directory, on this host, of conversation id (the
// conversation owning a VM) and its subagents: their tools run here, but the work
// happens on the VM, so this holds only files fetched for viewing or
// sending. It sits beside the database, so each instance has its own.
func (s *Server) vmScratchDir(id string) (string, error) {
	db, err := filepath.Abs(s.toolSetConfig.DBPath)
	return filepath.Join(filepath.Dir(db), "vm-scratch", id), err
}

// dbHash names this instance's conversation VM state: the hash of its database's
// absolute path.
func (s *Server) dbHash() (string, error) {
	db, err := filepath.Abs(s.toolSetConfig.DBPath)
	sum := sha256.Sum256([]byte(db))
	return hex.EncodeToString(sum[:4]), err
}

// conversationVMTools returns the VM a VM project's conversation (or its
// subagent's) works on, which its bash, patch and change_dir calls can
// target, and its scratch directory here, for calls on this host; nil without
// a VM. The conversation's cwd is on the VM: one still in the scratch
// directory, where conversations used to start, or with none, moves to the
// VM's directory.
func (s *Server) conversationVMTools(ctx context.Context, conversation *generated.Conversation) (claudetool.VM, string, error) {
	vm, err := s.db.GetConversationVM(ctx, conversation.ConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("load conversation VM: %w", err)
	}
	target, err := s.conversationVMTarget(vm)
	if err != nil {
		return nil, "", err
	}
	scratch, err := s.vmScratchDir(vm.ConversationID)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return nil, "", err
	}
	if cwd := derefString(conversation.Cwd); cwd == "" || cwd == scratch {
		if err := s.db.UpdateConversationCwd(ctx, conversation.ConversationID, vm.Dir); err != nil {
			return nil, "", err
		}
		conversation.Cwd = &vm.Dir
	}
	return target, scratch, nil
}

func (s *Server) conversationVMTarget(vm *db.ConversationVM) (conversationVMTarget, error) {
	options, err := s.conversationVMSSHOptions(*vm)
	return conversationVMTarget{s: s, owner: vm.ConversationID, name: vm.Name, dir: vm.Dir, options: options}, err
}

// conversationVMSSHOptions are the ssh options that reach vm. An exe.dev VM's
// are all in this instance's SSH configuration. An SSH host is the user's, so
// their own configuration (keys, agent, aliases, ProxyJump) applies, plus
// what Shelley needs: its control master, and failing rather than prompting.
// Its new host keys are trusted on first use, as exe.dev VMs' are.
func (s *Server) conversationVMSSHOptions(vm db.ConversationVM) ([]string, error) {
	if !vm.SSHHost() {
		config, err := s.vmSSHConfigPath()
		return []string{"-F", config}, err
	}
	control, err := s.conversationVMControlPath(vm)
	return []string{
		"-o", "ControlPath=" + control, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4",
	}, err
}

// conversationVMTarget is a VM conversation's claudetool.VM: its VM over SSH,
// through its SSH options and the VM's control master.
type conversationVMTarget struct {
	s                *Server
	owner, name, dir string
	options          []string
}

func (t conversationVMTarget) Name() string { return t.name }
func (t conversationVMTarget) Dir() string  { return t.dir }

func (t conversationVMTarget) Prepare(ctx context.Context) error {
	_, err := t.s.prepareConversationVMNow(ctx, t.owner)
	return err
}

func (t conversationVMTarget) Command(script string, args ...string) []string {
	// Only Shelley starts masters: one started by a command would hold that
	// command's output open.
	return t.ssh("-T", script, args...)
}

// Terminal is Command with a terminal on the VM, for the user's.
func (t conversationVMTarget) Terminal(script string, args ...string) []string {
	return t.ssh("-tt", script, args...)
}

func (t conversationVMTarget) ssh(tty, script string, args ...string) []string {
	argv := append([]string{t.s.sshProgram(), "-o", "ControlMaster=no"}, t.options...)
	return append(argv, tty, t.name, shellJoin(append([]string{"bash", "-c", script, "shelley"}, args...)))
}

// conversationVMDir holds this instance's SSH control sockets, logs, known
// hosts, and agent sockets, beside its database: its agent sockets
// authenticate as the user, so only the user may reach them. It is per
// database so a preview instance never reuses a master forwarding to another
// instance's API.
func (s *Server) conversationVMDir() (string, error) {
	hash, err := s.dbHash()
	if err != nil {
		return "", err
	}
	db, err := filepath.Abs(s.toolSetConfig.DBPath)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(db), "vm-ssh-"+hash)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !info.IsDir() || !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0o700 {
		return "", fmt.Errorf("conversation VM state directory %s must be a directory of this user's with mode 0700", dir)
	}
	return dir, nil
}

// exeDev runs an exe.dev CLI command through integration and returns its
// JSON output.
func (s *Server) exeDev(ctx context.Context, integration, command string) ([]byte, error) {
	if s.conversationVMs.exe != nil {
		return s.conversationVMs.exe(ctx, integration, command)
	}
	host, err := s.exeDevIntegrationHost(ctx, integration)
	if err != nil {
		return nil, err
	}
	return exeDev(ctx, host, command)
}

// exeDevIntegrationHost returns the host of exe-dev integration name, which
// must be attached to this host: personal and team integrations, in this
// exe.dev environment, have different hosts.
func (s *Server) exeDevIntegrationHost(ctx context.Context, name string) (string, error) {
	s.conversationVMs.mu.Lock()
	host, ok := s.conversationVMs.hosts[name]
	s.conversationVMs.mu.Unlock()
	if ok {
		return host, nil
	}
	env, integrations, err := s.listIntegrations(ctx)
	if err != nil {
		return "", fmt.Errorf("list exe.dev integrations: %w", err)
	}
	for _, integration := range integrations {
		if integration.Type == "exe-dev" && integration.Name == name {
			host = env.IntegrationHost(name, integration.Team)
			s.conversationVMs.mu.Lock()
			if s.conversationVMs.hosts == nil {
				s.conversationVMs.hosts = map[string]string{}
			}
			s.conversationVMs.hosts[name] = host
			s.conversationVMs.mu.Unlock()
			return host, nil
		}
	}
	return "", fmt.Errorf("exe.dev integration %q is not attached to this host", name)
}

func exeDev(ctx context.Context, host, command string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/exec", strings.NewReader(command))
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exe.dev %q: %w", command, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("exe.dev %q: %w", command, err)
	}
	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
		return nil, &exeDevError{Command: command, Message: failure.Error}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exe.dev %q: %s: %s", command, resp.Status, bytes.TrimSpace(body))
	}
	return body, nil
}

// exeDevError is exe.dev's refusal of a command, e.g. one the integration's
// key may not run.
type exeDevError struct{ Command, Message string }

func (e *exeDevError) Error() string { return fmt.Sprintf("exe.dev %q: %s", e.Command, e.Message) }

type exeDevVM struct {
	Name      string   `json:"vm_name"`
	CreatedAt string   `json:"created_at"`
	SSHDest   string   `json:"ssh_dest"`
	HTTPSURL  string   `json:"https_url"`
	Status    string   `json:"status"`
	Tags      []string `json:"tags"`
}

// findExeDevVM returns the VM named name, or nil if there is none.
func (s *Server) findExeDevVM(ctx context.Context, integration, name string) (*exeDevVM, error) {
	out, err := s.exeDev(ctx, integration, "ls "+name+" --json")
	if err != nil {
		return nil, err
	}
	var list struct {
		VMs []exeDevVM `json:"vms"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("exe.dev ls: %w", err)
	}
	for _, vm := range list.VMs {
		if vm.Name == name {
			return &vm, nil
		}
	}
	return nil, nil
}

// agentSocket returns a local SSH agent socket backed by integration's
// ssh-agent, starting its forwarder once; it stops at shutdown.
func (s *Server) agentSocket(ctx context.Context, integration string) (string, error) {
	host, err := s.exeDevIntegrationHost(ctx, integration)
	if err != nil {
		return "", err
	}
	s.conversationVMs.mu.Lock()
	defer s.conversationVMs.mu.Unlock()
	if path, ok := s.conversationVMs.agents[integration]; ok {
		return path, nil
	}
	dir, err := s.conversationVMDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, integration+".agent")
	os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return "", fmt.Errorf("listen for %s ssh-agent: %w", integration, err)
	}
	go func() {
		<-s.shutdownCh
		listener.Close()
	}()
	go func() {
		for {
			conn, err := listener.Accept()
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				s.logger.Error("Conversation VM agent forwarder stopped", "integration", integration, "error", err)
				return
			}
			go func() {
				defer conn.Close()
				upstream, err := tls.Dial("tcp", host+":443", &tls.Config{ServerName: host, NextProtos: []string{"ssh-agent"}})
				if err != nil {
					s.logger.Warn("Conversation VM agent connection failed", "integration", integration, "error", err)
					return
				}
				defer upstream.Close()
				go func() {
					io.Copy(upstream, conn)
					upstream.Close() // the client is done: so is the agent's side
				}()
				io.Copy(conn, upstream)
			}()
		}
	}()
	if s.conversationVMs.agents == nil {
		s.conversationVMs.agents = map[string]string{}
	}
	s.conversationVMs.agents[integration] = path
	return path, nil
}

func (s *Server) conversationVMLock(name string) *sync.Mutex {
	s.conversationVMs.mu.Lock()
	defer s.conversationVMs.mu.Unlock()
	if s.conversationVMs.locks == nil {
		s.conversationVMs.locks = map[string]*sync.Mutex{}
	}
	if s.conversationVMs.locks[name] == nil {
		s.conversationVMs.locks[name] = &sync.Mutex{}
	}
	return s.conversationVMs.locks[name]
}

// conversationProjectTag is the exe.dev tag of the conversation's project
// if an integration uses it, else "".
func (s *Server) conversationProjectTag(ctx context.Context, vm *db.ConversationVM) (string, error) {
	conv, err := s.db.GetConversationByID(ctx, vm.ConversationID)
	if err != nil {
		return "", err
	}
	project := db.ParseConversationOptions(conv.ConversationOptions).Profile
	if project == "" {
		return "", nil
	}
	return s.projectTagInUse(ctx, vm.Integration, project)
}
