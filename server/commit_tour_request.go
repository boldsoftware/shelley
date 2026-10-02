package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"shelley.exe.dev/committour"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/skills"
)

const (
	commitTourKind    = db.CommitTourKind
	commitTourTimeout = 30 * time.Minute

	commitTourStatusAbsent   = "absent"
	commitTourStatusBuilding = "building"
	commitTourStatusPresent  = "present"
	commitTourStatusFailed   = "failed"
)

var (
	errNoVerifiedCommitTour = errors.New("no verified commit tour")
	errCommitTourDraft      = errors.New("start the conversation before requesting a tour")
	errCommitTourNested     = errors.New("commit tours can only be requested from top-level conversations")
)

type commitTourTarget struct {
	Repository string
	Worktree   string
	Hash       string
}

type CommitTourStatus struct {
	Status               string `json:"status"`
	Hash                 string `json:"hash"`
	Repository           string `json:"repository,omitempty"`
	WorkerConversationID string `json:"worker_conversation_id,omitempty"`
	WorkerSlug           string `json:"worker_slug,omitempty"`
	Error                string `json:"error,omitempty"`
}

type commitTourJob struct {
	key       string
	parentID  string
	childID   string
	slug      string
	target    commitTourTarget
	model     string
	reasoning string
	status    string
	error     string
	cancelled bool
	cancel    context.CancelFunc
	done      chan struct{}
}

type commitTourRunner func(context.Context, string, commitTourTarget, string, string, string) error

func parseCommitTourCommand(message string) (hash, cwd string, ok bool, err error) {
	message = strings.TrimLeft(message, " \t\r\n")
	const command = "/tour"
	if message == command {
		return "", "", true, nil
	}
	if !strings.HasPrefix(message, command) || len(message) == len(command) {
		return "", "", false, nil
	}
	switch message[len(command)] {
	case ' ', '\t', '\r', '\n':
	default:
		return "", "", false, nil
	}

	remainder := strings.TrimSpace(message[len(command):])
	if remainder == "" {
		return "", "", true, nil
	}
	commandLine, cwd, _ := strings.Cut(remainder, "\n")
	fields := strings.Fields(commandLine)
	if len(fields) != 1 {
		return "", "", true, errors.New("usage: /tour <commit>")
	}
	cwd = strings.TrimSpace(cwd)
	if strings.Contains(cwd, "\n") {
		return "", "", true, errors.New("tour working directory must be one line")
	}
	return fields[0], cwd, true, nil
}

func validCommitTourHash(hash string) bool {
	if len(hash) < 4 || len(hash) > 64 {
		return false
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func canonicalCommitTourPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute), nil
}

func resolveCommitTourTarget(cwd, hash string) (commitTourTarget, error) {
	if hash != "" && !validCommitTourHash(hash) {
		return commitTourTarget{}, errors.New("invalid commit hash")
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return commitTourTarget{}, errors.New("invalid working directory")
	}
	gitRoot, err := getGitRoot(cwd)
	if err != nil {
		return commitTourTarget{}, errors.New("not a git repository")
	}
	gitRoot, err = canonicalCommitTourPath(gitRoot)
	if err != nil {
		return commitTourTarget{}, fmt.Errorf("resolve git worktree: %w", err)
	}

	ref := hash
	if ref == "" {
		ref = "HEAD"
	}
	fullHashCmd := exec.Command("git", "rev-parse", "--verify", ref+"^{commit}")
	fullHashCmd.Dir = gitRoot
	fullHashBytes, err := fullHashCmd.Output()
	if err != nil {
		if hash == "" {
			return commitTourTarget{}, errors.New("working directory has no commit at HEAD")
		}
		return commitTourTarget{}, errors.New("failed to read commit")
	}
	fullHash := strings.TrimSpace(string(fullHashBytes))

	commonDirCmd := exec.Command("git", "rev-parse", "--git-common-dir")
	commonDirCmd.Dir = gitRoot
	commonDirBytes, err := commonDirCmd.Output()
	if err != nil {
		return commitTourTarget{}, errors.New("failed to locate git repository")
	}
	commonDir := strings.TrimSpace(string(commonDirBytes))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitRoot, commonDir)
	}
	commonDir, err = canonicalCommitTourPath(commonDir)
	if err != nil {
		return commitTourTarget{}, fmt.Errorf("resolve git repository: %w", err)
	}
	return commitTourTarget{Repository: commonDir, Worktree: gitRoot, Hash: fullHash}, nil
}

