package server

import (
	"context"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db/generated"
)

func (cm *ConversationManager) noteCheckoutEdit(ctx context.Context, vm claudetool.VM, dir string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	machine := ""
	argv := []string{"git", "-C", dir, "rev-parse", "--show-toplevel"}
	if vm != nil {
		machine = vm.Name()
		argv = vm.Command(`exec git -C "$1" rev-parse --show-toplevel`, dir)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = 100 * time.Millisecond
	if root, err := cmd.Output(); err == nil {
		cm.recordCheckout(ctx, machine, strings.TrimSuffix(string(root), "\n"))
	}
}

func (cm *ConversationManager) recordCheckout(ctx context.Context, machine, root string) {
	err := cm.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.RecordCheckoutActivity(ctx, generated.RecordCheckoutActivityParams{
			ConversationID: cm.conversationID, Machine: machine, Worktree: root, TouchedAt: time.Now().UnixNano(),
		})
	})
	if err != nil {
		cm.logger.Error("record checkout activity", "error", err, "worktree", root)
	}
}

func (s *Server) handleRecentCheckouts(w http.ResponseWriter, r *http.Request) {
	m, err := s.requestMachine(r)
	if err != nil {
		machineError(w, err)
		return
	}
	params := generated.RecentCheckoutsParams{Chat: r.URL.Query().Get("conversation_id")}
	if m.vm != nil {
		params.Machine = m.vm.Name()
	}
	var workspace string
	if dir := m.dir(r.URL.Query().Get("cwd")); dir != "" {
		workspace, _ = m.gitRoot(dir)
	}
	var paths []string
	err = s.db.Queries(r.Context(), func(q *generated.Queries) error {
		var err error
		paths, err = q.RecentCheckouts(r.Context(), params)
		return err
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if workspace != "" && !slices.Contains(paths, workspace) {
		paths = append(paths, workspace)
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace": workspace, "paths": paths})
}
