package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func readStreamResponseWithTimeout(reader *bufio.Reader, timeout time.Duration) (*StreamResponse, error) {
	type result struct {
		resp *StreamResponse
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		var dataLines []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				ch <- result{nil, err}
				return
			}
			line = strings.TrimSpace(line)
			if line == "" && len(dataLines) > 0 {
				break
			}
			if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
			}
		}

		data := strings.Join(dataLines, "\n")
		if data == "" {
			ch <- result{nil, nil}
			return
		}

		var response StreamResponse
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			ch <- result{nil, err}
			return
		}
		ch <- result{&response, nil}
	}()

	select {
	case r := <-ch:
		return r.resp, r.err
	case <-time.After(timeout):
		return nil, context.DeadlineExceeded
	}
}

func streamResponseContainsToolResultText(resp *StreamResponse, want string) bool {
	for _, msg := range resp.Messages {
		if msg.LlmData == nil {
			continue
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}
		for _, content := range llmMsg.Content {
			if content.Type != llm.ContentTypeToolResult {
				continue
			}
			for _, result := range content.ToolResult {
				if result.Type == llm.ContentTypeText && strings.Contains(result.Text, want) {
					return true
				}
			}
		}
	}
	return false
}

func openFIFOForWriteWithTimeout(path string, timeout time.Duration) error {
	type result struct {
		err error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer f.Close()
		_, err = f.WriteString("go\n")
		ch <- result{err: err}
	}()

	select {
	case r := <-ch:
		return r.err
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

func TestToolProgressStreamedBeforeToolResult(t *testing.T) {
	srv, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	streamResp, err := http.Get(httpServer.URL + "/api/conversation/" + conversation.ConversationID + "/stream")
	if err != nil {
		t.Fatalf("failed to connect to stream: %v", err)
	}
	defer streamResp.Body.Close()

	reader := bufio.NewReader(streamResp.Body)
	initialEvent, err := readStreamResponseWithTimeout(reader, 2*time.Second)
	if err != nil {
		t.Fatalf("failed to read initial SSE event: %v", err)
	}
	if initialEvent == nil {
		t.Fatal("expected initial SSE event")
	}

	fifoPath := fmt.Sprintf("%s/tool-progress.fifo", t.TempDir())
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("failed to create FIFO: %v", err)
	}

	chatReq := ChatRequest{
		Message: fmt.Sprintf("bash: printf 'alpha\\n'; cat %q >/dev/null; printf 'omega\\n'", fifoPath),
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)
	resp, err := http.Post(
		httpServer.URL+"/api/conversation/"+conversation.ConversationID+"/chat",
		"application/json",
		strings.NewReader(string(chatBody)),
	)
	if err != nil {
		t.Fatalf("failed to post chat request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	sawAlphaProgress := false
	progressDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(progressDeadline) {
		event, err := readStreamResponseWithTimeout(reader, time.Until(progressDeadline))
		if err == context.DeadlineExceeded {
			break
		}
		if err != nil {
			t.Fatalf("failed reading SSE event: %v", err)
		}
		if event == nil {
			continue
		}
		if streamResponseContainsToolResultText(event, "alpha") || streamResponseContainsToolResultText(event, "omega") {
			t.Fatal("received tool result before tool progress was streamed")
		}
		if event.ToolProgress != nil && event.ToolProgress.ToolName == "bash" && strings.Contains(event.ToolProgress.Output, "alpha") {
			sawAlphaProgress = true
			break
		}
	}
	if !sawAlphaProgress {
		t.Fatal("did not receive bash tool progress containing alpha before completion")
	}

	if err := openFIFOForWriteWithTimeout(fifoPath, 2*time.Second); err != nil {
		t.Fatalf("failed to unblock FIFO: %v", err)
	}

	sawOmegaResult := false
	resultDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(resultDeadline) {
		event, err := readStreamResponseWithTimeout(reader, time.Until(resultDeadline))
		if err == context.DeadlineExceeded {
			break
		}
		if err != nil {
			t.Fatalf("failed reading SSE event after unblocking FIFO: %v", err)
		}
		if event == nil {
			continue
		}
		if streamResponseContainsToolResultText(event, "omega") {
			sawOmegaResult = true
			break
		}
	}
	if !sawOmegaResult {
		t.Fatal("did not receive final tool result containing omega after unblocking FIFO")
	}
}