func verifiedCommitTour(target commitTourTarget) (json.RawMessage, error) {
	note, err := committour.ReadNote(target.Worktree, target.Hash)
	if errors.Is(err, committour.ErrNoNote) {
		return nil, errNoVerifiedCommitTour
	}
	if err != nil {
		return nil, err
	}
	tour, err := committour.ParseTour(note)
	if err != nil {
		return nil, errNoVerifiedCommitTour
	}
	if _, err := committour.Verify(target.Worktree, target.Hash, tour); err != nil {
		return nil, errNoVerifiedCommitTour
	}
	resolved, err := json.Marshal(tour)
	if err != nil {
		return nil, errNoVerifiedCommitTour
	}
	return resolved, nil
}

func commitTourKey(target commitTourTarget) string {
	return target.Repository + "\x00" + target.Hash
}

func commitTourStatusForWorker(conversation generated.Conversation, request *db.CommitTourRequest) CommitTourStatus {
	status := CommitTourStatus{
		Status:               request.State,
		Hash:                 request.Commit,
		Repository:           request.Repository,
		WorkerConversationID: conversation.ConversationID,
		Error:                request.Error,
	}
	if conversation.Slug != nil {
		status.WorkerSlug = *conversation.Slug
	}
	if status.Status == "complete" {
		status.Status = commitTourStatusFailed
		if status.Error == "" {
			status.Error = "the generated tour is missing or invalid"
		}
	}
	return status
}

func commitTourRequestFromConversation(conversation generated.Conversation) (*db.CommitTourRequest, bool) {
	return db.ManagedCommitTourRequest(conversation)
}

func (s *Server) matchingCommitTourWorker(ctx context.Context, target commitTourTarget) (generated.Conversation, *db.CommitTourRequest, bool, error) {
	workers, err := s.db.ListCommitTourWorkers(ctx)
	if err != nil {
		return generated.Conversation{}, nil, false, err
	}
	for i := len(workers) - 1; i >= 0; i-- {
		request, ok := commitTourRequestFromConversation(workers[i])
		if ok && request.Repository == target.Repository && request.Commit == target.Hash {
			return workers[i], request, true, nil
		}
	}
	return generated.Conversation{}, nil, false, nil
}

func (s *Server) commitTourWorkerProgress(ctx context.Context, childID string) (started, finished bool, err error) {
	messages, err := s.db.ListMessages(ctx, childID)
	if err != nil {
		return false, false, err
	}
	for i := range messages {
		if messages[i].Type == string(db.MessageTypeUser) {
			started = true
		}
		if (messages[i].Type == string(db.MessageTypeAgent) || messages[i].Type == string(db.MessageTypeError)) && isAgentEndOfTurn(&messages[i]) {
			finished = true
		}
	}
	return started, finished, nil
}

func (s *Server) commitTourStatus(ctx context.Context, target commitTourTarget) (CommitTourStatus, error) {
	if _, err := verifiedCommitTour(target); err == nil {
		return CommitTourStatus{Status: commitTourStatusPresent, Hash: target.Hash, Repository: target.Repository}, nil
	} else if !errors.Is(err, errNoVerifiedCommitTour) {
		return CommitTourStatus{}, err
	}

	key := commitTourKey(target)
	s.commitTourMu.Lock()
	if job := s.commitTourJobs[key]; job != nil {
		status := CommitTourStatus{
			Status:               job.status,
			Hash:                 target.Hash,
			Repository:           target.Repository,
			WorkerConversationID: job.childID,
			WorkerSlug:           job.slug,
			Error:                job.error,
		}
		s.commitTourMu.Unlock()
		return status, nil
	}
	s.commitTourMu.Unlock()

	worker, request, ok, err := s.matchingCommitTourWorker(ctx, target)
	if err != nil {
		return CommitTourStatus{}, err
	}
	if !ok {
		return CommitTourStatus{Status: commitTourStatusAbsent, Hash: target.Hash, Repository: target.Repository}, nil
	}
	if request.State == commitTourStatusBuilding {
		_, finished, progressErr := s.commitTourWorkerProgress(ctx, worker.ConversationID)
		if progressErr != nil {
			return CommitTourStatus{}, progressErr
		}
		if finished {
			if _, err := s.settleInactiveCommitTourWorker(ctx, worker.ConversationID, target, commitTourStatusFailed, "subagent finished without attaching a tour"); err != nil {
				return CommitTourStatus{}, err
			}
			return s.commitTourStatus(ctx, target)
		}
		go s.recoverCommitTourWorkers(context.Background())
	}
	return commitTourStatusForWorker(worker, request), nil
}

