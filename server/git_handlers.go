package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GitDiffInfo represents a commit or working changes
type GitDiffInfo struct {
	ID         string    `json:"id"`
	Message    string    `json:"message"`
	Author     string    `json:"author"`
	Timestamp  time.Time `json:"timestamp"`
	FilesCount int       `json:"filesCount"`
	Additions  int       `json:"additions"`
	Deletions  int       `json:"deletions"`
	// Refs is the list of decorating refs on this commit (branches, tags),
	// e.g. "main", "HEAD", "origin/main", "v1.2.3". Empty for working changes
	// and for commits with no refs pointing at them.
	Refs []string `json:"refs,omitempty"`
	// IsMergeBase indicates the commit is the merge-base with @{upstream}.
	IsMergeBase bool `json:"isMergeBase,omitempty"`
}

// GitFileInfo represents a file in a diff
type GitFileInfo struct {
	Path        string `json:"path"`
	Status      string `json:"status"` // added, modified, deleted
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
	IsGenerated bool   `json:"isGenerated"`
}

// GitFileDiff represents the content of a file diff
type GitFileDiff struct {
	Path       string `json:"path"`
	OldContent string `json:"oldContent"`
	NewContent string `json:"newContent"`
}

// emptyTreeHash is the well-known hash for git's empty tree object.
// Used to diff root commits that have no parent.
const emptyTreeHash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// safeRef rejects refs that could be misinterpreted as a git option flag.
// Returns true if the ref is safe to pass as a positional argument.
func safeRef(ref string) bool {
	if ref == "" {
		return false
	}
	return !strings.HasPrefix(ref, "-")
}

// parentRef returns the parent commit hash for a commit.
// For root commits (no parent), it returns the empty tree hash.
func parentRef(gitDir, commitHash string) string {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", commitHash+"^")
	cmd.Dir = gitDir
	out, err := cmd.Output()
	if err != nil {
		return emptyTreeHash
	}
	return strings.TrimSpace(string(out))
}

// getGitRoot returns the git repository root for the given directory
func getGitRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// parseDiffStat parses git diff --numstat output
// parseDecorations turns git's %D output into a list of decoration labels.
// Input examples:
//
//	"HEAD -> main, origin/main"
//	"tag: v1.2.3, refs/stash"
//	""  (no decorations)
//
// Output is the raw labels in display order, e.g. ["HEAD", "main",
// "origin/main"], with the "tag: " prefix stripped from tag entries and
// the "HEAD -> X" form expanded to two entries: "HEAD" and "X".
func parseDecorations(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "HEAD -> ") {
			out = append(out, "HEAD", strings.TrimPrefix(part, "HEAD -> "))
			continue
		}
		part = strings.TrimPrefix(part, "tag: ")
		out = append(out, part)
	}
	return out
}

func parseDiffStat(output string) (additions, deletions, filesCount int) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			if parts[0] != "-" {
				add, _ := strconv.Atoi(parts[0])
				additions += add
			}
			if parts[1] != "-" {
				del, _ := strconv.Atoi(parts[1])
				deletions += del
			}
			filesCount++
		}
	}
	return additions, deletions, filesCount
}

