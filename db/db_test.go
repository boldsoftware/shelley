package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
)

// setupTestDB creates a test database with schema migrated
func setupTestDB(t *testing.T) *DB {
	t.Helper()

	db, cleanup := NewTestDB(t)
	t.Cleanup(cleanup)
	return db
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "memory database not supported",
			cfg:     Config{DSN: ":memory:"},
			wantErr: true,
		},
		{
			name:    "empty DSN",
			cfg:     Config{DSN: ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := New(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if db != nil {
				defer db.Close()
			}
		})
	}
}

func TestDB_Migrate(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := New(Config{DSN: tmpDir + "/test.db"})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run migrations first time
	if err := db.Migrate(ctx); err != nil {
		t.Errorf("Migrate() error = %v", err)
	}

	// Verify tables were created by trying to count conversations
	var count int64
	err = db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		count, err = q.CountConversations(ctx)
		return err
	})
	if err != nil {
		t.Errorf("Failed to query conversations after migration: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 conversations, got %d", count)
	}

	// Run migrations a second time to verify idempotency
	if err := db.Migrate(ctx); err != nil {
		t.Errorf("Second Migrate() error = %v", err)
	}

	// Verify we can still query after running migrations twice
	err = db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		count, err = q.CountConversations(ctx)
		return err
	})
	if err != nil {
		t.Errorf("Failed to query conversations after second migration: %v", err)
	}
}

func TestDB_WithTx(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test successful transaction
	err := db.WithTx(ctx, func(q *generated.Queries) error {
		_, err := q.CreateConversation(ctx, generated.CreateConversationParams{
			ConversationID: "test-conv-1",
			Slug:           stringPtr("test-slug"),
			UserInitiated:  true,
			Model:          nil,
		})
		return err
	})
	if err != nil {
		t.Errorf("WithTx() error = %v", err)
	}

	// Verify the conversation was created
	var conv generated.Conversation
	err = db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conv, err = q.GetConversation(ctx, "test-conv-1")
		return err
	})
	if err != nil {
		t.Errorf("Failed to get conversation after transaction: %v", err)
	}
	if conv.ConversationID != "test-conv-1" {
		t.Errorf("Expected conversation ID 'test-conv-1', got %s", conv.ConversationID)
	}
}

// stringPtr returns a pointer to the given string
func stringPtr(s string) *string {
	return &s
}

func TestDB_ForeignKeyConstraints(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to create a message with a non-existent conversation_id
	// This should fail due to foreign key constraint
	err := db.QueriesTx(ctx, func(q *generated.Queries) error {
		_, err := q.CreateMessage(ctx, generated.CreateMessageParams{
			MessageID:      "test-msg-1",
			ConversationID: "non-existent-conversation",
			Type:           "user",
		})
		return err
	})

	if err == nil {
		t.Error("Expected error when creating message with non-existent conversation_id")
		return
	}

	// Verify the error is related to foreign key constraint
	if !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Errorf("Expected foreign key constraint error, got: %v", err)
	}
}

func TestDB_Pool(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Test Pool method
	pool := db.Pool()
	if pool == nil {
		t.Error("Expected non-nil pool")
	}
}

func TestDB_WithTxRes(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test WithTxRes with a simple function that returns a string
	result, err := WithTxRes[string](db, ctx, func(queries *generated.Queries) (string, error) {
		return "test result", nil
	})
	if err != nil {
		t.Errorf("WithTxRes() error = %v", err)
	}

	if result != "test result" {
		t.Errorf("Expected 'test result', got %s", result)
	}

	// Test WithTxRes with error handling
	_, err = WithTxRes[string](db, ctx, func(queries *generated.Queries) (string, error) {
		return "", fmt.Errorf("test error")
	})

	if err == nil {
		t.Error("Expected error from WithTxRes, got none")
	}
}

func TestNewTestDB_IsolatedCopies(t *testing.T) {
	db1, cleanup1 := NewTestDB(t)
	defer cleanup1()

	db2, cleanup2 := NewTestDB(t)
	defer cleanup2()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := db1.CreateConversation(ctx, stringPtr("only-in-db1"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation in first test db: %v", err)
	}

	var count int64
	err = db2.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		count, qerr = q.CountConversations(ctx)
		return qerr
	})
	if err != nil {
		t.Fatalf("Failed to count conversations in second test db: %v", err)
	}

	if count != 0 {
		t.Fatalf("Expected second test db to start empty, got %d conversations", count)
	}
}