func commitTourPrompt(target commitTourTarget, skill string) string {
	return fmt.Sprintf(`Build and attach a guided commit tour for commit %s in %s.

Use the commit-tour skill included below. Follow its workflow completely, including verification and attachment. Do not modify tracked files, amend or create commits, change branches, rebase, reset, or push. Store scratch files outside the repository. The task is complete only when the tour is attached to this exact commit. Return a concise final status.

<commit_tour_skill>
%s
</commit_tour_skill>`, target.Hash, target.Worktree, skill)
}

func commitTourError(err error) string {
	if err == nil {
		return "subagent finished without attaching a tour"
	}
	runes := []rune(strings.TrimSpace(err.Error()))
	if len(runes) > 500 {
		runes = runes[:500]
	}
	return string(runes)
}

func (s *Server) requestCommitTour(ctx context.Context, parentID string, target commitTourTarget, modelID, reasoning string) (CommitTourStatus, bool, error) {
	parent, err := s.db.GetConversationByID(ctx, parentID)
	if err != nil {
		return CommitTourStatus{}, false, err
	}
	if parent.IsDraft {
		return CommitTourStatus{}, false, errCommitTourDraft
	}
	if parent.ParentConversationID != nil {
		return CommitTourStatus{}, false, errCommitTourNested
	}
	if _, err := verifiedCommitTour(target); err == nil {
		status := CommitTourStatus{Status: commitTourStatusPresent, Hash: target.Hash, Repository: target.Repository}
		return status, false, nil
	} else if !errors.Is(err, errNoVerifiedCommitTour) {
		return CommitTourStatus{}, false, err
	}
	if modelID == "" {
		modelID = derefString(parent.Model)
	}
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}

	key := commitTourKey(target)
	s.commitTourMu.Lock()
	if job := s.commitTourJobs[key]; job != nil {
		if job.status == commitTourStatusBuilding {
			status := CommitTourStatus{
				Status:               job.status,
				Hash:                 target.Hash,
				Repository:           target.Repository,
				WorkerConversationID: job.childID,
				WorkerSlug:           job.slug,
				Error:                job.error,
			}
			s.commitTourMu.Unlock()
			return status, false, nil
		}
		conversation, settleErr := s.db.UpdateCommitTourWorker(ctx, job.childID, func(request *db.CommitTourRequest) {
			request.State = commitTourStatusFailed
			if request.Error == "" {
				request.Error = job.error
			}
		})
		if settleErr != nil && !errors.Is(settleErr, sql.ErrNoRows) {
			s.commitTourMu.Unlock()
			return CommitTourStatus{}, false, settleErr
		}
		delete(s.commitTourJobs, key)
		if conversation != nil {
			go s.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: conversation})
		}
	}
	worker, request, found, err := s.matchingCommitTourWorker(ctx, target)
	if err != nil {
		s.commitTourMu.Unlock()
		return CommitTourStatus{}, false, err
	}
	if found && request.State == commitTourStatusBuilding {
		_, finished, progressErr := s.commitTourWorkerProgress(ctx, worker.ConversationID)
		if progressErr != nil {
			s.commitTourMu.Unlock()
			return CommitTourStatus{}, false, progressErr
		}
		if !finished {
			status := commitTourStatusForWorker(worker, request)
			s.commitTourMu.Unlock()
			go s.recoverCommitTourWorkers(context.Background())
			return status, false, nil
		}
		s.updateCommitTourWorkerState(ctx, worker.ConversationID, commitTourStatusFailed, "subagent finished without attaching a tour")
	}

	opts := db.ConversationOptions{
		Kind:                 commitTourKind,
		ThinkingLevel:        reasoning,
		DisableNotifications: true,
		CommitTour: &db.CommitTourRequest{
			Repository:  target.Repository,
			Worktree:    target.Worktree,
			Commit:      target.Hash,
			State:       commitTourStatusBuilding,
			RequestedAt: time.Now().UTC(),
		},
	}
	child, err := s.db.CreateCommitTourWorker(ctx, parentID, target.Worktree, modelID, opts)
	if err != nil {
		s.commitTourMu.Unlock()
		return CommitTourStatus{}, false, err
	}
	workerCtx, cancel := context.WithTimeout(context.Background(), commitTourTimeout)
	job := &commitTourJob{
		key:       key,
		parentID:  parentID,
		childID:   child.ConversationID,
		target:    target,
		model:     modelID,
		reasoning: reasoning,
		status:    commitTourStatusBuilding,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	if child.Slug != nil {
		job.slug = *child.Slug
	}
	s.commitTourJobs[key] = job
	s.commitTourMu.Unlock()

	go s.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: child})
	go s.runCommitTourJob(workerCtx, job, false)
	return CommitTourStatus{
		Status:               commitTourStatusBuilding,
		Hash:                 target.Hash,
		Repository:           target.Repository,
		WorkerConversationID: child.ConversationID,
		WorkerSlug:           job.slug,
	}, true, nil
}