// handleGitDiffs returns available diffs (working changes + recent commits)
func (s *Server) handleGitDiffs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		http.Error(w, "cwd parameter required", http.StatusBadRequest)
		return
	}

	// Validate cwd is a directory
	fi, err := os.Stat(cwd)
	if err != nil || !fi.IsDir() {
		http.Error(w, "invalid cwd", http.StatusBadRequest)
		return
	}

	gitRoot, err := getGitRoot(cwd)
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}

	var diffs []GitDiffInfo

	// Working changes
	workingStatCmd := exec.Command("git", "diff", "HEAD", "--numstat")
	workingStatCmd.Dir = gitRoot
	workingStatOutput, _ := workingStatCmd.Output()
	workingAdditions, workingDeletions, workingFilesCount := parseDiffStat(string(workingStatOutput))

	diffs = append(diffs, GitDiffInfo{
		ID:         "working",
		Message:    "Working Changes",
		Author:     "",
		Timestamp:  time.Now(),
		FilesCount: workingFilesCount,
		Additions:  workingAdditions,
		Deletions:  workingDeletions,
	})

	// Compute the merge-base with the configured upstream, if any. Failures
	// are non-fatal: many local-only branches have no upstream.
	mergeBase := ""
	mbCmd := exec.Command("git", "merge-base", "HEAD", "@{upstream}")
	mbCmd.Dir = gitRoot
	if out, err := mbCmd.Output(); err == nil {
		mergeBase = strings.TrimSpace(string(out))
	}

	// Get commits. %D yields decorating refs (already trimmed) like
	// "HEAD -> main, origin/main, tag: v1.2".
	cmd := exec.Command("git", "log", "-20", "--pretty=format:%H%x00%s%x00%an%x00%at%x00%D")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\x00")
			if len(parts) < 5 {
				continue
			}

			timestamp, _ := strconv.ParseInt(parts[3], 10, 64)

			// Get diffstat
			parent := parentRef(gitRoot, parts[0])
			statCmd := exec.Command("git", "diff", parent, parts[0], "--numstat")
			statCmd.Dir = gitRoot
			statOutput, _ := statCmd.Output()
			additions, deletions, filesCount := parseDiffStat(string(statOutput))

			diffs = append(diffs, GitDiffInfo{
				ID:          parts[0],
				Message:     parts[1],
				Author:      parts[2],
				Timestamp:   time.Unix(timestamp, 0),
				FilesCount:  filesCount,
				Additions:   additions,
				Deletions:   deletions,
				Refs:        parseDecorations(parts[4]),
				IsMergeBase: mergeBase != "" && parts[0] == mergeBase,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"diffs":   diffs,
		"gitRoot": gitRoot,
	})
}

