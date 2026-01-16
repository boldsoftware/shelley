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
}

// LLMProvider is an interface for getting LLM services
type LLMProvider interface {
	GetService(modelID string) (llm.Service, error)
	GetAvailableModels() []string
	HasModel(modelID string) bool
}

// NewLLMServiceManager creates a new LLM service manager from config
func NewLLMServiceManager(cfg *LLMConfig, history *models.LLMRequestHistory) LLMProvider {
	// Convert LLMConfig to models.Config
	modelConfig := &models.Config{
		AnthropicAPIKey: cfg.AnthropicAPIKey,
		OpenAIAPIKey:    cfg.OpenAIAPIKey,
		GeminiAPIKey:    cfg.GeminiAPIKey,
		FireworksAPIKey: cfg.FireworksAPIKey,
		Gateway:         cfg.Gateway,
		Logger:          cfg.Logger,
	}

	manager, err := models.NewManager(modelConfig, history)
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

// isAgentEndOfTurn checks if a message is an agent message with end_of_turn=true.
// This indicates the agent loop has finished processing.
func isAgentEndOfTurn(msg *generated.Message) bool {
	if msg == nil {
		return false
	}
	// Only agent messages can have end_of_turn
	if msg.Type != string(db.MessageTypeAgent) {
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
}

// NewServer creates a new server instance
func NewServer(database *db.DB, llmManager LLMProvider, toolSetConfig claudetool.ToolSetConfig, logger *slog.Logger, predictableOnly bool, terminalURL, defaultModel, requireHeader string, links []Link) *Server {
	return &Server{
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
	}
}

// RegisterRoutes registers HTTP routes on the given mux
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// API routes - wrap with gzip where beneficial
	mux.Handle("/api/conversations", gzipHandler(http.HandlerFunc(s.handleConversations)))
	mux.Handle("/api/conversations/archived", gzipHandler(http.HandlerFunc(s.handleArchivedConversations)))
	mux.Handle("/api/conversations/new", http.HandlerFunc(s.handleNewConversation)) // Small response
	mux.Handle("/api/conversation/", http.StripPrefix("/api/conversation", s.conversationMux()))
	mux.Handle("/api/conversation-by-slug/", gzipHandler(http.HandlerFunc(s.handleConversationBySlug)))
	mux.Handle("/api/validate-cwd", http.HandlerFunc(s.handleValidateCwd)) // Small response
	mux.Handle("/api/list-directory", gzipHandler(http.HandlerFunc(s.handleListDirectory)))
	mux.Handle("/api/git/diffs", gzipHandler(http.HandlerFunc(s.handleGitDiffs)))
	mux.Handle("/api/git/diffs/", gzipHandler(http.HandlerFunc(s.handleGitDiffFiles)))
	mux.Handle("/api/git/file-diff/", gzipHandler(http.HandlerFunc(s.handleGitFileDiff)))
	mux.HandleFunc("/api/upload", s.handleUpload)                      // Binary uploads
	mux.HandleFunc("/api/read", s.handleRead)                          // Serves images
	mux.Handle("/api/write-file", http.HandlerFunc(s.handleWriteFile)) // Small response

	// Version endpoint
	mux.Handle("/version", http.HandlerFunc(s.handleVersion)) // Small response

	// Debug routes
	mux.Handle("/debug/llm", gzipHandler(http.HandlerFunc(s.handleDebugLLM)))

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
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

// ListDirectoryResponse is the response from the list-directory endpoint
type ListDirectoryResponse struct {
	Path    string           `json:"path"`
	Parent  string           `json:"parent"`
	Entries []DirectoryEntry `json:"entries"`
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
		// Skip hidden files/directories (starting with .)
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		// Only include directories
		if entry.IsDir() {
			entries = append(entries, DirectoryEntry{
				Name:  entry.Name(),
				IsDir: true,
			})
		}
	}

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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
		ConversationID: conversationID,
		Type:           messageType,
		LLMData:        message,
		UserData:       nil,
		UsageData:      usage,
		DisplayData:    displayDataToStore,
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
	switch message.Role {
	case llm.MessageRoleUser:
		return db.MessageTypeUser, nil
	case llm.MessageRoleAssistant:
		// Check if this is an error message by looking at content
		for _, content := range message.Content {
			if content.Type == llm.ContentTypeText && strings.HasPrefix(content.Text, "LLM request failed:") {
				return db.MessageTypeError, nil
			}
		}
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
	s.mu.Lock()
	defer s.mu.Unlock()

	// Broadcast to all active conversation managers
	for _, manager := range s.activeConversations {
		streamData := StreamResponse{
			ConversationState: &state,
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
	return s.StartWithListener(listener)
}

// StartWithListener starts the HTTP server using the provided listener.
// This is useful for systemd socket activation where the listener is created externally.
func (s *Server) StartWithListener(listener net.Listener) error {
	// Set up HTTP server with routes and middleware
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	// Add middleware (applied in reverse order: last added = first executed)
	handler := LoggerMiddleware(s.logger)(mux)
	handler = CSRFMiddleware()(handler)
	if s.requireHeader != "" {
		handler = RequireHeaderMiddleware(s.requireHeader)(handler)
	}

	httpServer := &http.Server{
		Handler: handler,
	}

	// Start cleanup routine
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.Cleanup()
		}
	}()

	// Get actual port from listener
	actualPort := listener.Addr().(*net.TCPAddr).Port

	// Start server in goroutine
	serverErrCh := make(chan error, 1)
	go func() {
		s.logger.Info("Server starting", "port", actualPort, "url", fmt.Sprintf("http://localhost:%d", actualPort))
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
	}()

	// Wait for shutdown signal or server error
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErrCh:
		s.logger.Error("Server failed", "error", err)
		return err
	case <-quit:
		s.logger.Info("Shutting down server")
	}

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		s.logger.Error("Server forced to shutdown", "error", err)
		return err
	}

	s.logger.Info("Server exited")
	return nil
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