func (s *Server) runCommitTourJob(ctx context.Context, job *commitTourJob, resume bool) {
	defer job.cancel()
	defer close(job.done)

	var err error
	if resume && s.commitTourRun == nil {
		err = s.resumeCommitTourSubagent(ctx, job)
	} else {
		skill, skillErr := skills.FindByName("commit-tour", job.target.Worktree)
		if skillErr != nil {
			err = fmt.Errorf("load commit-tour skill: %w", skillErr)
		} else {
			prompt := commitTourPrompt(job.target, skill)
			if s.commitTourRun != nil {
				err = s.commitTourRun(ctx, job.childID, job.target, prompt, job.model, job.reasoning)
			} else {
				_, err = NewSubagentRunner(s).RunSubagent(ctx, job.childID, prompt, true, commitTourTimeout, job.model, job.reasoning)
			}
		}
	}
	if err != nil {
		s.stopCommitTourChild(job.childID)
	}
	select {
	case <-s.shutdownCh:
		s.commitTourMu.Lock()
		if s.commitTourJobs[job.key] == job {
			delete(s.commitTourJobs, job.key)
		}
		s.commitTourMu.Unlock()
		return
	default:
	}
	s.finishCommitTourJob(job, err)
}

func (s *Server) resumeCommitTourSubagent(ctx context.Context, job *commitTourJob) error {
	service, err := s.llmManager.GetService(job.model)
	if err != nil {
		return fmt.Errorf("load commit tour model: %w", err)
	}
	manager, err := s.getOrCreateSubagentConversationManager(ctx, job.childID)
	if err != nil {
		return fmt.Errorf("restore commit tour worker: %w", err)
	}
	runner := NewSubagentRunner(s)
	manager.registerSubagentWaiter()
	if err := manager.ResumeInterruptedTurn(ctx, service, job.model); err != nil {
		runner.endWait(manager, job.childID, true)
		return fmt.Errorf("resume commit tour worker: %w", err)
	}
	done, err := runner.waitForIdle(ctx, manager, time.Now().Add(commitTourTimeout), func() bool { return false })
	runner.endWait(manager, job.childID, true)
	if err != nil {
		return err
	}
	if !done {
		return errors.New("commit tour worker timed out")
	}
	return nil
}

