package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
)

// Archiving a conversation deletes its VM. The archived flag itself is the durable
// request: a worker finds archived conversations whose VM is not yet deleted, so
// every archive route (button, API, archive_on_success actions, the
// post-restart archive) is covered, failures are retried with backoff, and
// a restart resumes them. Only the VM Shelley recorded creating for the conversation
// (name and exe.dev creation time) is deleted; anything else is kept and
// says why. Unarchiving stops a pending deletion; after a deletion, the next
// use copies a fresh VM from the base.

type conversationVMRetry struct {
	attempts int
	at       time.Time
}

// conversationVMSweepInterval bounds how long an archive no one announced waits.
const conversationVMSweepInterval = 5 * time.Minute

// pokeConversationVMCleanup wakes the cleanup worker.
func (s *Server) pokeConversationVMCleanup() {
	select {
	case s.conversationVMs.cleanup <- struct{}{}:
	default:
	}
}

// activateConversationVMs starts the cleanup worker's sweeps, once this
// instance has a conversation VM: until then it costs nothing.
func (s *Server) activateConversationVMs() {
	s.conversationVMs.activeOnce.Do(func() { close(s.conversationVMs.active) })
}

// startConversationVMCleanup runs the cleanup worker until shutdown, from
// when this instance has conversation VMs (see activateConversationVMs).
func (s *Server) startConversationVMCleanup() {
	s.workspace.workers.Add(1)
	go func() {
		defer s.workspace.workers.Done()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-s.shutdownCh
			cancel()
		}()
		// The sweep itself reports a failure to list them.
		if vms, err := s.db.ListConversationVMs(ctx); err != nil || len(vms) > 0 {
			s.activateConversationVMs()
		}
		select {
		case <-ctx.Done():
			return
		case <-s.conversationVMs.active:
		}
		for {
			wait := s.cleanupConversationVMs(ctx)
			select {
			case <-ctx.Done():
				return
			case <-s.conversationVMs.cleanup:
			case <-time.After(wait):
			}
		}
	}()
}

// cleanupConversationVMs deletes archived conversations' VMs that are due, and returns how
// long to wait before the next sweep. It also brings the SSH configuration
// up to date with archived and unarchived conversations, and at startup starts the
// agent forwarders it needs.
func (s *Server) cleanupConversationVMs(ctx context.Context) time.Duration {
	if err := s.writeVMSSHConfig(ctx); err != nil {
		s.logger.Error("Conversation VM SSH configuration", "error", err)
	}
	vms, err := s.db.ListConversationVMs(ctx)
	if err != nil {
		s.logger.Error("Conversation VM cleanup: list VMs", "error", err)
		return time.Minute
	}
	next := conversationVMSweepInterval
	for _, vm := range vms {
		done := vm.Status == db.ConversationVMDeleted || vm.Status == db.ConversationVMKept
		if vm.Shared || vm.ConversationArchived && done || !vm.ConversationArchived && vm.Status != db.ConversationVMDeleting {
			continue
		}
		s.conversationVMs.mu.Lock()
		retry := s.conversationVMs.retries[vm.ConversationID]
		s.conversationVMs.mu.Unlock()
		if wait := time.Until(retry.at); wait > 0 {
			next = shorter(next, wait)
			continue
		}
		err := s.cleanupConversationVM(ctx, vm.ConversationID)
		if ctx.Err() != nil {
			return 0
		}
		s.conversationVMs.mu.Lock()
		if err == nil {
			delete(s.conversationVMs.retries, vm.ConversationID)
		} else {
			retry.attempts++
			backoff := shorter(time.Minute<<(retry.attempts-1), time.Hour)
			retry.at = time.Now().Add(backoff)
			if s.conversationVMs.retries == nil {
				s.conversationVMs.retries = map[string]conversationVMRetry{}
			}
			s.conversationVMs.retries[vm.ConversationID] = retry
			next = shorter(next, backoff)
			s.logger.Warn("Conversation VM cleanup failed", "conversationID", vm.ConversationID, "vm", vm.Name, "attempts", retry.attempts, "error", err)
		}
		s.conversationVMs.mu.Unlock()
	}
	return next
}

// cleanupConversationVM deletes the VM of archived conversation id, or, if the conversation was
// unarchived before that happened, lets it keep the VM. A failure is
// recorded on the VM and returned.
func (s *Server) cleanupConversationVM(ctx context.Context, id string) error {
	vm, err := s.db.GetConversationVM(ctx, id)
	if err != nil {
		return err
	}
	if vm.ConversationArchived && vm.Status != db.ConversationVMDeleting {
		vm.Status, vm.Error = db.ConversationVMDeleting, ""
		if err := s.recordConversationVM(ctx, *vm); err != nil {
			return err
		}
	}
	if vm.ConversationArchived {
		// Nothing may keep using the VM: archived conversations start no turns, and
		// their VMs refuse preparation (prepareConversationVMNow).
		if err := s.stopConversationTree(ctx, id); err != nil {
			return s.recordConversationVMCleanupError(ctx, *vm, err)
		}
	}
	lock := s.conversationVMLock(vm.Name)
	lock.Lock()
	defer lock.Unlock()
	if vm, err = s.db.GetConversationVM(ctx, id); err != nil {
		return err
	}
	if !vm.ConversationArchived {
		if vm.Status != db.ConversationVMDeleting {
			return nil
		}
		// Unarchived in time: the next use reconnects to the same VM.
		vm.Status, vm.Error = db.ConversationVMPending, ""
		return s.recordConversationVM(ctx, *vm)
	}
	s.closeConversationVMConnection(*vm)
	status, reason, err := s.deleteConversationVM(ctx, *vm)
	if err != nil {
		return s.recordConversationVMCleanupError(ctx, *vm, err)
	}
	vm.Status, vm.Error = status, reason
	if status == db.ConversationVMDeleted {
		vm.VMCreatedAt, vm.CopyRequestedAt, vm.SSHDest, vm.HTTPSURL = "", "", "", ""
	}
	return s.recordConversationVM(ctx, *vm)
}

