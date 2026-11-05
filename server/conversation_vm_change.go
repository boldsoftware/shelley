package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// changeConversationVMRequest is the body of PUT /api/conversation/ID/vm:
// where the conversation's tools run from its next turn. VM is nil for this
// host; an existing machine (shared_vm, with an exe.dev integration or none
// for an ssh destination); or a new VM of its own (integration, and base to
// copy, or none for an empty one). Dir is the working directory there.
type changeConversationVMRequest struct {
	VM  *db.ProjectVM `json:"vm"`
	Dir string        `json:"dir"`
}

// handleChangeConversationVM moves an idle top-level conversation to another
// machine. It keeps an own VM it leaves until the conversation is archived.
func (s *Server) handleChangeConversationVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req changeConversationVMRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	done, ok := s.beginConversationMutation(w, r, id, "vm")
	if !ok {
		return
	}
	defer done()
	ctx := r.Context()
	conversation, err := s.db.GetConversationByID(ctx, id)
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	switch {
	case conversation.ParentConversationID != nil:
		err = fmt.Errorf("%w: a subagent works on its conversation's machine", db.ErrConflict)
	case conversation.Archived:
		err = fmt.Errorf("%w: the conversation is archived", db.ErrConflict)
	case conversation.IsDraft:
		err = fmt.Errorf("%w: the conversation is still a draft", db.ErrConflict)
	case s.isAssistant(id):
		err = fmt.Errorf("%w: the Assistant runs on this host", db.ErrConflict)
	}
	if err == nil {
		req.Dir, err = s.conversationVMChangeDir(ctx, id, req)
	}
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	old, err := s.db.GetConversationVM(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		old, err = nil, nil
	}
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	manager, err := s.getOrCreateConversationManager(ctx, id, r.Header.Get("X-ExeDev-Email"))
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	// The loop's tools are built for its machine: the conversation's
	// managers, its subagents' included, are rebuilt at their next turn.
	err = manager.resetLoopAfter(false, func() error {
		working, err := s.conversationTreeWorking(ctx, id)
		if err != nil {
			return err
		}
		if working {
			return fmt.Errorf("%w: finish or stop the current turn, and its subagents', to change the machine", db.ErrConflict)
		}
		return s.db.ChangeConversationVM(ctx, id, req.VM, req.Dir)
	})
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	if err := s.dropConversationTreeManagers(ctx, id); err != nil {
		s.logger.Error("Conversation moved, but its loops were not rebuilt", "conversationID", id, "error", err)
	}
	if old != nil && !old.Shared {
		s.closeConversationVMConnection(*old)
	}
	if err := s.recordMachineChangeNotice(ctx, id, derefString(conversation.Cwd), req); err != nil {
		s.logger.Error("Conversation moved, but the agent was not told", "conversationID", id, "error", err)
	}
	if err := s.writeVMSSHConfig(context.WithoutCancel(ctx)); err != nil {
		s.logger.Error("Conversation VM SSH configuration", "error", err)
	}
	s.notifyWorkspaceChanged()
	var vm *db.ConversationVM
	if req.VM != nil {
		if vm, err = s.db.GetConversationVM(ctx, id); err != nil {
			s.workspaceHTTPError(w, err)
			return
		}
		// Ready, usually, by the time the user's next message needs it.
		s.prepareConversationVM(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"vm": vm, "cwd": req.Dir})
}

// conversationVMChangeDir is the working directory a conversation moving
// per req works in: req.Dir, or the default for the machine, its project's
// directory if it has one there, else the home directory of this host or an
// exe.dev VM. An SSH host's is unknown, so it needs req.Dir. A directory here
// must exist; one elsewhere is checked by the first command there.
func (s *Server) conversationVMChangeDir(ctx context.Context, id string, req changeConversationVMRequest) (string, error) {
	var project *db.Project
	metadata, err := s.db.GetWorkspaceConversation(ctx, id)
	switch {
	case err == nil:
		if project, err = s.db.GetProject(ctx, metadata.Project); err != nil {
			return "", err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	dir := req.Dir
	switch {
	case dir != "":
	case req.VM == nil && project != nil && project.VM == nil && project.Cwd != "":
		dir = project.Cwd
	case req.VM == nil:
		if dir, err = os.UserHomeDir(); err != nil {
			return "", err
		}
	case req.VM.Integration == "" && req.VM.SharedVM != "":
		return "", fmt.Errorf("%w: dir is required for an SSH host", db.ErrInvalid)
	case project != nil && project.VM != nil:
		dir = project.VMDir()
	default:
		dir = "/home/exedev"
	}
	if req.VM == nil {
		return dir, validateConversationDirectory(dir)
	}
	return dir, nil
}

// conversationTreeWorking reports whether conversation id or any of its
// subagents is mid-turn.
func (s *Server) conversationTreeWorking(ctx context.Context, id string) (bool, error) {
	ids, err := s.conversationTree(ctx, id)
	for _, cid := range ids {
		if s.IsAgentWorking(cid) {
			return true, nil
		}
	}
	return false, err
}

// conversationTree is conversation id and its subagents, recursively.
func (s *Server) conversationTree(ctx context.Context, id string) ([]string, error) {
	ids := []string{id}
	for i := 0; i < len(ids); i++ {
		children, err := s.db.GetSubagents(ctx, ids[i])
		if err != nil {
			return ids, err
		}
		for _, child := range children {
			ids = append(ids, child.ConversationID)
		}
	}
	return ids, nil
}

// dropConversationTreeManagers stops the loops of conversation id and its
// subagents and forgets their managers, so the next use builds them afresh.
func (s *Server) dropConversationTreeManagers(ctx context.Context, id string) error {
	ids, err := s.conversationTree(ctx, id)
	var managers []*ConversationManager
	s.mu.Lock()
	for _, cid := range ids {
		if manager := s.activeConversations[cid]; manager != nil {
			managers = append(managers, manager)
			delete(s.activeConversations, cid)
		}
	}
	s.mu.Unlock()
	for _, manager := range managers {
		manager.stopLoop()
	}
	return err
}

// recordMachineChangeNotice tells the agent its conversation moved, as a
// working directory change notice (see recordCwdChangeNotice) naming the
// machine. The next loop reads it from the database.
func (s *Server) recordMachineChangeNotice(ctx context.Context, id, from string, req changeConversationVMRequest) error {
	machine := "this Shelley host"
	text := fmt.Sprintf("[The user moved this conversation's work to %s, in %s. Tools run here again, without ssh_target.]", machine, req.Dir)
	if req.VM != nil {
		vm, err := s.db.GetConversationVM(ctx, id)
		if err != nil {
			return err
		}
		machine = vm.Name
		text = fmt.Sprintf("[The user moved this conversation's work to %s, in %s. The project_vm section of the system prompt describes it.]", machine, req.Dir)
	}
	message, err := s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: id,
		Type:           db.MessageTypeUser,
		LLMData:        llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: text}}},
		UserData:       map[string]any{"cwd_change": true, "from": from, "to": req.Dir, "machine": machine},
		UsageData:      llm.Usage{},
	})
	if err != nil {
		return err
	}
	conversation, err := s.db.GetConversationByID(ctx, id)
	if err != nil {
		return err
	}
	if s.streamPub != nil {
		s.streamPub.Broadcast(StreamResponse{
			ConversationID: id, Messages: toAPIMessages([]generated.Message{*message}), Conversation: conversation,
		})
	}
	return nil
}