func (s *Server) finishCommitTourJob(job *commitTourJob, runErr error) {
	state := "complete"
	errorText := ""
	if _, err := verifiedCommitTour(job.target); err != nil {
		state = commitTourStatusFailed
		errorText = commitTourError(runErr)
		if !errors.Is(err, errNoVerifiedCommitTour) && runErr == nil {
			errorText = commitTourError(err)
		}
	}
	conversation, err := s.db.UpdateCommitTourWorker(context.Background(), job.childID, func(request *db.CommitTourRequest) {
		request.State = state
		request.Error = errorText
	})
	if err != nil {
		s.commitTourMu.Lock()
		cancelled := job.cancelled
		if s.commitTourJobs[job.key] == job {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				delete(s.commitTourJobs, job.key)
			case cancelled:
				delete(s.commitTourJobs, job.key)
			default:
				job.status = commitTourStatusFailed
				job.error = commitTourError(err)
			}
		}
		s.commitTourMu.Unlock()
		if !errors.Is(err, sql.ErrNoRows) {
			s.logger.Error("Failed to settle commit tour worker", "child", job.childID, "error", err)
		}
		return
	}

	s.commitTourMu.Lock()
	if s.commitTourJobs[job.key] == job {
		delete(s.commitTourJobs, job.key)
	}
	s.commitTourMu.Unlock()
	go s.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: conversation})
}

func (s *Server) cancelCommitTourJobs(conversationIDs ...string) {
	cancelled := make(map[string]bool, len(conversationIDs))
	for _, id := range conversationIDs {
		cancelled[id] = true
	}
	s.commitTourMu.Lock()
	for _, job := range s.commitTourJobs {
		if cancelled[job.parentID] || cancelled[job.childID] {
			job.cancelled = true
			job.cancel()
		}
	}
	s.commitTourMu.Unlock()
}

func (s *Server) stopCommitTourChild(childID string) {
	s.mu.Lock()
	manager := s.activeConversations[childID]
	s.mu.Unlock()
	if manager == nil || !manager.IsAgentWorking() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.CancelConversation(ctx); err != nil {
		s.logger.Error("Failed to cancel commit tour child", "child", childID, "error", err)
	}
}

func (s *Server) updateCommitTourWorkerState(ctx context.Context, childID, state, errorText string) {
	conversation, err := s.db.UpdateCommitTourWorker(ctx, childID, func(request *db.CommitTourRequest) {
		request.State = state
		request.Error = errorText
	})
	if err != nil {
		s.logger.Error("Failed to update commit tour worker", "child", childID, "state", state, "error", err)
		return
	}
	go s.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: conversation})
}

func (s *Server) settleInactiveCommitTourWorker(ctx context.Context, childID string, target commitTourTarget, state, errorText string) (bool, error) {
	key := commitTourKey(target)
	s.commitTourMu.Lock()
	defer s.commitTourMu.Unlock()
	if s.commitTourJobs[key] != nil {
		return false, nil
	}
	current, err := s.db.GetConversationByID(ctx, childID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	request, ok := commitTourRequestFromConversation(*current)
	if !ok || request.State != commitTourStatusBuilding || request.Repository != target.Repository || request.Commit != target.Hash {
		return false, nil
	}
	resolved, resolveErr := resolveCommitTourTarget(request.Worktree, request.Commit)
	if resolveErr == nil && resolved.Repository == request.Repository && resolved.Hash == request.Commit {
		if _, err := verifiedCommitTour(resolved); err == nil {
			state = "complete"
			errorText = ""
		}
	}
	conversation, err := s.db.UpdateCommitTourWorker(ctx, childID, func(request *db.CommitTourRequest) {
		request.State = state
		request.Error = errorText
	})
	if err != nil {
		return false, err
	}
	go s.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: conversation})
	return true, nil
}

func (s *Server) commitTourShuttingDown() bool {
	select {
	case <-s.shutdownCh:
		return true
	default:
		return false
	}
}

func (s *Server) recoverCommitTourWorkers(ctx context.Context) {
	if s.commitTourShuttingDown() {
		return
	}
	s.commitTourRecoveryMu.Lock()
	if s.commitTourRecoveryRunning {
		s.commitTourRecoveryPending = true
		s.commitTourRecoveryMu.Unlock()
		return
	}
	s.commitTourRecoveryRunning = true
	s.commitTourRecoveryMu.Unlock()

	for {
		s.recoverCommitTourWorkersOnce(ctx)
		s.commitTourRecoveryMu.Lock()
		if s.commitTourShuttingDown() {
			s.commitTourRecoveryPending = false
			s.commitTourRecoveryRunning = false
			s.commitTourRecoveryMu.Unlock()
			return
		}
		if s.commitTourRecoveryPending {
			s.commitTourRecoveryPending = false
			s.commitTourRecoveryMu.Unlock()
			continue
		}
		s.commitTourRecoveryRunning = false
		s.commitTourRecoveryMu.Unlock()
		return
	}
}