func (s *Server) recordConversationVM(ctx context.Context, vm db.ConversationVM) error {
	if err := s.db.UpdateConversationVM(context.WithoutCancel(ctx), vm); err != nil {
		return fmt.Errorf("record conversation VM %s: %w", vm.Name, err)
	}
	s.notifyWorkspaceChanged()
	return nil
}

func (s *Server) recordConversationVMCleanupError(ctx context.Context, vm db.ConversationVM, err error) error {
	vm.Error = "Deleting the VM failed; retrying: " + err.Error()
	return errors.Join(err, s.recordConversationVM(ctx, vm))
}

// deleteConversationVM deletes vm if it is the VM Shelley created for its conversation. It
// returns the resulting status and, for a kept VM, why.
func (s *Server) deleteConversationVM(ctx context.Context, vm db.ConversationVM) (status, reason string, err error) {
	if vm.Shared || vm.Name == vm.Base {
		return "", "", fmt.Errorf("refusing to delete base or shared VM %s", vm.Name)
	}
	found, err := s.findExeDevVM(ctx, vm.Integration, vm.Name)
	created := vm.VMCreatedAt
	if created == "" && found != nil {
		// A copy cut short (e.g. by a restart) that exe.dev completed.
		created = copiedVMCreatedAt(vm, found)
	}
	unowned := fmt.Sprintf("Kept VM %s: Shelley has no record of creating it, so archiving did not delete it.", vm.Name)
	switch {
	case err != nil && vm.VMCreatedAt == "" && vm.CopyRequestedAt == "":
		return db.ConversationVMKept, unowned, nil // nothing of Shelley's to delete
	case err != nil:
		return "", "", err
	case found == nil:
		return db.ConversationVMDeleted, "", nil
	case created == "":
		return db.ConversationVMKept, unowned, nil
	case found.CreatedAt != created:
		return db.ConversationVMKept, fmt.Sprintf("Kept VM %s: it was created at %s, not at %s when Shelley copied it, so archiving did not delete it.",
			vm.Name, found.CreatedAt, created), nil
	}
	if _, err := s.exeDev(ctx, vm.Integration, "rm "+vm.Name+" --json"); err != nil {
		return "", "", err
	}
	if found, err := s.findExeDevVM(ctx, vm.Integration, vm.Name); err != nil || found != nil {
		return "", "", errors.Join(err, fmt.Errorf("exe.dev rm %s reported success, but the VM is still listed", vm.Name))
	}
	s.logger.Info("Deleted archived conversation's VM", "conversationID", vm.ConversationID, "vm", vm.Name)
	return db.ConversationVMDeleted, "", nil
}

// closeConversationVMConnection stops vm's SSH control master, ending every command
// still multiplexed over it, and forgets the VM's host key and log. It names
// the control socket itself: the SSH configuration no longer lists an
// archived conversation's VM.
func (s *Server) closeConversationVMConnection(vm db.ConversationVM) {
	dir, err := s.conversationVMDir()
	if err != nil {
		s.logger.Warn("Conversation VM cleanup: state directory", "vm", vm.Name, "error", err)
		return
	}
	control, err := s.conversationVMControlPath(vm.Name)
	if err != nil {
		s.logger.Warn("Conversation VM cleanup: control socket", "vm", vm.Name, "error", err)
		return
	}
	if _, err := os.Stat(control); err == nil {
		if out, err := exec.Command(s.sshProgram(), "-F", os.DevNull, "-o", "ControlPath="+control, "-O", "exit", vm.Name).CombinedOutput(); err != nil {
			s.logger.Warn("Conversation VM cleanup: close SSH master", "vm", vm.Name, "error", err, "output", string(out))
		}
	}
	os.Remove(filepath.Join(dir, vm.Name+".known_hosts"))
	os.Remove(filepath.Join(dir, vm.Name+".log"))
}

// stopConversationTree cancels the turns of conversation id and its subagents and kills
// their background jobs.
func (s *Server) stopConversationTree(ctx context.Context, id string) error {
	s.mu.Lock()
	s.stoppedParents[id] = true
	manager := s.activeConversations[id]
	s.mu.Unlock()
	if manager != nil && manager.IsAgentWorking() {
		if err := manager.CancelConversation(ctx); err != nil {
			return fmt.Errorf("stop conversation %s: %w", id, err)
		}
	}
	s.cancelSubagentTree(ctx, id)
	queue := []string{id}
	for len(queue) > 0 {
		cid := queue[0]
		queue = queue[1:]
		jobs, err := s.runningBackgroundJobsOf(ctx, cid)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if err := job.Kill(); err != nil && !errors.Is(err, claudetool.ErrBackgroundJobGone) {
				return fmt.Errorf("kill background job %s: %w", job.ID, err)
			}
		}
		children, err := s.db.GetSubagents(ctx, cid)
		if err != nil {
			return err
		}
		for _, child := range children {
			queue = append(queue, child.ConversationID)
		}
	}
	return nil
}

func shorter(a, b time.Duration) time.Duration {
	if a > 0 && a < b {
		return a
	}
	return b
}
