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
// conversations never share a checkout, caches, ports, or processes. The agent loop and
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
	locks   map[string]*sync.Mutex         // per VM name: serializes preparation and deletion
	agents  map[string]string              // integration -> local agent socket
	hosts   map[string]string              // integration -> its host (see exeDevIntegrationHost)
	retries map[string]conversationVMRetry // per conversation: failed VM cleanup
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
// call it. Shutdown cancels it and waits for it, like project actions.
func (s *Server) prepareConversationVM(conversationID string) {
	s.activateConversationVMs()
	s.workspace.workers.Add(1)
	go func() {
		defer s.workspace.workers.Done()
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
	}()
}

// conversationVMArchived refuses work on vm while its conversation is archived:
// archiving deletes the VM, and drops it from the SSH configuration.
func conversationVMArchived(vm *db.ConversationVM) error {
	if !vm.ConversationArchived {
		return nil
	}
	return fmt.Errorf("%w: this conversation is archived, which deletes its VM %s; unarchive it to continue on a fresh copy of %s",
		db.ErrConflict, vm.Name, vm.Base)
}

// prepareConversationVMNow creates the conversation's VM if needed and starts its SSH
// control master, recording the outcome on the conversation (and in the UI and the
// SSH configuration). It refuses an archived conversation's VM: archiving
// deletes the VM, and nothing may create it again until the conversation
// is unarchived.
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
	if vm.Status == db.ConversationVMReady && s.conversationVMMasterRunning(ctx, vm.Name) {
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
// VM's SSH to come up.
func (s *Server) connectConversationVM(ctx context.Context, vm *db.ConversationVM) error {
	dir, err := s.conversationVMDir()
	if err != nil {
		return err
	}
	found, err := s.findExeDevVM(ctx, vm.Integration, vm.Name)
	if err != nil {
		return err
	}
	// Only a new VM's SSH may take a while to come up.
	deadline := time.Now()
	if found != nil && vm.VMCreatedAt == "" {
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
		if _, err := s.exeDev(ctx, vm.Integration, "cp "+vm.Base+" "+vm.Name+" --json"); err != nil {
			return err
		}
		// A new VM's host key is new too, even if the name was used before.
		os.Remove(filepath.Join(dir, vm.Name+".known_hosts"))
		if found, err = s.findExeDevVM(ctx, vm.Integration, vm.Name); err != nil {
			return err
		}
		if found == nil {
			return fmt.Errorf("exe.dev cp %s %s reported success, but the VM is not listed", vm.Base, vm.Name)
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
	config, err := s.vmSSHConfigPath()
	if err != nil {
		return err
	}
	logPath := filepath.Join(dir, vm.Name+".log")
	for {
		os.Remove(logPath)
		// The VM isn't ready yet, so its configuration explains that to
		// anyone else connecting; this connection bypasses it.
		cmd := exec.CommandContext(ctx, s.sshProgram(), "-F", config, "-o", "ProxyCommand=none",
			"-o", "ControlMaster=yes", "-o", "ControlPersist=4h", "-N", "-f", "-E", logPath, vm.Name)
		// -f leaves the master running; it must not hold pipes of ours. It
		// outlives a restart (the next process reuses it) and exits when
		// idle, e.g. once a preview instance is gone; ssh connects
		// directly without it, and the next turn starts a new one.
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
		err := cmd.Run()
		if err == nil {
			return nil
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

// conversationVMMasterRunning reports whether VM name's control master is up.
// Like closeConversationVMConnection, it names the control socket itself:
// the SSH configuration lists only active conversations' VMs.
func (s *Server) conversationVMMasterRunning(ctx context.Context, name string) bool {
	control, err := s.conversationVMControlPath(name)
	if err != nil {
		return false
	}
	return exec.CommandContext(ctx, s.sshProgram(), "-F", os.DevNull, "-o", "ControlPath="+control, "-O", "check", name).Run() == nil
}

// conversationVMControlPath is VM name's control socket. Its name is a hash:
// a socket path must fit in about 100 bytes, VM names take up to 42, and ssh
// appends 17 more while it creates the socket.
func (s *Server) conversationVMControlPath(name string) (string, error) {
	dir, err := s.conversationVMDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(name))
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

// conversationVMTools moves a VM project's conversation (or its subagent's) to its
// scratch directory here and returns the VM its bash and patch calls can
// target; nil without a VM.
func (s *Server) conversationVMTools(ctx context.Context, conversation *generated.Conversation) (claudetool.VM, error) {
	vm, err := s.db.GetConversationVM(ctx, conversation.ConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load conversation VM: %w", err)
	}
	target, err := s.conversationVMTarget(vm)
	if err != nil {
		return nil, err
	}
	dir, err := s.vmScratchDir(vm.ConversationID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if derefString(conversation.Cwd) != dir {
		if err := s.db.UpdateConversationCwd(ctx, conversation.ConversationID, dir); err != nil {
			return nil, err
		}
		conversation.Cwd = &dir
	}
	return target, nil
}

func (s *Server) conversationVMTarget(vm *db.ConversationVM) (conversationVMTarget, error) {
	config, err := s.vmSSHConfigPath()
	return conversationVMTarget{s: s, owner: vm.ConversationID, name: vm.Name, dir: vm.Dir, config: config}, err
}

// conversationVMTarget is a VM conversation's claudetool.VM: its VM over SSH, through this
// instance's SSH configuration and the VM's control master.
type conversationVMTarget struct {
	s                        *Server
	owner, name, dir, config string
}

func (t conversationVMTarget) Name() string { return t.name }
func (t conversationVMTarget) Dir() string  { return t.dir }

func (t conversationVMTarget) Prepare(ctx context.Context) error {
	_, err := t.s.prepareConversationVMNow(ctx, t.owner)
	return err
}

func (t conversationVMTarget) Command(script string, args ...string) []string {
	return []string{
		t.s.sshProgram(), "-F", t.config, "-T", t.name,
		shellJoin(append([]string{"bash", "-c", script, "shelley"}, args...)),
	}
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
		return nil, fmt.Errorf("exe.dev %q: %s", command, failure.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exe.dev %q: %s: %s", command, resp.Status, bytes.TrimSpace(body))
	}
	return body, nil
}

type exeDevVM struct {
	Name      string `json:"vm_name"`
	CreatedAt string `json:"created_at"`
	SSHDest   string `json:"ssh_dest"`
	HTTPSURL  string `json:"https_url"`
	Status    string `json:"status"`
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
