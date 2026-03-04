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

	// Get commits
	cmd := exec.Command("git", "log", "--oneline", "-20", "--pretty=format:%H%x00%s%x00%an%x00%at")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\x00")
			if len(parts) < 4 {
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
				ID:         parts[0],
				Message:    parts[1],
				Author:     parts[2],
				Timestamp:  time.Unix(timestamp, 0),
				FilesCount: filesCount,
				Additions:  additions,
				Deletions:  deletions,
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

	var cmd *exec.Cmd
	var statBaseArg string

	if diffID == "working" {
		cmd = exec.Command("git", "diff", "--name-status", "HEAD")
		statBaseArg = "HEAD"
	} else {
		// Diff from the selected commit's parent to the working tree,
		// showing all changes from that point through the current state.
		parent := parentRef(gitRoot, diffID)
		cmd = exec.Command("git", "diff", "--name-status", parent)
		statBaseArg = parent
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

		// Get additions/deletions for this file
		// For both working and commit diffs, we compare statBaseArg to working tree
		statCmd := exec.Command("git", "diff", statBaseArg, "--numstat", "--", parts[1])
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

	// Right side: always the current working tree state.
	// For commit diffs this shows all changes from that commit through now,
	// making edits safe since they operate on the actual current file.
	var newContent string
	fullPath := filepath.Join(gitRoot, cleanPath)
	if file, err := os.Open(fullPath); err == nil {
		defer file.Close()
		if fileData, err := io.ReadAll(file); err == nil {
			newContent = string(fileData)
		}
	}

	fileDiff := GitFileDiff{
		Path:       filePath,
		OldContent: oldContent,
		NewContent: newContent,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fileDiff)
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
