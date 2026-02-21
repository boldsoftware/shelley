package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/util/singleflight"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
	"shelley.exe.dev/server/notifications"
	"shelley.exe.dev/ui"
)

// APIMessage is the message format sent to clients
// TODO: We could maybe omit llm_data when display_data is available
type APIMessage struct {
	MessageID      string    `json:"message_id"`
	ConversationID string    `json:"conversation_id"`
	SequenceID     int64     `json:"sequence_id"`
	Type           string    `json:"type"`
	LlmData        *string   `json:"llm_data,omitempty"`
	UserData       *string   `json:"user_data,omitempty"`
	UsageData      *string   `json:"usage_data,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	DisplayData    *string   `json:"display_data,omitempty"`
	EndOfTurn      *bool     `json:"end_of_turn,omitempty"`
}

// ConversationState represents the current state of a conversation.
// This is broadcast to all subscribers whenever the state changes.
type ConversationState struct {
	ConversationID string `json:"conversation_id"`
	Working        bool   `json:"working"`
	Model          string `json:"model,omitempty"`
}

// ConversationWithState combines a conversation with its working state.
type ConversationWithState struct {
	generated.Conversation
	Working bool `json:"working"`
}

// StreamResponse represents the response format for conversation streaming
type StreamResponse struct {
	Messages          []APIMessage           `json:"messages"`
	Conversation      generated.Conversation `json:"conversation"`
	ConversationState *ConversationState     `json:"conversation_state,omitempty"`
	ContextWindowSize uint64                 `json:"context_window_size,omitempty"`
	// ConversationListUpdate is set when another conversation in the list changed
	ConversationListUpdate *ConversationListUpdate `json:"conversation_list_update,omitempty"`
	// Heartbeat indicates this is a heartbeat message (no new data, just keeping connection alive)
	Heartbeat bool `json:"heartbeat,omitempty"`
	// NotificationEvent is set when a notification-worthy event occurs (e.g. agent finished).
	NotificationEvent *notifications.Event `json:"notification_event,omitempty"`
}

// LLMProvider is an interface for getting LLM services
type LLMProvider interface {
	GetService(modelID string) (llm.Service, error)
	GetAvailableModels() []string
	HasModel(modelID string) bool
	GetModelInfo(modelID string) *models.ModelInfo
	RefreshCustomModels() error
}

// NewLLMServiceManager creates a new LLM service manager from config
func NewLLMServiceManager(cfg *LLMConfig) LLMProvider {
	// Convert LLMConfig to models.Config
	modelConfig := &models.Config{
		AnthropicAPIKey: cfg.AnthropicAPIKey,
		OpenAIAPIKey:    cfg.OpenAIAPIKey,
		GeminiAPIKey:    cfg.GeminiAPIKey,
		FireworksAPIKey: cfg.FireworksAPIKey,
		Gateway:         cfg.Gateway,
		Logger:          cfg.Logger,
		DB:              cfg.DB,
	}

	manager, err := models.NewManager(modelConfig)
	if err != nil {
		// This shouldn't happen in practice, but handle it gracefully
		cfg.Logger.Error("Failed to create models manager", "error", err)
	}

	return manager
}

// toAPIMessages converts database messages to API messages.
// When display_data is present (tool results), llm_data is omitted to save bandwidth
// since the display_data contains all information needed for UI rendering.
func toAPIMessages(messages []generated.Message) []APIMessage {
	apiMessages := make([]APIMessage, len(messages))
	for i, msg := range messages {
		var endOfTurnPtr *bool
		if msg.LlmData != nil && msg.Type == string(db.MessageTypeAgent) {
			if endOfTurn, ok := extractEndOfTurn(*msg.LlmData); ok {
				endOfTurnCopy := endOfTurn
				endOfTurnPtr = &endOfTurnCopy
			}
		}

		// TODO: Consider omitting llm_data when display_data is present to save bandwidth.
		// The display_data contains all info needed for UI rendering of tool results,
		// but the UI currently still uses llm_data for some checks.

		apiMsg := APIMessage{
			MessageID:      msg.MessageID,
			ConversationID: msg.ConversationID,
			SequenceID:     msg.SequenceID,
			Type:           msg.Type,
			LlmData:        msg.LlmData,
			UserData:       msg.UserData,
			UsageData:      msg.UsageData,
			CreatedAt:      msg.CreatedAt,
			DisplayData:    msg.DisplayData,
			EndOfTurn:      endOfTurnPtr,
		}
		apiMessages[i] = apiMsg
	}
	return apiMessages
}

func extractEndOfTurn(raw string) (bool, bool) {
	var message llm.Message
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		return false, false
	}
	return message.EndOfTurn, true
}

// calculateContextWindowSize returns the context window usage from the most recent message with non-zero usage.
// Each API call's input tokens represent the full conversation history sent to the model,
// so we only need the last message's tokens (not accumulated across all messages).
// The total input includes regular input tokens plus cached tokens (both read and created).
// Messages without usage data (user messages, tool messages, etc.) are skipped.
func calculateContextWindowSize(messages []APIMessage) uint64 {
	// Find the last message with non-zero usage data
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.UsageData == nil {
			continue
		}
		var usage llm.Usage
		if err := json.Unmarshal([]byte(*msg.UsageData), &usage); err != nil {
			continue
		}
		ctxUsed := usage.ContextWindowUsed()
		if ctxUsed == 0 {
			continue
		}
		// Return total context window used: all input tokens + output tokens
		// This represents the full context that would be sent for the next turn
		return ctxUsed
	}
	return 0
}

// isAgentEndOfTurn checks if a message is an agent or error message with end_of_turn=true.
// This indicates the agent loop has finished processing.
func isAgentEndOfTurn(msg *generated.Message) bool {
	if msg == nil {
		return false
	}
	// Agent and error messages can have end_of_turn
	if msg.Type != string(db.MessageTypeAgent) && msg.Type != string(db.MessageTypeError) {
		return false
	}
	if msg.LlmData == nil {
		return false
	}
	endOfTurn, ok := extractEndOfTurn(*msg.LlmData)
	if !ok {
		return false
	}
	return endOfTurn
}

// calculateContextWindowSizeFromMsg calculates context window usage from a single message.
// Returns 0 if the message has no usage data (e.g., user messages), in which case
// the client should keep its previous context window value.
func calculateContextWindowSizeFromMsg(msg *generated.Message) uint64 {
	if msg == nil || msg.UsageData == nil {
		return 0
	}
	var usage llm.Usage
	if err := json.Unmarshal([]byte(*msg.UsageData), &usage); err != nil {
		return 0
	}
	return usage.ContextWindowUsed()
}

// ConversationListUpdate represents an update to the conversation list
type ConversationListUpdate struct {
	Type           string                  `json:"type"` // "update", "delete"
	Conversation   *generated.Conversation `json:"conversation,omitempty"`
	ConversationID string                  `json:"conversation_id,omitempty"` // For deletes
}

// Server manages the HTTP API and active conversations
type Server struct {
	listenAddr          string
	db                  *db.DB
	llmManager          LLMProvider
	toolSetConfig       claudetool.ToolSetConfig
	activeConversations map[string]*ConversationManager
	mu                  sync.Mutex
	logger              *slog.Logger
	predictableOnly     bool
	terminalURL         string
	defaultModel        string
	links               []Link
	requireHeader       string
	conversationGroup   singleflight.Group[string, *ConversationManager]
	versionChecker      *VersionChecker
	notifDispatcher     *notifications.Dispatcher
	shutdownCh          chan struct{} // Signals background routines to stop
}

// NewServer creates a new server instance
func NewServer(database *db.DB, llmManager LLMProvider, toolSetConfig claudetool.ToolSetConfig, logger *slog.Logger, predictableOnly bool, terminalURL, defaultModel, requireHeader string, links []Link, useClaudeCodeSubagent bool) *Server {
	s := &Server{
		db:                  database,
		llmManager:          llmManager,
		toolSetConfig:       toolSetConfig,
		activeConversations: make(map[string]*ConversationManager),
		logger:              logger,
		predictableOnly:     predictableOnly,
		terminalURL:         terminalURL,
		defaultModel:        defaultModel,
		requireHeader:       requireHeader,
		links:               links,
		versionChecker:      NewVersionChecker(),
		notifDispatcher:     notifications.NewDispatcher(logger),
		shutdownCh:          make(chan struct{}),
	}

	// Set up subagent support
	if useClaudeCodeSubagent {
		s.toolSetConfig.SubagentRunner = NewClaudeCodeRunner(s)
	} else {
		s.toolSetConfig.SubagentRunner = NewSubagentRunner(s)
	}
	s.toolSetConfig.SubagentDB = &db.SubagentDBAdapter{DB: database}
	s.toolSetConfig.MaxSubagentDepth = 1 // Only top-level conversations can spawn subagents

	return s
}

// RegisterNotificationChannel adds a backend notification channel to the dispatcher.
func (s *Server) RegisterNotificationChannel(ch notifications.Channel) {
	s.notifDispatcher.Register(ch)
	s.logger.Info("registered notification channel", "channel", ch.Name())
}

// RegisterRoutes registers HTTP routes on the given mux
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// API routes - wrap with gzip where beneficial
	mux.Handle("/api/conversations", gzipHandler(http.HandlerFunc(s.handleConversations)))
	mux.Handle("/api/conversations/archived", gzipHandler(http.HandlerFunc(s.handleArchivedConversations)))
	mux.Handle("/api/conversations/new", http.HandlerFunc(s.handleNewConversation))           // Small response
	mux.Handle("/api/conversations/continue", http.HandlerFunc(s.handleContinueConversation)) // Small response
	mux.Handle("/api/conversations/distill", http.HandlerFunc(s.handleDistillConversation))   // Small response
	mux.Handle("/api/conversation/", http.StripPrefix("/api/conversation", s.conversationMux()))
	mux.Handle("/api/conversation/{id}/mcp", http.HandlerFunc(s.handleMCP))
	mux.Handle("/api/conversation-by-slug/", gzipHandler(http.HandlerFunc(s.handleConversationBySlug)))
	mux.Handle("/api/validate-cwd", http.HandlerFunc(s.handleValidateCwd)) // Small response
	mux.Handle("/api/list-directory", gzipHandler(http.HandlerFunc(s.handleListDirectory)))
	mux.Handle("/api/create-directory", http.HandlerFunc(s.handleCreateDirectory))
	mux.Handle("/api/git/diffs", gzipHandler(http.HandlerFunc(s.handleGitDiffs)))
	mux.Handle("/api/git/diffs/", gzipHandler(http.HandlerFunc(s.handleGitDiffFiles)))
	mux.Handle("/api/git/file-diff/", gzipHandler(http.HandlerFunc(s.handleGitFileDiff)))
	mux.HandleFunc("/api/upload", s.handleUpload)                      // Binary uploads
	mux.HandleFunc("/api/read", s.handleRead)                          // Serves images
	mux.Handle("/api/write-file", http.HandlerFunc(s.handleWriteFile)) // Small response
	mux.HandleFunc("/api/exec-ws", s.handleExecWS)                     // Websocket for shell commands

	// Custom models API
	mux.Handle("/api/custom-models", http.HandlerFunc(s.handleCustomModels))
	mux.Handle("/api/custom-models/", http.HandlerFunc(s.handleCustomModel))
	mux.Handle("/api/custom-models-test", http.HandlerFunc(s.handleTestModel))

	// Notification channels API
	mux.Handle("/api/notification-channels", http.HandlerFunc(s.handleNotificationChannels))
	mux.Handle("/api/notification-channels/", http.HandlerFunc(s.handleNotificationChannel))
	mux.Handle("/api/notification-channel-types", http.HandlerFunc(s.handleNotificationChannelTypes))

	// Models API (dynamic list refresh)
	mux.Handle("/api/models", http.HandlerFunc(s.handleModels))

	// Version endpoints
	mux.Handle("GET /version", http.HandlerFunc(s.handleVersion))
	mux.Handle("GET /version-check", http.HandlerFunc(s.handleVersionCheck))
	mux.Handle("GET /version-changelog", http.HandlerFunc(s.handleVersionChangelog))
	mux.Handle("POST /upgrade", http.HandlerFunc(s.handleUpgrade))
	mux.Handle("POST /exit", http.HandlerFunc(s.handleExit))
	mux.Handle("GET /settings", http.HandlerFunc(s.handleGetSettings))
	mux.Handle("POST /settings", http.HandlerFunc(s.handleSetSetting))

	// Debug endpoints
	mux.Handle("GET /debug/conversations", http.HandlerFunc(s.handleDebugConversationsPage))
	mux.Handle("GET /debug/llm_requests", http.HandlerFunc(s.handleDebugLLMRequests))
	mux.Handle("GET /debug/llm_requests/api", http.HandlerFunc(s.handleDebugLLMRequestsAPI))
	mux.Handle("GET /debug/llm_requests/{id}/request", http.HandlerFunc(s.handleDebugLLMRequestBody))
	mux.Handle("GET /debug/llm_requests/{id}/request_full", http.HandlerFunc(s.handleDebugLLMRequestBodyFull))
	mux.Handle("GET /debug/llm_requests/{id}/response", http.HandlerFunc(s.handleDebugLLMResponseBody))

	// Serve embedded UI assets
	mux.Handle("/", s.staticHandler(ui.Assets()))
}

// handleValidateCwd validates that a path exists and is a directory
func (s *Server) handleValidateCwd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"valid": false,
			"error": "path is required",
		})
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if os.IsNotExist(err) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"valid": false,
				"error": "directory does not exist",
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"valid": false,
				"error": err.Error(),
			})
		}
		return
	}

	if !info.IsDir() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"valid": false,
			"error": "path is not a directory",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"valid": true,
	})
}

// DirectoryEntry represents a single directory entry for the directory picker
type DirectoryEntry struct {
	Name           string `json:"name"`
	IsDir          bool   `json:"is_dir"`
	GitHeadSubject string `json:"git_head_subject,omitempty"`
}

// ListDirectoryResponse is the response from the list-directory endpoint
type ListDirectoryResponse struct {
	Path            string           `json:"path"`
	Parent          string           `json:"parent"`
	Entries         []DirectoryEntry `json:"entries"`
	GitHeadSubject  string           `json:"git_head_subject,omitempty"`
	GitWorktreeRoot string           `json:"git_worktree_root,omitempty"`
}

// handleListDirectory lists the contents of a directory for the directory picker
func (s *Server) handleListDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		// Default to home directory or root
		homeDir, err := os.UserHomeDir()
		if err != nil {
			path = "/"
		} else {
			path = homeDir
		}
	}

	// Clean and resolve the path
	path = filepath.Clean(path)

	// Verify path exists and is a directory
	info, err := os.Stat(path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if os.IsNotExist(err) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "directory does not exist",
			})
		} else if os.IsPermission(err) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "permission denied",
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": err.Error(),
			})
		}
		return
	}

	if !info.IsDir() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "path is not a directory",
		})
		return
	}

	// Read directory contents
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if os.IsPermission(err) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "permission denied",
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": err.Error(),
			})
		}
		return
	}

	// Build response with only directories (for directory picker)
	var entries []DirectoryEntry
	for _, entry := range dirEntries {
		// Only include directories
		if entry.IsDir() {
			dirEntry := DirectoryEntry{
				Name:  entry.Name(),
				IsDir: true,
			}

			// Check if this is a git repo root and get HEAD commit subject
			entryPath := filepath.Join(path, entry.Name())
			if isGitRepo(entryPath) {
				if subject := getGitHeadSubject(entryPath); subject != "" {
					dirEntry.GitHeadSubject = subject
				}
			}

			entries = append(entries, dirEntry)
		}
	}

	// Sort entries: non-hidden first, then hidden (.*), alphabetically within each group
	sort.Slice(entries, func(i, j int) bool {
		iHidden := strings.HasPrefix(entries[i].Name, ".")
		jHidden := strings.HasPrefix(entries[j].Name, ".")
		if iHidden != jHidden {
			return !iHidden // non-hidden comes first
		}
		return entries[i].Name < entries[j].Name
	})

	// Calculate parent directory
	parent := filepath.Dir(path)
	if parent == path {
		// At root, no parent
		parent = ""
	}

	response := ListDirectoryResponse{
		Path:    path,
		Parent:  parent,
		Entries: entries,
	}

	// Check if the current directory itself is a git repo
	if isGitRepo(path) {
		response.GitHeadSubject = getGitHeadSubject(path)
		if root := getGitWorktreeRoot(path); root != "" {
			response.GitWorktreeRoot = root
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// getGitHeadSubject returns the subject line of HEAD commit for a git repository.
// Returns empty string if unable to get the subject.
// isGitRepo checks if the given path is a git repository root.
// Returns true for both regular repos (.git directory) and worktrees (.git file with gitdir:).
func isGitRepo(dirPath string) bool {
	gitPath := filepath.Join(dirPath, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	if fi.IsDir() {
		return true // regular .git directory
	}
	if fi.Mode().IsRegular() {
		// Check if it's a worktree .git file
		content, err := os.ReadFile(gitPath)
		if err == nil && strings.HasPrefix(string(content), "gitdir:") {
			return true
		}
	}
	return false
}

// getGitHeadSubject returns the subject line of HEAD commit for a git repository.
// Returns empty string if unable to get the subject.
func getGitHeadSubject(repoPath string) string {
	cmd := exec.Command("git", "log", "-1", "--format=%s")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// getGitWorktreeRoot returns the main repository root if the given path is
// a git worktree (not the main repo itself). Returns "" otherwise.
func getGitWorktreeRoot(repoPath string) string {
	// Get the worktree's git dir and the common (main repo) git dir
	cmd := exec.Command("git", "rev-parse", "--git-dir", "--git-common-dir")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)
	if len(lines) != 2 {
		return ""
	}
	gitDir := lines[0]
	commonDir := lines[1]

	// Resolve relative paths
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoPath, gitDir)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repoPath, commonDir)
	}
	gitDir = filepath.Clean(gitDir)
	commonDir = filepath.Clean(commonDir)

	// If they're the same, this is the main repo, not a worktree
	if gitDir == commonDir {
		return ""
	}

	// The main repo root is the parent of the common .git dir
	return filepath.Dir(commonDir)
}

// handleCreateDirectory creates a new directory
func (s *Server) handleCreateDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "invalid request body",
		})
		return
	}

	if req.Path == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "path is required",
		})
		return
	}

	// Clean the path
	path := filepath.Clean(req.Path)

	// Check if path already exists
	if _, err := os.Stat(path); err == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "path already exists",
		})
		return
	}

	// Verify parent directory exists
	parentDir := filepath.Dir(path)
	if _, err := os.Stat(parentDir); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "parent directory does not exist",
		})
		return
	}

	// Create the directory (only the final directory, not parents)
	if err := os.Mkdir(path, 0o755); err != nil {
		w.Header().Set("Content-Type", "application/json")
		if os.IsPermission(err) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "permission denied",
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": err.Error(),
			})
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path": path,
	})
}

// getOrCreateConversationManager gets an existing conversation manager or creates a new one.
func (s *Server) getOrCreateConversationManager(ctx context.Context, conversationID string) (*ConversationManager, error) {
	manager, err, _ := s.conversationGroup.Do(conversationID, func() (*ConversationManager, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if manager, exists := s.activeConversations[conversationID]; exists {
			manager.Touch()
			return manager, nil
		}

		recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return s.recordMessage(ctx, conversationID, message, usage)
		}

		onStateChange := func(state ConversationState) {
			s.publishConversationState(state)
		}

		manager := NewConversationManager(conversationID, s.db, s.logger, s.toolSetConfig, recordMessage, onStateChange)
		if err := manager.Hydrate(ctx); err != nil {
			return nil, err
		}

		s.activeConversations[conversationID] = manager
		return manager, nil
	})
	if err != nil {
		return nil, err
	}
	return manager, nil
}

// getOrCreateSubagentConversationManager is like getOrCreateConversationManager but
// uses a toolSetConfig with SubagentDepth incremented by 1, preventing subagents
// from spawning their own subagents (when MaxSubagentDepth is 1).
func (s *Server) getOrCreateSubagentConversationManager(ctx context.Context, conversationID string) (*ConversationManager, error) {
	manager, err, _ := s.conversationGroup.Do(conversationID, func() (*ConversationManager, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if manager, exists := s.activeConversations[conversationID]; exists {
			manager.Touch()
			return manager, nil
		}

		recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return s.recordMessage(ctx, conversationID, message, usage)
		}

		onStateChange := func(state ConversationState) {
			s.publishConversationState(state)
		}

		// Use a modified toolSetConfig with incremented depth for subagents
		subagentConfig := s.toolSetConfig
		subagentConfig.SubagentDepth = s.toolSetConfig.SubagentDepth + 1

		manager := NewConversationManager(conversationID, s.db, s.logger, subagentConfig, recordMessage, onStateChange)
		if err := manager.Hydrate(ctx); err != nil {
			return nil, err
		}

		s.activeConversations[conversationID] = manager
		return manager, nil
	})
	if err != nil {
		return nil, err
	}
	return manager, nil
}

// ExtractDisplayData extracts display data from message content for storage
func ExtractDisplayData(message llm.Message) interface{} {
	// Build a map of tool_use_id to tool_name for lookups
	toolNameMap := make(map[string]string)
	for _, content := range message.Content {
		if content.Type == llm.ContentTypeToolUse {
			toolNameMap[content.ID] = content.ToolName
		}
	}

	var displayData []any
	for _, content := range message.Content {
		if content.Type == llm.ContentTypeToolResult && content.Display != nil {
			// Include tool name if we can find it
			toolName := toolNameMap[content.ToolUseID]
			displayData = append(displayData, map[string]any{
				"tool_use_id": content.ToolUseID,
				"tool_name":   toolName,
				"display":     content.Display,
			})
		}
	}

	if len(displayData) > 0 {
		return displayData
	}
	return nil
}

// recordMessage records a new message to the database and also notifies subscribers
func (s *Server) recordMessage(ctx context.Context, conversationID string, message llm.Message, usage llm.Usage) error {
	// Log message based on role
	if message.Role == llm.MessageRoleUser {
		s.logger.Info("User message", "conversation_id", conversationID, "content_items", len(message.Content))
	} else if message.Role == llm.MessageRoleAssistant {
		s.logger.Info("Agent message", "conversation_id", conversationID, "content_items", len(message.Content), "end_of_turn", message.EndOfTurn)
	}

	// Convert LLM message to database format
	messageType, err := s.getMessageType(message)
	if err != nil {
		return fmt.Errorf("failed to determine message type: %w", err)
	}

	// Extract display data from content items
	displayDataToStore := ExtractDisplayData(message)

	// Create message
	createdMsg, err := s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID:      conversationID,
		Type:                messageType,
		LLMData:             message,
		UserData:            nil,
		UsageData:           usage,
		DisplayData:         displayDataToStore,
		ExcludedFromContext: message.ExcludedFromContext,
	})
	if err != nil {
		return fmt.Errorf("failed to create message: %w", err)
	}

	// Update conversation's last updated timestamp for correct ordering
	if err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpdateConversationTimestamp(ctx, conversationID)
	}); err != nil {
		s.logger.Warn("Failed to update conversation timestamp", "conversationID", conversationID, "error", err)
	}

	// Touch active manager activity time if present
	s.mu.Lock()
	mgr, ok := s.activeConversations[conversationID]
	if ok {
		mgr.Touch()
	}
	s.mu.Unlock()

	// Notify subscribers with only the new message - use WithoutCancel because
	// the HTTP request context may be cancelled after the handler returns, but
	// we still want the notification to complete so SSE clients see the message immediately
	go s.notifySubscribersNewMessage(context.WithoutCancel(ctx), conversationID, createdMsg)

	return nil
}

// getMessageType determines the message type from an LLM message
func (s *Server) getMessageType(message llm.Message) (db.MessageType, error) {
	// System-generated errors are stored as error type
	if message.ErrorType != llm.ErrorTypeNone {
		return db.MessageTypeError, nil
	}

	switch message.Role {
	case llm.MessageRoleUser:
		return db.MessageTypeUser, nil
	case llm.MessageRoleAssistant:
		return db.MessageTypeAgent, nil
	default:
		// For tool messages, check if it's a tool call or tool result
		for _, content := range message.Content {
			if content.Type == llm.ContentTypeToolUse {
				return db.MessageTypeTool, nil
			}
			if content.Type == llm.ContentTypeToolResult {
				return db.MessageTypeTool, nil
			}
		}
		return db.MessageTypeAgent, nil
	}
}

// convertToLLMMessage converts a database message to an LLM message
func convertToLLMMessage(msg generated.Message) (llm.Message, error) {
	var llmMsg llm.Message
	if msg.LlmData == nil {
		return llm.Message{}, fmt.Errorf("message has no LLM data")
	}
	if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
		return llm.Message{}, fmt.Errorf("failed to unmarshal LLM data: %w", err)
	}
	return llmMsg, nil
}

// notifySubscribers sends conversation metadata updates (e.g., slug changes) to subscribers.
// This is used when only the conversation data changes, not the messages.
// Uses Broadcast instead of Publish to avoid racing with message sequence IDs.
func (s *Server) notifySubscribers(ctx context.Context, conversationID string) {
	s.mu.Lock()
	manager, exists := s.activeConversations[conversationID]
	s.mu.Unlock()

	if !exists {
		return
	}

	// Get conversation data only (no messages needed for metadata-only updates)
	var conversation generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conversation, err = q.GetConversation(ctx, conversationID)
		return err
	})
	if err != nil {
		s.logger.Error("Failed to get conversation data for notification", "conversationID", conversationID, "error", err)
		return
	}

	// Broadcast conversation update with no new messages.
	// Using Broadcast instead of Publish ensures this metadata-only update
	// doesn't race with notifySubscribersNewMessage which uses Publish with sequence IDs.
	streamData := StreamResponse{
		Messages:     nil, // No new messages, just conversation update
		Conversation: conversation,
	}
	manager.subpub.Broadcast(streamData)

	// Also notify conversation list subscribers (e.g., slug change)
	s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: &conversation,
	})
}

// notifySubscribersNewMessage sends a single new message to all subscribers.
// This is more efficient than re-sending all messages on each update.
func (s *Server) notifySubscribersNewMessage(ctx context.Context, conversationID string, newMsg *generated.Message) {
	s.mu.Lock()
	manager, exists := s.activeConversations[conversationID]
	s.mu.Unlock()

	if !exists {
		return
	}

	// Get conversation data for the response
	var conversation generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conversation, err = q.GetConversation(ctx, conversationID)
		return err
	})
	if err != nil {
		s.logger.Error("Failed to get conversation data for notification", "conversationID", conversationID, "error", err)
		return
	}

	// Convert the single new message to API format
	apiMessages := toAPIMessages([]generated.Message{*newMsg})

	// Update agent working state based on message type
	if isAgentEndOfTurn(newMsg) {
		manager.SetAgentWorking(false)
	}

	// Publish only the new message
	streamData := StreamResponse{
		Messages:     apiMessages,
		Conversation: conversation,
		// ContextWindowSize: 0 for messages without usage data (user/tool messages).
		// With omitempty, 0 is omitted from JSON, so the UI keeps its cached value.
		// Only agent messages have usage data, so context window updates when they arrive.
		ContextWindowSize: calculateContextWindowSizeFromMsg(newMsg),
	}
	manager.subpub.Publish(newMsg.SequenceID, streamData)

	// Also notify conversation list subscribers about the update (updated_at changed)
	s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: &conversation,
	})
}

// publishConversationListUpdate broadcasts a conversation list update to ALL active
// conversation streams. This allows clients to receive updates about other conversations
// while they're subscribed to their current conversation's stream.
func (s *Server) publishConversationListUpdate(update ConversationListUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Broadcast to all active conversation managers
	for _, manager := range s.activeConversations {
		streamData := StreamResponse{
			ConversationListUpdate: &update,
		}
		manager.subpub.Broadcast(streamData)
	}
}

// publishConversationState broadcasts a conversation state update to ALL active
// conversation streams. This allows clients to see the working state of other conversations.
func (s *Server) publishConversationState(state ConversationState) {
	// When the agent finishes working, emit a notification event.
	var notifEvent *notifications.Event
	if !state.Working {
		payload := notifications.AgentDonePayload{
			Model: state.Model,
		}
		if conv, err := s.db.GetConversationByID(context.Background(), state.ConversationID); err == nil && conv.Slug != nil {
			payload.ConversationTitle = *conv.Slug
		}
		if msg, err := s.db.GetLatestMessage(context.Background(), state.ConversationID); err == nil && msg.Type == string(db.MessageTypeAgent) && msg.LlmData != nil {
			var llmMsg llm.Message
			if json.Unmarshal([]byte(*msg.LlmData), &llmMsg) == nil {
				var text string
				for _, c := range llmMsg.Content {
					if c.Type == llm.ContentTypeText && c.Text != "" {
						text = c.Text
					}
				}
				if len(text) > 255 {
					text = text[:255] + "..."
				}
				payload.FinalResponse = text
			}
		}
		event := notifications.Event{
			Type:           notifications.EventAgentDone,
			ConversationID: state.ConversationID,
			Timestamp:      time.Now(),
			Payload:        payload,
		}
		s.notifDispatcher.Dispatch(context.Background(), event)
		notifEvent = &event
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Broadcast to all active conversation managers
	for _, manager := range s.activeConversations {
		streamData := StreamResponse{
			ConversationState: &state,
			NotificationEvent: notifEvent,
		}
		manager.subpub.Broadcast(streamData)
	}
}

// getWorkingConversations returns a map of conversation IDs that are currently working.
func (s *Server) getWorkingConversations() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	working := make(map[string]bool)
	for id, manager := range s.activeConversations {
		if manager.IsAgentWorking() {
			working[id] = true
		}
	}
	return working
}

// IsAgentWorking returns whether the agent is currently working on the given conversation.
// Returns false if the conversation doesn't have an active manager.
func (s *Server) IsAgentWorking(conversationID string) bool {
	s.mu.Lock()
	manager, exists := s.activeConversations[conversationID]
	s.mu.Unlock()
	if !exists {
		return false
	}
	return manager.IsAgentWorking()
}

// Cleanup removes inactive conversation managers
func (s *Server) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for id, manager := range s.activeConversations {
		// Remove managers that have been inactive for more than 30 minutes
		manager.mu.Lock()
		lastActivity := manager.lastActivity
		manager.mu.Unlock()
		if now.Sub(lastActivity) > 30*time.Minute {
			manager.stopLoop()
			delete(s.activeConversations, id)
			s.logger.Debug("Cleaned up inactive conversation", "conversationID", id)
		}
	}
}

// Start starts the HTTP server and handles the complete lifecycle
func (s *Server) Start(port string) error {
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		s.logger.Error("Failed to create listener", "error", err, "port_info", getPortOwnerInfo(port))
		return err
	}
	return s.StartWithListeners(listener, "")
}

// StartWithListener starts the HTTP server using the provided listener.
// This is useful for systemd socket activation where the listener is created externally.
func (s *Server) StartWithListener(listener net.Listener) error {
	return s.StartWithListeners(listener, "")
}

// StartWithListeners starts the HTTP server on the given TCP listener and optionally
// also on a Unix socket. The TCP listener gets full middleware (CSRF, requireHeader, logger).
// The Unix socket listener gets only the logger middleware (no CSRF, no requireHeader)
// since it is local and trusted.
func (s *Server) StartWithListeners(tcpListener net.Listener, socketPath string) error {
	// Set up shared mux with routes
	mux := http.NewServeMux()
	s.listenAddr = tcpListener.Addr().String()
	s.RegisterRoutes(mux)

	// TCP handler: full middleware (applied in reverse order: last added = first executed)
	tcpHandler := LoggerMiddleware(s.logger)(mux)
	cop := http.NewCrossOriginProtection()
	tcpHandler = cop.Handler(tcpHandler)
	if s.requireHeader != "" {
		tcpHandler = RequireHeaderMiddleware(s.requireHeader)(tcpHandler)
	}

	tcpServer := &http.Server{
		Handler: tcpHandler,
	}

	// Start cleanup routine
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.Cleanup()
		}
	}()

	// Start auto-upgrade routine
	go s.autoUpgradeRoutine()

	// Get actual port from listener
	actualPort := tcpListener.Addr().(*net.TCPAddr).Port

	// Start TCP server in goroutine
	serverErrCh := make(chan error, 2)
	go func() {
		s.logger.Info("Server starting", "port", actualPort, "url", fmt.Sprintf("http://localhost:%d", actualPort))
		if err := tcpServer.Serve(tcpListener); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
	}()

	// Optionally start Unix socket server
	var socketServer *http.Server
	var actualSocketPath string
	if socketPath != "" {
		actualSocketPath = resolveSocketPath(socketPath, s.logger)

		// Ensure the directory exists
		if err := os.MkdirAll(filepath.Dir(actualSocketPath), 0o700); err != nil {
			s.logger.Error("Failed to create socket directory", "error", err)
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			tcpServer.Shutdown(shutdownCtx)
			return err
		}

		unixListener, err := net.Listen("unix", actualSocketPath)
		if err != nil {
			s.logger.Error("Failed to create Unix socket listener", "error", err, "path", actualSocketPath)
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			tcpServer.Shutdown(shutdownCtx)
			return err
		}

		// Make socket accessible to the current user only
		if err := os.Chmod(actualSocketPath, 0o600); err != nil {
			s.logger.Warn("Failed to chmod socket", "path", actualSocketPath, "error", err)
		}

		// Unix socket handler: relaxed middleware (only logger, no CSRF or requireHeader)
		socketHandler := LoggerMiddleware(s.logger)(mux)

		socketServer = &http.Server{
			Handler: socketHandler,
		}

		go func() {
			s.logger.Info("Unix socket server starting", "path", actualSocketPath)
			if err := socketServer.Serve(unixListener); err != nil && err != http.ErrServerClosed {
				serverErrCh <- err
			}
		}()
	}

	// Wait for shutdown signal or server error
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErrCh:
		s.logger.Error("Server failed", "error", err)
		close(s.shutdownCh)
		return err
	case <-quit:
		s.logger.Info("Shutting down server")
	}

	// Signal background routines to stop
	close(s.shutdownCh)

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := tcpServer.Shutdown(ctx); err != nil {
		s.logger.Error("TCP server forced to shutdown", "error", err)
	}

	if socketServer != nil {
		if err := socketServer.Shutdown(ctx); err != nil {
			s.logger.Error("Unix socket server forced to shutdown", "error", err)
		}
		os.Remove(actualSocketPath)
	}

	s.logger.Info("Server exited")
	return nil
}

// autoUpgradeRoutine checks for upgrades every 24 hours if auto-upgrade is enabled
func (s *Server) autoUpgradeRoutine() {
	// Wait a bit before starting to let the server fully initialize
	timer := time.NewTimer(1 * time.Minute)
	defer timer.Stop()

	select {
	case <-timer.C:
		// Continue to main loop
	case <-s.shutdownCh:
		return
	}

	// Do initial check after startup delay
	s.tryAutoUpgrade()

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.tryAutoUpgrade()
		case <-s.shutdownCh:
			return
		}
	}
}

// tryAutoUpgrade attempts to upgrade if auto-upgrade is enabled and server is idle
func (s *Server) tryAutoUpgrade() {
	ctx := context.Background()

	// Check if auto-upgrade is enabled
	autoUpgradeEnabled, err := s.db.GetSetting(ctx, "auto_upgrade")
	if err != nil || autoUpgradeEnabled != "true" {
		return
	}

	// Check for updates first
	versionInfo, err := s.versionChecker.Check(ctx, true)
	if err != nil {
		s.logger.Error("Auto-upgrade version check failed", "error", err)
		return
	}

	if !versionInfo.HasUpdate {
		s.logger.Debug("Auto-upgrade: no update available")
		return
	}

	s.logger.Info("Auto-upgrade: update available", "current", versionInfo.CurrentTag, "latest", versionInfo.LatestTag)

	// Try to find an idle spot for up to 1 hour (check every 10 minutes)
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	timeout := time.After(1 * time.Hour)

	// Check immediately first
	if s.isServerIdle() {
		s.performUpgradeAndRestart(ctx, versionInfo)
		return
	}

	s.logger.Info("Auto-upgrade: waiting for idle window (will retry for 1 hour)")

	for {
		select {
		case <-ticker.C:
			if s.isServerIdle() {
				s.performUpgradeAndRestart(ctx, versionInfo)
				return
			}
			s.logger.Debug("Auto-upgrade: server still busy, will retry")
		case <-timeout:
			s.logger.Info("Auto-upgrade: timed out waiting for idle window (1 hour)")
			return
		case <-s.shutdownCh:
			return
		}
	}
}

// isServerIdle checks if any conversations are actively running
func (s *Server) isServerIdle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, cm := range s.activeConversations {
		if cm.IsAgentWorking() {
			return false
		}
	}
	return true
}

// performUpgradeAndRestart performs the upgrade and restarts the server
func (s *Server) performUpgradeAndRestart(ctx context.Context, versionInfo *VersionInfo) {
	s.logger.Info("Auto-upgrade: starting upgrade", "current", versionInfo.CurrentTag, "latest", versionInfo.LatestTag)

	err := s.versionChecker.DoUpgrade(ctx)
	if err != nil {
		s.logger.Error("Auto-upgrade failed", "error", err)
		return
	}

	s.logger.Info("Auto-upgrade complete, restarting")

	// Exit to trigger restart (systemd will restart us)
	time.Sleep(100 * time.Millisecond)
	os.Exit(0)
}

// resolveSocketPath finds an available socket path. If the requested path has a
// stale socket file (no one listening), it removes the file and returns the path.
// If the socket is actively in use, it appends -2, -3, etc. to the base name.
func resolveSocketPath(requested string, logger *slog.Logger) string {
	path := requested
	for i := 0; i < 10; i++ {
		if i > 0 {
			// e.g. shelley.sock → shelley-2.sock
			ext := filepath.Ext(requested)
			base := strings.TrimSuffix(requested, ext)
			path = fmt.Sprintf("%s-%d%s", base, i+1, ext)
		}

		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			// Path is free
			return path
		}
		if err != nil {
			// Can't stat — try it anyway
			return path
		}

		// File exists — check if it's a live socket
		conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
		if err != nil {
			// Stale socket — remove and use this path
			os.Remove(path)
			if path != requested {
				logger.Info("Removed stale socket", "path", path)
			}
			return path
		}
		conn.Close()

		// Socket is live — another instance is using it, try next
		logger.Info("Socket in use, trying next", "path", path)
	}

	// All slots taken, return the last one and let Listen fail with a clear error
	return path
}

// getPortOwnerInfo tries to identify what process is using a port.
// Returns a human-readable string with the PID and process name, or an error message.
func getPortOwnerInfo(port string) string {
	// Use lsof to find the process using the port
	cmd := exec.Command("lsof", "-i", ":"+port, "-sTCP:LISTEN", "-n", "-P")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("(unable to determine: %v)", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return "(no process found)"
	}

	// Parse lsof output: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
	// Skip the header line
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			command := fields[0]
			pid := fields[1]
			return fmt.Sprintf("pid=%s process=%s", pid, command)
		}
	}

	return "(could not parse lsof output)"
}
