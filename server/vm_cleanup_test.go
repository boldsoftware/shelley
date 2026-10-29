package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

// fakeExeDev is an exe.dev account: VM names to creation times.
type fakeExeDev struct {
	mu       sync.Mutex
	vms      map[string]string
	commands []string
	rmErr    error
	// newGate, if set, holds each new until it receives.
	newGate chan struct{}
}

func (f *fakeExeDev) exec(_ context.Context, _, command string) ([]byte, error) {
	if f.newGate != nil && strings.HasPrefix(command, "new ") {
		<-f.newGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, command)
	args := strings.Fields(command)
	switch args[0] {
	case "ls":
		list := []exeDevVM{}
		if created, ok := f.vms[args[1]]; ok {
			list = append(list, exeDevVM{Name: args[1], CreatedAt: created, SSHDest: args[1] + ".exe.test"})
		}
		return json.Marshal(map[string]any{"vms": list})
	case "cp":
		f.vms[args[2]] = time.Now().UTC().Format(time.RFC3339)
		return []byte(`{}`), nil
	case "rm":
		if f.rmErr != nil {
			return nil, f.rmErr
		}
		delete(f.vms, args[1])
		return []byte(`{}`), nil
	case "new":
		name := strings.TrimPrefix(args[1], "--name=")
		f.vms[name] = time.Now().UTC().Format(time.RFC3339)
		return []byte(`{}`), nil
	}
	return nil, errors.New("unexpected command " + command)
}

func (f *fakeExeDev) removed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.commands {
		if strings.HasPrefix(c, "rm ") {
			out = append(out, c)
		}
	}
	return out
}

// newConversationVMCleanupFixture is a VM conversation whose VM Shelley created at created.
func newConversationVMCleanupFixture(t *testing.T, recorded, actual string) (*Server, *db.DB, string, *fakeExeDev) {
	t.Helper()
	s, database, id, _, _, _ := newConversationVMFixture(t, "true")
	vm, err := database.GetConversationVM(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	vm.Status, vm.VMCreatedAt = db.ConversationVMReady, recorded
	if err := database.UpdateConversationVM(t.Context(), *vm); err != nil {
		t.Fatal(err)
	}
	exe := &fakeExeDev{vms: map[string]string{vm.Name: actual, "base": "2026-01-01T00:00:00Z"}}
	s.conversationVMs.exe = exe.exec
	return s, database, id, exe
}

func archiveConversation(t *testing.T, s *Server, id, op string) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/conversation/"+id+"/"+op, nil)
	if op == "archive" {
		s.handleArchiveConversation(w, r, id)
	} else {
		s.handleUnarchiveConversation(w, r, id)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", op, w.Code, w.Body.String())
	}
}

func conversationVMOf(t *testing.T, database *db.DB, id string) db.ConversationVM {
	t.Helper()
	vm, err := database.GetConversationVM(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return *vm
}

func TestArchiveDeletesConversationVM(t *testing.T) {
	t.Parallel()
	const created = "2026-10-26T20:00:00Z"
	s, database, id, exe := newConversationVMCleanupFixture(t, created, created)
	// A background job on the VM, and a turn waiting on it.
	postChatMessage(t, s, id, "bash-bg@"+conversationVMOf(t, database, id).Name+": exec sleep 300")
	waitFor(t, 10*time.Second, func() bool {
		jobs, err := s.runningBackgroundJobsOf(t.Context(), id)
		return err == nil && len(jobs) == 1
	})
	waitForIdle(t, s, id)

	archiveConversation(t, s, id, "archive")
	if select1(s.conversationVMs.cleanup) != true {
		t.Fatal("archiving did not wake the cleanup worker")
	}
	s.cleanupConversationVMs(t.Context())
	vm := conversationVMOf(t, database, id)
	if vm.Status != db.ConversationVMDeleted || vm.Error != "" || vm.VMCreatedAt != "" {
		t.Fatalf("VM after archive: %+v", vm)
	}
	if rm := exe.removed(); len(rm) != 1 || rm[0] != "rm "+vm.Name+" --json" {
		t.Fatalf("rm commands: %q", rm)
	}
	if _, ok := exe.vms["base"]; !ok {
		t.Fatal("base VM deleted")
	}
	// Its SSH master is closed, though the SSH configuration no longer
	// lists the VM.
	ssh := &fakeSSH{dir: filepath.Dir(s.conversationVMs.ssh)}
	control, err := s.conversationVMControlPath(vm.Name)
	if err != nil {
		t.Fatal(err)
	}
	if exits := ssh.calls(t, "exit"); len(exits) != 1 || exits[0] != vm.Name {
		t.Fatalf("SSH master exits: %v", exits)
	}
	if _, err := os.Stat(control); !os.IsNotExist(err) {
		t.Fatalf("SSH master still running: %v", err)
	}
	// The killed job's notice waits in the queue instead of starting a turn.
	waitFor(t, 10*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), id)
		queued, _ := db.ParseQueuedMessagesStrict(c.QueuedMessages)
		return err == nil && len(queued) == 1 && !c.AgentWorking
	})

	// The archived conversation starts no turns and cannot recreate its VM.
	if _, err := s.prepareConversationVMNow(t.Context(), id); err == nil || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("Prepare on archived conversation: %v", err)
	}
	manager, err := s.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.checkProjectTurn(t.Context()); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("turn on archived VM conversation: %v", err)
	}
	// Sweeping again changes nothing.
	s.cleanupConversationVMs(t.Context())
	if rm := exe.removed(); len(rm) != 1 {
		t.Fatalf("rm commands: %q", rm)
	}

	// Unarchived, it may run again; its next turn copies a fresh VM.
	archiveConversation(t, s, id, "unarchive")
	if vm := conversationVMOf(t, database, id); vm.Status != db.ConversationVMDeleted || vm.ConversationArchived {
		t.Fatalf("unarchived VM: %+v", vm)
	}
	if err := manager.checkProjectTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForActions(t, s)
	if vm := conversationVMOf(t, database, id); vm.Status != db.ConversationVMReady || vm.VMCreatedAt == "" || exe.vms[vm.Name] != vm.VMCreatedAt {
		t.Fatalf("unarchived VM: %+v", vm)
	}
}