func TestLLMRequestPrefixDeduplication(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create a conversation first
	slug := "test-prefix-conv"
	conv, err := db.CreateConversation(ctx, &slug, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Create a long shared prefix (must be > 100 bytes for deduplication to kick in)
	sharedPrefix := strings.Repeat("A", 200) // 200 bytes of 'A's

	// First request - full body stored
	req1Body := sharedPrefix + "_suffix1"
	req1, err := db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: &conv.ConversationID,
		Model:          "test-model",
		Provider:       "test-provider",
		Url:            "http://example.com",
		RequestBody:    &req1Body,
	})
	if err != nil {
		t.Fatalf("Failed to insert first request: %v", err)
	}

	// First request should have full body, no prefix reference
	if req1.PrefixRequestID != nil {
		t.Errorf("First request should not have prefix reference, got %v", *req1.PrefixRequestID)
	}
	if req1.PrefixLength != nil && *req1.PrefixLength != 0 {
		t.Errorf("First request should have no prefix length, got %v", *req1.PrefixLength)
	}
	if req1.RequestBody == nil || *req1.RequestBody != req1Body {
		t.Errorf("First request body mismatch: expected %q, got %q", req1Body, safeDeref(req1.RequestBody))
	}

	// Second request - shares prefix with first
	req2Body := sharedPrefix + "_suffix2_longer"
	req2, err := db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: &conv.ConversationID,
		Model:          "test-model",
		Provider:       "test-provider",
		Url:            "http://example.com",
		RequestBody:    &req2Body,
	})
	if err != nil {
		t.Fatalf("Failed to insert second request: %v", err)
	}

	// Second request should have prefix reference
	if req2.PrefixRequestID == nil || *req2.PrefixRequestID != req1.ID {
		t.Errorf("Second request should reference first request, got prefix_request_id=%v", safeDeref64(req2.PrefixRequestID))
	}
	// Common prefix is sharedPrefix + "_suffix" = 200 + 7 = 207 bytes
	expectedPrefixLen := len(sharedPrefix) + len("_suffix")
	if req2.PrefixLength == nil || *req2.PrefixLength != int64(expectedPrefixLen) {
		t.Errorf("Second request prefix length should be %d, got %v", expectedPrefixLen, safeDeref64(req2.PrefixLength))
	}
	// Stored body should only be the suffix after the shared prefix ("1" vs "2_longer")
	expectedSuffix := "2_longer"
	if req2.RequestBody == nil || *req2.RequestBody != expectedSuffix {
		t.Errorf("Second request should only store suffix %q, got %q", expectedSuffix, safeDeref(req2.RequestBody))
	}

	// Third request - shares even longer prefix with second
	req3Body := sharedPrefix + "_suffix2_longer_and_more"
	req3, err := db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: &conv.ConversationID,
		Model:          "test-model",
		Provider:       "test-provider",
		Url:            "http://example.com",
		RequestBody:    &req3Body,
	})
	if err != nil {
		t.Fatalf("Failed to insert third request: %v", err)
	}

	// Third request should reference second request
	if req3.PrefixRequestID == nil || *req3.PrefixRequestID != req2.ID {
		t.Errorf("Third request should reference second request, got prefix_request_id=%v", safeDeref64(req3.PrefixRequestID))
	}
	// The prefix length should be the full length of req2Body (since req3Body starts with req2Body)
	if req3.PrefixLength == nil || *req3.PrefixLength != int64(len(sharedPrefix)+len("_suffix2_longer")) {
		t.Errorf("Third request prefix length should be %d, got %v", len(sharedPrefix)+len("_suffix2_longer"), safeDeref64(req3.PrefixLength))
	}

	// Test reconstruction of full bodies
	reconstructed1, err := db.GetFullLLMRequestBody(ctx, req1.ID)
	if err != nil {
		t.Fatalf("Failed to reconstruct first request: %v", err)
	}
	if reconstructed1 != req1Body {
		t.Errorf("Reconstructed first request mismatch: expected %q, got %q", req1Body, reconstructed1)
	}

	reconstructed2, err := db.GetFullLLMRequestBody(ctx, req2.ID)
	if err != nil {
		t.Fatalf("Failed to reconstruct second request: %v", err)
	}
	if reconstructed2 != req2Body {
		t.Errorf("Reconstructed second request mismatch: expected %q, got %q", req2Body, reconstructed2)
	}

	reconstructed3, err := db.GetFullLLMRequestBody(ctx, req3.ID)
	if err != nil {
		t.Fatalf("Failed to reconstruct third request: %v", err)
	}
	if reconstructed3 != req3Body {
		t.Errorf("Reconstructed third request mismatch: expected %q, got %q", req3Body, reconstructed3)
	}
}