// handleGitDiffFiles returns the files changed in a specific diff
func (s *Server) handleGitDiffFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract diff ID from path: /api/git/diffs/{id}/files
	path := strings.TrimPrefix(r.URL.Path, "/api/git/diffs/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 || parts[1] != "files" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	diffID := parts[0]

	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		http.Error(w, "cwd parameter required", http.StatusBadRequest)
		return
	}

	gitRoot, err := getGitRoot(cwd)
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}

	// Optional `to` parameter scopes the right-hand side of the diff:
	//   "" or "working": through working tree (default)
	//   "self":          only the selected commit (parent..diffID)
	//   <hash>:          range from selected commit's parent through <hash>
	toRef := r.URL.Query().Get("to")

	var cmd *exec.Cmd
	var statBaseArg string
	var statHeadArg string // empty means "working tree"

	if diffID == "working" {
		cmd = exec.Command("git", "diff", "--name-status", "HEAD")
		statBaseArg = "HEAD"
	} else {
		parent := parentRef(gitRoot, diffID)
		statBaseArg = parent
		switch toRef {
		case "", "working":
			// Diff from parent to working tree (existing behavior).
			cmd = exec.Command("git", "diff", "--name-status", parent)
		case "self":
			statHeadArg = diffID
			cmd = exec.Command("git", "diff", "--name-status", parent, diffID)
		default:
			if !safeRef(toRef) {
				http.Error(w, "invalid to parameter", http.StatusBadRequest)
				return
			}
			statHeadArg = toRef
			cmd = exec.Command("git", "diff", "--name-status", parent, toRef)
		}
	}
	cmd.Dir = gitRoot

	output, err := cmd.Output()
	if err != nil {
		http.Error(w, "failed to get diff files", http.StatusInternalServerError)
		return
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var files []GitFileInfo

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		status := "modified"
		switch parts[0] {
		case "A":
			status = "added"
		case "D":
			status = "deleted"
		case "M":
			status = "modified"
		}

		// Get additions/deletions for this file.
		// statHeadArg empty means compare statBaseArg to working tree.
		statArgs := []string{"diff", statBaseArg}
		if statHeadArg != "" {
			statArgs = append(statArgs, statHeadArg)
		}
		statArgs = append(statArgs, "--numstat", "--", parts[1])
		statCmd := exec.Command("git", statArgs...)
		statCmd.Dir = gitRoot
		statOutput, _ := statCmd.Output()
		additions, deletions := 0, 0
		if statOutput != nil {
			statParts := strings.Fields(string(statOutput))
			if len(statParts) >= 2 {
				additions, _ = strconv.Atoi(statParts[0])
				deletions, _ = strconv.Atoi(statParts[1])
			}
		}

		// Check if file is autogenerated based on path.
		// For Go files, we could also check content, but that requires reading the file
		// which is more expensive. Path-based detection covers most cases.
		isGenerated := IsAutogeneratedPath(parts[1])

		// For Go files that aren't obviously autogenerated by path,
		// check the file content for autogeneration markers.
		if !isGenerated && strings.HasSuffix(parts[1], ".go") && status != "deleted" {
			fullPath := filepath.Join(gitRoot, parts[1])
			if content, err := os.ReadFile(fullPath); err == nil {
				isGenerated = isAutogeneratedGoContent(content)
			}
		}

		files = append(files, GitFileInfo{
			Path:        parts[1],
			Status:      status,
			Additions:   additions,
			Deletions:   deletions,
			IsGenerated: isGenerated,
		})
	}

	// Sort files: non-generated first (alphabetically), then generated (alphabetically)
	sort.Slice(files, func(i, j int) bool {
		// If one is generated and the other isn't, non-generated comes first
		if files[i].IsGenerated != files[j].IsGenerated {
			return !files[i].IsGenerated
		}
		// Otherwise, sort alphabetically by path
		return files[i].Path < files[j].Path
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

// handleGitFileDiff returns the old and new content for a file
func (s *Server) handleGitFileDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract diff ID and file path from: /api/git/file-diff/{id}/*filepath
	path := strings.TrimPrefix(r.URL.Path, "/api/git/file-diff/")
	slashIdx := strings.Index(path, "/")
	if slashIdx < 0 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	diffID := path[:slashIdx]
	filePath := path[slashIdx+1:]

	if diffID == "" || filePath == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		http.Error(w, "cwd parameter required", http.StatusBadRequest)
		return
	}

	gitRoot, err := getGitRoot(cwd)
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}

	// Prevent path traversal
	cleanPath := filepath.Clean(filePath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		http.Error(w, "invalid file path", http.StatusBadRequest)
		return
	}

	// Left side: state before the selected commit (or HEAD for working changes)
	var baseRef string
	if diffID == "working" {
		baseRef = "HEAD"
	} else {
		baseRef = parentRef(gitRoot, diffID)
	}

	var oldContent string
	if baseRef == emptyTreeHash {
		oldContent = ""
	} else {
		oldCmd := exec.Command("git", "show", baseRef+":"+filePath)
		oldCmd.Dir = gitRoot
		oldOutput, _ := oldCmd.Output()
		oldContent = string(oldOutput)
	}

	// Right side: working tree by default; if `to` is set, show that ref instead.
	//   ""/"working": working tree (allows in-place edits)
	//   "self":      the selected commit
	//   <hash>:      arbitrary commit
	toRef := r.URL.Query().Get("to")
	var headRef string
	if diffID != "working" {
		switch toRef {
		case "self":
			headRef = diffID
		case "", "working":
			headRef = ""
		default:
			if !safeRef(toRef) {
				http.Error(w, "invalid to parameter", http.StatusBadRequest)
				return
			}
			headRef = toRef
		}
	}

	var newContent string
	if headRef == "" {
		fullPath := filepath.Join(gitRoot, cleanPath)
		if file, err := os.Open(fullPath); err == nil {
			defer file.Close()
			if fileData, err := io.ReadAll(file); err == nil {
				newContent = string(fileData)
			}
		}
	} else {
		newCmd := exec.Command("git", "show", headRef+":"+filePath)
		newCmd.Dir = gitRoot
		newOutput, _ := newCmd.Output()
		newContent = string(newOutput)
	}

	fileDiff := GitFileDiff{
		Path:       filePath,
		OldContent: oldContent,
		NewContent: newContent,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fileDiff)
}

// CommitMessage represents a commit's full message for display in the diff viewer.
type CommitMessage struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Author  string `json:"author"`
	IsHead  bool   `json:"isHead"`
}

// handleGitCommitMessages returns the full commit messages for commits in a range.
// Query params: cwd, from (commit hash — the selected base commit, inclusive),
// and optional `to`:
//
//	""/"working": from `from` through HEAD (default)
//	"self":      only the `from` commit
//	<hash>:      from `from` through <hash>
func (s *Server) handleGitCommitMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		http.Error(w, "cwd parameter required", http.StatusBadRequest)
		return
	}

	from := r.URL.Query().Get("from")
	if from == "" {
		http.Error(w, "from parameter required", http.StatusBadRequest)
		return
	}
	if !safeRef(from) {
		http.Error(w, "invalid from parameter", http.StatusBadRequest)
		return
	}
	toRef := r.URL.Query().Get("to")

	gitRoot, err := getGitRoot(cwd)
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}

	// Get HEAD hash
	headCmd := exec.Command("git", "rev-parse", "HEAD")
	headCmd.Dir = gitRoot
	headOut, err := headCmd.Output()
	if err != nil {
		http.Error(w, "failed to get HEAD", http.StatusInternalServerError)
		return
	}
	headHash := strings.TrimSpace(string(headOut))

	// Determine the upper bound of the commit range.
	var upperRef string
	switch toRef {
	case "self":
		upperRef = "" // only `from` itself; skip the range query entirely
	case "", "working":
		upperRef = "HEAD"
	default:
		if !safeRef(toRef) {
			http.Error(w, "invalid to parameter", http.StatusBadRequest)
			return
		}
		upperRef = toRef
	}

	// Get commits from `from` (exclusive) to `upperRef` (inclusive).
	// Use %x00 as separator, %x01 to separate records.
	// Format: hash\0subject\0body\0author
	var output []byte
	if upperRef != "" {
		cmd := exec.Command("git", "log", "--format=%H%x00%s%x00%b%x00%an%x01", from+".."+upperRef)
		cmd.Dir = gitRoot
		out, err := cmd.Output()
		if err != nil {
			// from..HEAD fails if from IS HEAD or is the only commit; that's fine,
			// the from commit is fetched separately below.
			out = nil
		}
		output = out
	}

	var messages []CommitMessage

	// Parse the range output (does NOT include 'from' itself)
	if len(output) > 0 {
		records := strings.Split(strings.TrimSpace(string(output)), "\x01")
		for _, rec := range records {
			rec = strings.TrimSpace(rec)
			if rec == "" {
				continue
			}
			parts := strings.SplitN(rec, "\x00", 4)
			if len(parts) < 4 {
				continue
			}
			messages = append(messages, CommitMessage{
				Hash:    parts[0],
				Subject: parts[1],
				Body:    strings.TrimSpace(parts[2]),
				Author:  strings.TrimSpace(parts[3]),
				IsHead:  parts[0] == headHash,
			})
		}
	}

	// Also include the 'from' commit itself
	fromCmd := exec.Command("git", "log", "-1", "--format=%H%x00%s%x00%b%x00%an", from)
	fromCmd.Dir = gitRoot
	fromOut, err := fromCmd.Output()
	if err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(fromOut)), "\x00", 4)
		if len(parts) >= 4 {
			messages = append(messages, CommitMessage{
				Hash:    parts[0],
				Subject: parts[1],
				Body:    strings.TrimSpace(parts[2]),
				Author:  strings.TrimSpace(parts[3]),
				IsHead:  parts[0] == headHash,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

// handleGitAmendMessage amends the most recent commit's message.
func (s *Server) handleGitAmendMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Cwd     string `json:"cwd"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Cwd == "" || req.Message == "" {
		http.Error(w, "cwd and message are required", http.StatusBadRequest)
		return
	}

	gitRoot, err := getGitRoot(req.Cwd)
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("git", "commit", "--amend", "-m", req.Message)
	cmd.Dir = gitRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, "failed to amend: "+string(output), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleGitCreateWorktree creates a new git worktree.
// The worktree is created as a sibling of the repo directory with name repo-YYYY-MM-DD-N.
func (s *Server) handleGitCreateWorktree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Cwd string `json:"cwd"` // current working directory (must be in a git repo)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Cwd == "" {
		http.Error(w, "cwd is required", http.StatusBadRequest)
		return
	}

	// Find the repo root (main repo, not worktree)
	gitRoot, err := getGitRoot(req.Cwd)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "not a git repository"})
		return
	}

	// If this is a worktree, use the main repo root
	mainRoot := gitRoot
	if root := getGitWorktreeRoot(gitRoot); root != "" {
		mainRoot = root
	}

	// Worktrees are siblings of the repo dir: ../reponame-YYYY-MM-DD-N
	repoName := filepath.Base(mainRoot)
	parentDir := filepath.Dir(mainRoot)
	dateStr := time.Now().Format("2006-01-02")

	// Find next available suffix
	var worktreePath string
	for i := 1; i <= 100; i++ {
		var name string
		if i == 1 {
			name = repoName + "-" + dateStr
		} else {
			name = repoName + "-" + dateStr + "-" + strconv.Itoa(i)
		}
		candidate := filepath.Join(parentDir, name)
		_, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			worktreePath = candidate
			break
		}
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "failed to check path: " + err.Error()})
			return
		}
	}
	if worktreePath == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "too many worktrees for today"})
		return
	}

	// Fetch origin first (best-effort)
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = mainRoot
	fetchCmd.Run() // ignore errors

	// Determine the branch name from the worktree path
	branchName := filepath.Base(worktreePath)

	// Create the worktree with a new branch based on origin/main (or HEAD)
	base := "HEAD"
	checkCmd := exec.Command("git", "rev-parse", "--verify", "origin/main")
	checkCmd.Dir = mainRoot
	if err := checkCmd.Run(); err == nil {
		base = "origin/main"
	}

	cmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreePath, base)
	cmd.Dir = mainRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to create worktree: " + string(output)})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": worktreePath})
}

// GitGraphCommit is a single commit node in the graph view.
type GitGraphCommit struct {
	Hash      string   `json:"hash"`
	ShortHash string   `json:"shortHash"`
	Parents   []string `json:"parents"`
	Subject   string   `json:"subject"`
	Author    string   `json:"author"`
	Email     string   `json:"email"`
	Timestamp int64    `json:"timestamp"`
	Refs      []string `json:"refs"`
	IsHead    bool     `json:"isHead"`
}

// handleGitGraph returns the commit DAG for the graph viewer.
func (s *Server) handleGitGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		http.Error(w, "cwd parameter required", http.StatusBadRequest)
		return
	}
	fi, err := os.Stat(cwd)
	if err != nil || !fi.IsDir() {
		http.Error(w, "invalid cwd", http.StatusBadRequest)
		return
	}
	gitRoot, err := getGitRoot(cwd)
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}

	limit := 500
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200000 {
			limit = n
		}
	}

	// Get all branches/tags/HEAD as starting points so multiple tips are
	// visible in the graph. Include HEAD last so it's emphasized.
	logArgs := []string{
		"log",
		"--all",
		"--date-order",
		"--pretty=format:%H%x00%P%x00%s%x00%an%x00%ae%x00%at%x00%D",
		"-n", strconv.Itoa(limit),
	}
	cmd := exec.Command("git", logArgs...)
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		http.Error(w, "git log failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var commits []GitGraphCommit
	lines := strings.Split(strings.TrimRight(string(output), "\n"), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) < 7 {
			continue
		}
		hash := parts[0]
		var parents []string
		if parts[1] != "" {
			parents = strings.Fields(parts[1])
		}
		ts, _ := strconv.ParseInt(parts[5], 10, 64)

		var refs []string
		isHead := false
		if parts[6] != "" {
			for _, rref := range strings.Split(parts[6], ", ") {
				rref = strings.TrimSpace(rref)
				if rref == "" {
					continue
				}
				// HEAD -> main form
				if strings.HasPrefix(rref, "HEAD -> ") {
					isHead = true
					refs = append(refs, "HEAD", strings.TrimPrefix(rref, "HEAD -> "))
					continue
				}
				if rref == "HEAD" {
					isHead = true
				}
				refs = append(refs, rref)
			}
		}

		short := hash
		if len(short) > 7 {
			short = short[:7]
		}
		commits = append(commits, GitGraphCommit{
			Hash:      hash,
			ShortHash: short,
			Parents:   parents,
			Subject:   parts[2],
			Author:    parts[3],
			Email:     parts[4],
			Timestamp: ts,
			Refs:      refs,
			IsHead:    isHead,
		})
	}

	// Current branch for convenience.
	branchCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = gitRoot
	branchOut, _ := branchCmd.Output()
	currentBranch := strings.TrimSpace(string(branchOut))

	// Origin remote URL → GitHub base URL, if it's github.
	remoteCmd := exec.Command("git", "config", "--get", "remote.origin.url")
	remoteCmd.Dir = gitRoot
	remoteOut, _ := remoteCmd.Output()
	githubBase := githubBaseURL(strings.TrimSpace(string(remoteOut)))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"commits":       commits,
		"gitRoot":       gitRoot,
		"currentBranch": currentBranch,
		"githubBase":    githubBase,
	})
}

// githubBaseURL returns the https://github.com/owner/repo base URL for a
// remote URL, or "" if the remote isn't a github.com one. Supports
// https://, git://, and ssh (git@github.com:owner/repo.git) forms.
func githubBaseURL(remote string) string {
	if remote == "" {
		return ""
	}
	var path string
	switch {
	case strings.HasPrefix(remote, "git@github.com:"):
		path = strings.TrimPrefix(remote, "git@github.com:")
	case strings.HasPrefix(remote, "ssh://git@github.com/"):
		path = strings.TrimPrefix(remote, "ssh://git@github.com/")
	case strings.HasPrefix(remote, "https://github.com/"):
		path = strings.TrimPrefix(remote, "https://github.com/")
	case strings.HasPrefix(remote, "http://github.com/"):
		path = strings.TrimPrefix(remote, "http://github.com/")
	case strings.HasPrefix(remote, "git://github.com/"):
		path = strings.TrimPrefix(remote, "git://github.com/")
	default:
		return ""
	}
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")
	if path == "" || !strings.Contains(path, "/") {
		return ""
	}
	return "https://github.com/" + path
}

// GitCommitDetailFile is one file's diffstat line.
type GitCommitDetailFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
}

// GitCommitDetail is the full detail bundle for a single commit.
type GitCommitDetail struct {
	Hash     string                `json:"hash"`
	Subject  string                `json:"subject"`
	Body     string                `json:"body"`
	Files    []GitCommitDetailFile `json:"files"`
	InsTotal int                   `json:"insTotal"`
	DelTotal int                   `json:"delTotal"`
}

// handleGitCommitDetail returns commit body + numstat for a single commit.
// Query: cwd, hash.
func (s *Server) handleGitCommitDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cwd := r.URL.Query().Get("cwd")
	hash := r.URL.Query().Get("hash")
	if cwd == "" || hash == "" {
		http.Error(w, "cwd and hash are required", http.StatusBadRequest)
		return
	}
	// Validate hash shape: hex only, 4..64 chars. Prevents flag injection.
	if len(hash) < 4 || len(hash) > 64 {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			http.Error(w, "invalid hash", http.StatusBadRequest)
			return
		}
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		http.Error(w, "invalid cwd", http.StatusBadRequest)
		return
	}
	gitRoot, err := getGitRoot(cwd)
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}

	// Body (everything after the subject line).
	bodyCmd := exec.Command("git", "log", "-1", "--format=%B", hash)
	bodyCmd.Dir = gitRoot
	bodyOut, err := bodyCmd.Output()
	if err != nil {
		http.Error(w, "git log failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	raw := strings.TrimRight(string(bodyOut), "\n")
	subject := raw
	body := ""
	if i := strings.Index(raw, "\n"); i >= 0 {
		subject = raw[:i]
		body = strings.TrimLeft(raw[i+1:], "\n")
	}

	// Diffstat via --numstat: "add\tdel\tpath", or "-\t-\tpath" for binary.
	numCmd := exec.Command("git", "show", "--format=", "--numstat", hash)
	numCmd.Dir = gitRoot
	numOut, err := numCmd.Output()
	if err != nil {
		http.Error(w, "git show failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var files []GitCommitDetailFile
	var insTotal, delTotal int
	for _, line := range strings.Split(strings.TrimRight(string(numOut), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		f := GitCommitDetailFile{Path: parts[2]}
		if parts[0] == "-" || parts[1] == "-" {
			f.Binary = true
		} else {
			f.Additions, _ = strconv.Atoi(parts[0])
			f.Deletions, _ = strconv.Atoi(parts[1])
			insTotal += f.Additions
			delTotal += f.Deletions
		}
		files = append(files, f)
	}
	if files == nil {
		files = []GitCommitDetailFile{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(GitCommitDetail{
		Hash:     hash,
		Subject:  subject,
		Body:     body,
		Files:    files,
		InsTotal: insTotal,
		DelTotal: delTotal,
	})
}