func select1(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestArchiveKeepsVMShelleyDidNotCreate(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ recorded, actual, reason string }{
		"never created": {"", "2026-10-26T20:00:00Z", "no record of creating it"},
		"replaced":      {"2026-10-26T20:00:00Z", "2026-10-26T21:00:00Z", "not at 2026-10-26T20:00:00Z"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, database, id, exe := newConversationVMCleanupFixture(t, c.recorded, c.actual)
			archiveConversation(t, s, id, "archive")
			s.cleanupConversationVMs(t.Context())
			vm := conversationVMOf(t, database, id)
			if vm.Status != db.ConversationVMKept || !strings.Contains(vm.Error, c.reason) || len(exe.removed()) != 0 {
				t.Fatalf("VM: %+v, rm %q", vm, exe.removed())
			}
		})
	}
}

// A conversation whose VM was never created has nothing of Shelley's to delete,
// even when exe.dev cannot be reached: it is not retried.
func TestArchiveUncreatedConversationVMIsNotRetried(t *testing.T) {
	t.Parallel()
	s, database, id, exe := newConversationVMCleanupFixture(t, "", "")
	s.conversationVMs.exe = func(context.Context, string, string) ([]byte, error) { return nil, errors.New("403 Forbidden") }
	archiveConversation(t, s, id, "archive")
	s.cleanupConversationVMs(t.Context())
	if vm := conversationVMOf(t, database, id); vm.Status != db.ConversationVMKept || !strings.Contains(vm.Error, "no record") || len(exe.removed()) != 0 {
		t.Fatalf("VM: %+v", vm)
	}
}

func TestConversationVMCleanupRetriesAndStopsOnUnarchive(t *testing.T) {
	t.Parallel()
	const created = "2026-10-26T20:00:00Z"
	s, database, id, exe := newConversationVMCleanupFixture(t, created, created)
	exe.rmErr = errors.New("exe.dev unavailable")
	archiveConversation(t, s, id, "archive")
	if wait := s.cleanupConversationVMs(t.Context()); wait > time.Minute {
		t.Fatalf("retry in %v", wait)
	}
	vm := conversationVMOf(t, database, id)
	if vm.Status != db.ConversationVMDeleting || !strings.Contains(vm.Error, "exe.dev unavailable") {
		t.Fatalf("failed cleanup not recorded: %+v", vm)
	}
	// Backing off: the next sweep does not retry yet.
	s.cleanupConversationVMs(t.Context())
	if n := len(exe.removed()); n != 1 {
		t.Fatalf("%d rm attempts", n)
	}

	// Unarchiving before a retry succeeds keeps the VM.
	archiveConversation(t, s, id, "unarchive")
	if err := s.cleanupConversationVM(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if vm := conversationVMOf(t, database, id); vm.Status != db.ConversationVMPending || vm.Error != "" || vm.VMCreatedAt != created {
		t.Fatalf("unarchived VM: %+v", vm)
	}

	// Archived again, a retry deletes it.
	exe.rmErr = nil
	archiveConversation(t, s, id, "archive")
	if err := s.cleanupConversationVM(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if vm := conversationVMOf(t, database, id); vm.Status != db.ConversationVMDeleted {
		t.Fatalf("VM: %+v", vm)
	}
}

// An archive_on_success action archives through the database; finishing it
// wakes the cleanup worker.
func TestArchivingActionWakesConversationVMCleanup(t *testing.T) {
	t.Parallel()
	const created = "2026-10-26T20:00:00Z"
	s, database, id, _ := newConversationVMCleanupFixture(t, created, created)
	metadata, err := database.GetWorkspaceConversation(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	c, err := database.GetProject(t.Context(), metadata.Project)
	if err != nil {
		t.Fatal(err)
	}
	c.Actions[0].ArchiveOnSuccess = true
	if err := database.UpdateProject(t.Context(), c.Name, *c); err != nil {
		t.Fatal(err)
	}
	select1(s.conversationVMs.cleanup)
	workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	waitForActions(t, s)
	if !select1(s.conversationVMs.cleanup) {
		t.Fatal("cleanup worker not woken")
	}
	s.cleanupConversationVMs(t.Context())
	if vm := conversationVMOf(t, database, id); !vm.ConversationArchived || vm.Status != db.ConversationVMDeleted {
		t.Fatalf("VM: %+v", vm)
	}
}