func TestLLMRequestNoPrefixForShortOverlap(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	slug := "test-short-conv"
	conv, err := db.CreateConversation(ctx, &slug, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Short prefix (< 100 bytes) - should NOT deduplicate
	shortPrefix := strings.Repeat("B", 50)

	req1Body := shortPrefix + "_first"
	_, err = db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: &conv.ConversationID,
		Model:          "test-model",
		Provider:       "test-provider",
		Url:            "http://example.com",
		RequestBody:    &req1Body,
	})
	if err != nil {
		t.Fatalf("Failed to insert first request: %v", err)
	}

	req2Body := shortPrefix + "_second"
	req2, err := db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: &conv.ConversationID,
		Model:          "test-model",
		Provider:       "test-provider",
		Url:            "http://example.com",
		RequestBody:    &req2Body,
	})
	if err != nil {
		t.Fatalf("Failed to insert second request: %v", err)
	}

	// With short prefix, should NOT have prefix reference (full body stored)
	if req2.PrefixRequestID != nil {
		t.Errorf("Short overlap should not have prefix reference, got %v", *req2.PrefixRequestID)
	}
	if req2.RequestBody == nil || *req2.RequestBody != req2Body {
		t.Errorf("Short overlap should store full body %q, got %q", req2Body, safeDeref(req2.RequestBody))
	}
}

func TestLLMRequestNoConversationID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Request without conversation_id - should store full body
	reqBody := strings.Repeat("C", 300)
	req, err := db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: nil,
		Model:          "test-model",
		Provider:       "test-provider",
		Url:            "http://example.com",
		RequestBody:    &reqBody,
	})
	if err != nil {
		t.Fatalf("Failed to insert request: %v", err)
	}

	// Should not have prefix reference
	if req.PrefixRequestID != nil {
		t.Errorf("Request without conversation_id should not have prefix reference")
	}
	if req.RequestBody == nil || *req.RequestBody != reqBody {
		t.Errorf("Request should store full body")
	}
}

func safeDeref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func safeDeref64(i *int64) int64 {
	if i == nil {
		return -1
	}
	return *i
}

func TestLLMRequestRealisticConversation(t *testing.T) {
	// This test simulates realistic LLM API request patterns where each
	// subsequent request includes all previous messages plus new ones
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	slug := "test-realistic-conv"
	conv, err := db.CreateConversation(ctx, &slug, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Simulate Anthropic-style messages array growing over conversation
	// Each request adds to the previous messages
	baseRequest := `{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"You are a helpful assistant."}],"messages":[`

	message1 := `{"role":"user","content":[{"type":"text","text":"Hello, how are you?"}]}`
	req1Body := baseRequest + message1 + `],"max_tokens":8192}`

	req1, err := db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: &conv.ConversationID,
		Model:          "claude-sonnet-4-6",
		Provider:       "anthropic",
		Url:            "https://api.anthropic.com/v1/messages",
		RequestBody:    &req1Body,
	})
	if err != nil {
		t.Fatalf("Failed to insert first request: %v", err)
	}

	// First request stored in full
	if req1.PrefixRequestID != nil {
		t.Errorf("First request should not have prefix reference")
	}

	// Second request: user message + assistant response + new user message
	message2 := `{"role":"assistant","content":[{"type":"text","text":"I'm doing well, thank you for asking!"}]}`
	message3 := `{"role":"user","content":[{"type":"text","text":"Can you help me write some code?"}]}`
	req2Body := baseRequest + message1 + `,` + message2 + `,` + message3 + `],"max_tokens":8192}`

	req2, err := db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: &conv.ConversationID,
		Model:          "claude-sonnet-4-6",
		Provider:       "anthropic",
		Url:            "https://api.anthropic.com/v1/messages",
		RequestBody:    &req2Body,
	})
	if err != nil {
		t.Fatalf("Failed to insert second request: %v", err)
	}

	// Second request should have prefix deduplication
	if req2.PrefixRequestID == nil {
		t.Errorf("Second request should have prefix reference")
	} else if *req2.PrefixRequestID != req1.ID {
		t.Errorf("Second request should reference first request")
	}

	// Verify prefix length is reasonable (should be at least the base + message1 length)
	minExpectedPrefix := len(baseRequest) + len(message1)
	if req2.PrefixLength == nil || *req2.PrefixLength < int64(minExpectedPrefix) {
		t.Errorf("Second request prefix length should be at least %d, got %v", minExpectedPrefix, safeDeref64(req2.PrefixLength))
	}

	// Verify we saved significant space
	req2StoredLen := len(safeDeref(req2.RequestBody))
	req2FullLen := len(req2Body)
	if req2StoredLen >= req2FullLen {
		t.Errorf("Second request should store less than full body: stored %d, full %d", req2StoredLen, req2FullLen)
	}
	t.Logf("Space saved for request 2: %d bytes (%.1f%% reduction)",
		req2FullLen-req2StoredLen,
		100.0*float64(req2FullLen-req2StoredLen)/float64(req2FullLen))

	// Third request: even more messages
	message4 := `{"role":"assistant","content":[{"type":"text","text":"Of course! What kind of code would you like me to help you with?"}]}`
	message5 := `{"role":"user","content":[{"type":"text","text":"I need a function to calculate fibonacci numbers."}]}`
	req3Body := baseRequest + message1 + `,` + message2 + `,` + message3 + `,` + message4 + `,` + message5 + `],"max_tokens":8192}`

	req3, err := db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: &conv.ConversationID,
		Model:          "claude-sonnet-4-6",
		Provider:       "anthropic",
		Url:            "https://api.anthropic.com/v1/messages",
		RequestBody:    &req3Body,
	})
	if err != nil {
		t.Fatalf("Failed to insert third request: %v", err)
	}

	// Third request should reference second
	if req3.PrefixRequestID == nil || *req3.PrefixRequestID != req2.ID {
		t.Errorf("Third request should reference second request")
	}

	req3StoredLen := len(safeDeref(req3.RequestBody))
	req3FullLen := len(req3Body)
	t.Logf("Space saved for request 3: %d bytes (%.1f%% reduction)",
		req3FullLen-req3StoredLen,
		100.0*float64(req3FullLen-req3StoredLen)/float64(req3FullLen))

	// Verify reconstruction works for all requests
	reconstructed1, err := db.GetFullLLMRequestBody(ctx, req1.ID)
	if err != nil {
		t.Fatalf("Failed to reconstruct request 1: %v", err)
	}
	if reconstructed1 != req1Body {
		t.Errorf("Reconstructed request 1 mismatch")
	}

	reconstructed2, err := db.GetFullLLMRequestBody(ctx, req2.ID)
	if err != nil {
		t.Fatalf("Failed to reconstruct request 2: %v", err)
	}
	if reconstructed2 != req2Body {
		t.Errorf("Reconstructed request 2 mismatch")
	}

	reconstructed3, err := db.GetFullLLMRequestBody(ctx, req3.ID)
	if err != nil {
		t.Fatalf("Failed to reconstruct request 3: %v", err)
	}
	if reconstructed3 != req3Body {
		t.Errorf("Reconstructed request 3 mismatch")
	}

	// Calculate total storage savings
	totalOriginal := len(req1Body) + len(req2Body) + len(req3Body)
	totalStored := len(safeDeref(req1.RequestBody)) + len(safeDeref(req2.RequestBody)) + len(safeDeref(req3.RequestBody))
	t.Logf("Total space: original %d bytes, stored %d bytes, saved %d bytes (%.1f%% reduction)",
		totalOriginal, totalStored, totalOriginal-totalStored,
		100.0*float64(totalOriginal-totalStored)/float64(totalOriginal))
}