func (s *Server) recoverCommitTourWorkersOnce(ctx context.Context) {
	workers, err := s.db.ListCommitTourWorkers(ctx)
	if err != nil {
		s.logger.Error("Failed to scan commit tour workers", "error", err)
		return
	}
	for _, worker := range workers {
		if s.commitTourShuttingDown() {
			return
		}
		request, ok := commitTourRequestFromConversation(worker)
		if !ok || request.State != commitTourStatusBuilding {
			continue
		}
		storedTarget := commitTourTarget{Repository: request.Repository, Worktree: request.Worktree, Hash: request.Commit}
		target, err := resolveCommitTourTarget(storedTarget.Worktree, storedTarget.Hash)
		if err != nil {
			if _, settleErr := s.settleInactiveCommitTourWorker(ctx, worker.ConversationID, storedTarget, commitTourStatusFailed, commitTourError(err)); settleErr != nil {
				s.logger.Error("Failed to settle invalid commit tour worker", "child", worker.ConversationID, "error", settleErr)
			}
			continue
		}
		if target.Repository != storedTarget.Repository || target.Hash != storedTarget.Hash {
			identityErr := errors.New("commit tour worktree no longer belongs to the requested repository")
			if _, settleErr := s.settleInactiveCommitTourWorker(ctx, worker.ConversationID, storedTarget, commitTourStatusFailed, identityErr.Error()); settleErr != nil {
				s.logger.Error("Failed to settle mismatched commit tour worker", "child", worker.ConversationID, "error", settleErr)
			}
			continue
		}
		if _, err := verifiedCommitTour(target); err == nil {
			s.updateCommitTourWorkerState(ctx, worker.ConversationID, "complete", "")
			continue
		}

		started, finished, err := s.commitTourWorkerProgress(ctx, worker.ConversationID)
		if err != nil {
			s.logger.Error("Failed to inspect commit tour worker", "child", worker.ConversationID, "error", err)
			continue
		}
		if finished {
			if _, err := s.settleInactiveCommitTourWorker(ctx, worker.ConversationID, target, commitTourStatusFailed, "subagent finished without attaching a tour"); err != nil {
				s.logger.Error("Failed to settle finished commit tour worker", "child", worker.ConversationID, "error", err)
			}
			continue
		}

		select {
		case s.commitTourRecoverySlots <- struct{}{}:
		default:
			continue
		}
		key := commitTourKey(target)
		s.commitTourMu.Lock()
		if s.commitTourShuttingDown() {
			s.commitTourMu.Unlock()
			<-s.commitTourRecoverySlots
			return
		}
		if s.commitTourJobs[key] != nil {
			s.commitTourMu.Unlock()
			<-s.commitTourRecoverySlots
			continue
		}
		current, err := s.db.GetConversationByID(ctx, worker.ConversationID)
		if err != nil {
			s.commitTourMu.Unlock()
			<-s.commitTourRecoverySlots
			continue
		}
		currentRequest, ok := commitTourRequestFromConversation(*current)
		if !ok || currentRequest.State != commitTourStatusBuilding || currentRequest.Repository != target.Repository || currentRequest.Commit != target.Hash {
			s.commitTourMu.Unlock()
			<-s.commitTourRecoverySlots
			continue
		}
		started, finished, err = s.commitTourWorkerProgress(ctx, worker.ConversationID)
		if err != nil || finished {
			s.commitTourMu.Unlock()
			<-s.commitTourRecoverySlots
			if err != nil {
				s.logger.Error("Failed to recheck commit tour worker", "child", worker.ConversationID, "error", err)
			} else if _, settleErr := s.settleInactiveCommitTourWorker(ctx, worker.ConversationID, target, commitTourStatusFailed, "subagent finished without attaching a tour"); settleErr != nil {
				s.logger.Error("Failed to settle finished commit tour worker", "child", worker.ConversationID, "error", settleErr)
			}
			continue
		}
		model := derefString(current.Model)
		if model == "" {
			model = s.effectiveDefaultModel(s.getModelList())
		}
		jobBaseCtx, cancel := context.WithCancel(context.Background())
		job := &commitTourJob{
			key:       key,
			parentID:  derefString(current.ParentConversationID),
			childID:   current.ConversationID,
			target:    target,
			model:     model,
			reasoning: db.ParseConversationOptions(current.ConversationOptions).ThinkingLevel,
			status:    commitTourStatusBuilding,
			cancel:    cancel,
			done:      make(chan struct{}),
		}
		if current.Slug != nil {
			job.slug = *current.Slug
		}
		s.commitTourJobs[key] = job
		s.commitTourMu.Unlock()
		go func() {
			defer func() {
				<-s.commitTourRecoverySlots
				if !s.commitTourShuttingDown() {
					go s.recoverCommitTourWorkers(context.Background())
				}
			}()
			jobCtx, timeoutCancel := context.WithTimeout(jobBaseCtx, commitTourTimeout)
			defer timeoutCancel()
			s.runCommitTourJob(jobCtx, job, started)
		}()
	}
}

func (s *Server) handleCommitTourStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	target, err := resolveCommitTourTarget(r.URL.Query().Get("cwd"), r.URL.Query().Get("hash"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	status, err := s.commitTourStatus(r.Context(), target)
	if err != nil {
		http.Error(w, "failed to read commit tour status", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleCommitTourCommand(ctx context.Context, w http.ResponseWriter, conversation generated.Conversation, modelID, reasoning, message string) bool {
	hash, requestedCwd, ok, err := parseCommitTourCommand(message)
	if !ok {
		return false
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	cwd := requestedCwd
	if cwd == "" {
		cwd = derefString(conversation.Cwd)
	}
	target, err := resolveCommitTourTarget(cwd, hash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	status, started, err := s.requestCommitTour(ctx, conversation.ConversationID, target, modelID, reasoning)
	if errors.Is(err, errCommitTourDraft) || errors.Is(err, errCommitTourNested) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	if err != nil {
		s.logger.Error("Failed to request commit tour", "conversationID", conversation.ConversationID, "hash", target.Hash, "error", err)
		http.Error(w, "failed to request commit tour", http.StatusInternalServerError)
		return true
	}
	if started {
		shown, err := s.lastMessageShowsCommit(ctx, conversation.ConversationID, target.Hash)
		if err != nil {
			s.logger.Error("Failed to inspect last message for commit tour", "conversationID", conversation.ConversationID, "hash", target.Hash, "error", err)
			http.Error(w, "failed to record commit tour request", http.StatusInternalServerError)
			return true
		}
		started = !shown
	}
	if started {
		marker, err := s.db.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conversation.ConversationID,
			Type:           db.MessageTypeGitInfo,
			UserData: map[string]any{
				"tour_request": true,
				"commit":       target.Hash,
				"worktree":     target.Worktree,
			},
			BumpTimestamp: true,
		})
		if err != nil {
			s.logger.Error("Failed to record commit tour request", "conversationID", conversation.ConversationID, "hash", target.Hash, "error", err)
			http.Error(w, "failed to record commit tour request", http.StatusInternalServerError)
			return true
		}
		go s.notifySubscribersNewMessage(context.WithoutCancel(ctx), conversation.ConversationID, marker)
	}
	w.Header().Set("Content-Type", "application/json")
	if started {
		w.WriteHeader(http.StatusAccepted)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	json.NewEncoder(w).Encode(map[string]any{"status": "accepted", "tour": status})
	return true
}

// lastMessageShowsCommit reports whether the conversation's most recent
// visible message is a gitinfo line for hash. That line already renders the
// tour's build state, so a separate tour marker would just repeat it.
func (s *Server) lastMessageShowsCommit(ctx context.Context, conversationID, hash string) (bool, error) {
	var tail []generated.Message
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		tail, err = q.ListMessagesTail(ctx, generated.ListMessagesTailParams{ConversationID: conversationID, Limit: 1})
		return err
	})
	if err != nil {
		return false, err
	}
	if len(tail) == 0 {
		return false, nil
	}
	last := tail[len(tail)-1]
	if last.Type != string(db.MessageTypeGitInfo) || last.UserData == nil {
		return false, nil
	}
	var data struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal([]byte(*last.UserData), &data); err != nil {
		return false, err
	}
	return data.Commit == hash, nil
}