func TestLLMRequestOpenAIStyle(t *testing.T) {
	// Test with OpenAI-style request format
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	slug := "test-openai-conv"
	conv, err := db.CreateConversation(ctx, &slug, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// OpenAI-style request format
	baseRequest := `{"model":"gpt-4","messages":[`
	message1 := `{"role":"system","content":"You are a helpful assistant."},{"role":"user","content":"Hello!"}`
	req1Body := baseRequest + message1 + `],"stream":true}`

	req1, err := db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: &conv.ConversationID,
		Model:          "gpt-4",
		Provider:       "openai",
		Url:            "https://api.openai.com/v1/chat/completions",
		RequestBody:    &req1Body,
	})
	if err != nil {
		t.Fatalf("Failed to insert first request: %v", err)
	}

	// Second request with more messages
	message2 := `{"role":"assistant","content":"Hello! How can I help you today?"},{"role":"user","content":"What's the weather like?"}`
	req2Body := baseRequest + message1 + `,` + message2 + `],"stream":true}`

	req2, err := db.InsertLLMRequest(ctx, generated.InsertLLMRequestParams{
		ConversationID: &conv.ConversationID,
		Model:          "gpt-4",
		Provider:       "openai",
		Url:            "https://api.openai.com/v1/chat/completions",
		RequestBody:    &req2Body,
	})
	if err != nil {
		t.Fatalf("Failed to insert second request: %v", err)
	}

	// Should have prefix deduplication
	if req2.PrefixRequestID == nil || *req2.PrefixRequestID != req1.ID {
		t.Errorf("Second request should reference first request")
	}

	// Verify reconstruction
	reconstructed2, err := db.GetFullLLMRequestBody(ctx, req2.ID)
	if err != nil {
		t.Fatalf("Failed to reconstruct second request: %v", err)
	}
	if reconstructed2 != req2Body {
		t.Errorf("Reconstructed request mismatch:\nexpected: %s\ngot: %s", req2Body, reconstructed2)
	}

	// Calculate savings
	req2StoredLen := len(safeDeref(req2.RequestBody))
	req2FullLen := len(req2Body)
	t.Logf("OpenAI-style space saved: %d bytes (%.1f%% reduction)",
		req2FullLen-req2StoredLen,
		100.0*float64(req2FullLen-req2StoredLen)/float64(req2FullLen))
}
