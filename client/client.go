// Package client implements the experimental Shelley CLI client.
// It communicates with a running Shelley server over a Unix socket or HTTP.
package client

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultSocketPath returns the default Unix socket path (~/.config/shelley/shelley.sock).
func DefaultSocketPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "shelley", "shelley.sock")
}

// defaultClientURL is the socket of the Shelley server running this command
// ($SHELLEY_SOCKET), else the default socket.
func defaultClientURL() string {
	return "unix://" + cmp.Or(os.Getenv("SHELLEY_SOCKET"), DefaultSocketPath())
}

func parseClientURL(rawURL string) (scheme, address string, err error) {
	if sockPath, ok := strings.CutPrefix(rawURL, "unix://"); ok {
		if sockPath == "" {
			return "", "", fmt.Errorf("unix:// URL must include a socket path")
		}
		return "unix", sockPath, nil
	}
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return strings.SplitN(rawURL, "://", 2)[0], rawURL, nil
	}
	return "", "", fmt.Errorf("unsupported URL scheme: %s (use unix://, http://, or https://)", rawURL)
}

type multiFlag []string

func (f *multiFlag) String() string { return strings.Join(*f, ", ") }

func (f *multiFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type clientConfig struct {
	serverURL string
	headers   map[string]string
}

func (cc *clientConfig) newHTTPClient() (*http.Client, string, error) {
	scheme, address, err := parseClientURL(cc.serverURL)
	if err != nil {
		return nil, "", err
	}

	switch scheme {
	case "unix":
		transport := &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", address)
			},
		}
		return &http.Client{Transport: transport}, "http://localhost", nil
	case "http", "https":
		return &http.Client{}, address, nil
	default:
		return nil, "", fmt.Errorf("unsupported scheme: %s", scheme)
	}
}

func (cc *clientConfig) newRequest(method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("X-Shelley-Request", "1")
	}
	for k, v := range cc.headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// doJSON sends body, if non-nil, as JSON and decodes a 2xx response into
// out, if non-nil. Other statuses become httpResponseError errors.
func (cc *clientConfig) doJSON(client *http.Client, method, url string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := cc.newRequest(method, url, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpResponseError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// streamConversation calls handle with each event of the conversation's
// stream until handle returns true or the stream ends.
func streamConversation(cc *clientConfig, client *http.Client, baseURL, conversationID string, handle func(streamResponseWire) bool) error {
	req, err := cc.newRequest(http.MethodGet, baseURL+"/api/conversation/"+conversationID+"/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpResponseError(resp)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var sr streamResponseWire
		if json.Unmarshal([]byte(data), &sr) != nil {
			continue
		}
		if handle(sr) {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stream: %w", err)
	}
	return nil
}

// fatalf prints an error line to stderr and exits with status 1.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// Run is the entry point for "shelley client [args...]".
func Run(args []string) {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	urlFlag := fs.String("url", defaultClientURL(), "Server URL (unix:///path, http://host:port, https://host:port)")
	var headerFlags multiFlag
	fs.Var(&headerFlags, "H", `Extra HTTP header ("Name: Value", can be repeated)`)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "EXPERIMENTAL: Shelley CLI client\n\n")
		fmt.Fprintf(fs.Output(), "Usage: shelley client [flags] <subcommand> [args...]\n\n")
		fmt.Fprintf(fs.Output(), "Flags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), "\nSubcommands:\n")
		fmt.Fprintf(fs.Output(), "  chat     Send a message (new or existing conversation)\n")
		fmt.Fprintf(fs.Output(), "  read     Read conversation messages\n")
		fmt.Fprintf(fs.Output(), "  list     List conversations\n")
		fmt.Fprintf(fs.Output(), "  search   Search conversations by content\n")
		fmt.Fprintf(fs.Output(), "  tag      Show/add/remove a conversation's tags\n")
		fmt.Fprintf(fs.Output(), "  tags     List tags already in use\n")
		fmt.Fprintf(fs.Output(), "  archive  Archive a conversation\n")
		fmt.Fprintf(fs.Output(), "  call-transcript  Print a voice call's transcript\n")
		fmt.Fprintf(fs.Output(), "  help     Print detailed help\n")
	}
	fs.Parse(args)

	headers := make(map[string]string)
	for _, h := range headerFlags {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) != 2 {
			fatalf("Error: invalid header %q (expected \"Name: Value\")", h)
		}
		headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}

	cc := &clientConfig{serverURL: *urlFlag, headers: headers}

	subArgs := fs.Args()
	if len(subArgs) == 0 {
		fs.Usage()
		os.Exit(1)
	}

	switch subArgs[0] {
	case "chat":
		cmdChat(cc, subArgs[1:])
	case "read":
		cmdRead(cc, subArgs[1:])
	case "list":
		cmdList(cc, subArgs[1:])
	case "search":
		cmdSearch(cc, subArgs[1:])
	case "tag":
		cmdTag(cc, subArgs[1:])
	case "tags":
		cmdTags(cc, subArgs[1:])
	case "archive":
		cmdArchive(cc, subArgs[1:])
	case "call-transcript":
		cmdCallTranscript(cc, subArgs[1:])
	case "help":
		cmdHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", subArgs[0])
		fs.Usage()
		os.Exit(1)
	}
}

func buildChatRequestBody(prompt, model, cwd string, disableNotifications bool, targetConversationID string) map[string]any {
	reqBody := map[string]any{"message": prompt}
	if model != "" {
		reqBody["model"] = model
	}
	if cwd != "" {
		reqBody["cwd"] = cwd
	}
	if disableNotifications {
		reqBody["conversation_options"] = map[string]any{"disable_notifications": true}
	}

	// Commands run by Shelley tools know which conversation launched them.
	// Carry that provenance when they chat into a different conversation; the
	// server validates that sender and target are a direct managed-child/parent
	// pair before recording any label.
	senderConversationID := os.Getenv("SHELLEY_CONVERSATION_ID")
	if targetConversationID != "" && senderConversationID != "" && senderConversationID != targetConversationID {
		reqBody["sender_conversation_id"] = senderConversationID
	}
	return reqBody
}

func cmdChat(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client chat", flag.ExitOnError)
	prompt := fs.String("p", "", "Message to send (required)")
	convID := fs.String("c", "", "Conversation ID to continue (creates new if omitted)")
	model := fs.String("model", "", "Model to use (server default if empty)")
	cwd := fs.String("cwd", "", "Working directory for the conversation")
	reasoning := fs.String("reasoning", "", "Reasoning level for a new conversation (off, minimal, low, medium, high, xhigh, max)")
	var toolOverrides toolOverridesFlag
	fs.Var(&toolOverrides, "tool", "Tool override for a new conversation (NAME=on|off, repeatable)")
	noTools := fs.Bool("no-tools", false, "Disable all tools for a new conversation (-tool NAME=on can re-enable one)")
	var tags multiFlag
	fs.Var(&tags, "tag", "Tag to add to the conversation (repeatable)")
	ephemeral := fs.Bool("ephemeral", false, "Wait for end of turn, then archive the conversation (for cron-style cleanup)")
	noNotify := fs.Bool("disable-notifications", false, "Disable end-of-turn notifications for this conversation (new conversations only)")
	fs.Parse(args)

	if *prompt == "" {
		fatalf("Error: -p PROMPT is required")
	}

	conversationOptions, err := buildConversationOptions(*reasoning, toolOverrides, *noTools, *noNotify)
	if err != nil {
		fatalf("Error: %v", err)
	}
	if err := validateChatTarget(*convID, conversationOptions); err != nil {
		fatalf("Error: %v", err)
	}

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fatalf("Error: %v", err)
	}

	// Default cwd to the caller's working directory for new conversations,
	// so the server doesn't fall back to its own cwd (which may be unrelated
	// and cause expensive filesystem walks).
	effectiveCwd := *cwd
	if effectiveCwd == "" && *convID == "" {
		if wd, err := os.Getwd(); err == nil {
			effectiveCwd = wd
		}
	}

	reqBody := buildChatRequestBody(*prompt, *model, effectiveCwd, *noNotify, *convID)
	if conversationOptions != nil {
		reqBody["conversation_options"] = conversationOptions
	}

	var apiURL string
	if *convID != "" {
		apiURL = baseURL + "/api/conversation/" + *convID + "/chat"
	} else {
		apiURL = baseURL + "/api/conversations/new"
	}
	var respBody map[string]any
	if err := cc.doJSON(client, http.MethodPost, apiURL, reqBody, &respBody); err != nil {
		fatalf("Error: %v", err)
	}

	cid := respBody["conversation_id"]
	if cid == nil {
		cid = *convID // when continuing, the chat endpoint doesn't echo the ID back
	}
	cidStr, _ := cid.(string)
	output := map[string]any{
		"conversation_id": cid,
	}
	if slug, ok := respBody["slug"]; ok {
		output["slug"] = slug
	}
	var postSendErr error
	if len(tags) > 0 {
		if cidStr == "" {
			postSendErr = fmt.Errorf("Error: -tag could not determine conversation ID")
		} else {
			next := normalizeTagList(tags)
			if *convID != "" {
				current, err := fetchConversationTags(cc, client, baseURL, cidStr)
				if err != nil {
					postSendErr = fmt.Errorf("Error adding tags: %w", err)
				} else {
					next = mergeTags(current, next)
				}
			}
			if postSendErr == nil {
				updated, err := setConversationTags(cc, client, baseURL, cidStr, next)
				if err != nil {
					postSendErr = fmt.Errorf("Error adding tags: %w", err)
				} else {
					output["tags"] = updated
				}
			}
		}
	}

	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fatalf("Error writing response: %v", err)
	}
	if postSendErr != nil {
		fatalf("%v", postSendErr)
	}

	if *ephemeral {
		if cidStr == "" {
			fatalf("Error: -ephemeral could not determine conversation ID")
		}
		waitForEndOfTurn(cc, client, baseURL, cidStr)
		archiveConversation(cc, client, baseURL, cidStr)
	}
}

// waitForEndOfTurn streams the conversation until the agent's turn ends.
// Stream events are discarded; only end-of-turn detection is performed.
func waitForEndOfTurn(cc *clientConfig, client *http.Client, baseURL, conversationID string) {
	err := streamConversation(cc, client, baseURL, conversationID, func(sr streamResponseWire) bool {
		for _, msg := range sr.Messages {
			if (msg.Type == "agent" || msg.Type == "error") && msg.EndOfTurn != nil && *msg.EndOfTurn {
				return true
			}
		}
		return false
	})
	if err != nil {
		fatalf("Error: %v", err)
	}
}

func archiveConversation(cc *clientConfig, client *http.Client, baseURL, conversationID string) {
	if err := cc.doJSON(client, http.MethodPost, baseURL+"/api/conversation/"+conversationID+"/archive", nil, nil); err != nil {
		fatalf("Error archiving: %v", err)
	}
}

// streamEvent is the simplified output format for read.
type streamEvent struct {
	SequenceID int64  `json:"sequence_id"`
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	EndOfTurn  bool   `json:"end_of_turn"`
}

func cmdRead(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client read", flag.ExitOnError)
	wait := fs.Bool("wait", false, "Wait for agent turn to finish (stream new messages)")
	full := fs.Bool("full", false, "Emit complete message records with parsed JSON payloads and conversation metadata")
	usage := fs.Bool("usage", false, "Emit one aggregate usage object (including descendant conversations)")
	fs.Parse(args)

	if fs.NArg() == 0 {
		fatalf("Usage: shelley client read [-wait] [-full | -usage] CONVERSATION_ID")
	}
	if *usage && (*wait || *full) {
		fatalf("Error: -usage cannot be combined with -wait or -full")
	}
	conversationID := fs.Arg(0)

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fatalf("Error: %v", err)
	}

	switch {
	case *usage:
		summary, err := collectConversationUsage(cc, client, baseURL, conversationID)
		if err != nil {
			fatalf("Error: %v", err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(summary); err != nil {
			fatalf("Error writing usage: %v", err)
		}
	case *wait:
		readStream(cc, client, baseURL, conversationID, *full)
	default:
		readSnapshot(cc, client, baseURL, conversationID, *full)
	}
}

func readSnapshot(cc *clientConfig, client *http.Client, baseURL, conversationID string, full bool) {
	sr, err := fetchConversationSnapshot(cc, client, baseURL, conversationID)
	if err != nil {
		fatalf("Error: %v", err)
	}

	if full {
		conversation, err := decodeRawJSON(sr.Conversation)
		if err != nil {
			fatalf("Error parsing conversation metadata: %v", err)
		}
		for _, msg := range sr.Messages {
			record, err := fullMessage(msg, conversation)
			if err != nil {
				fatalf("Error parsing response: %v", err)
			}
			if err := json.NewEncoder(os.Stdout).Encode(record); err != nil {
				fatalf("Error writing response: %v", err)
			}
		}
		return
	}

	for _, msg := range sr.Messages {
		json.NewEncoder(os.Stdout).Encode(simplifyMessage(msg))
	}
}

func readStream(cc *clientConfig, client *http.Client, baseURL, conversationID string, full bool) {
	var conversation any
	seenSeqIDs := make(map[int64]bool)
	err := streamConversation(cc, client, baseURL, conversationID, func(sr streamResponseWire) bool {
		if len(sr.Conversation) > 0 {
			var err error
			if conversation, err = decodeRawJSON(sr.Conversation); err != nil {
				fatalf("Error parsing conversation metadata: %v", err)
			}
		}
		for _, msg := range sr.Messages {
			if seenSeqIDs[msg.SequenceID] {
				continue
			}
			seenSeqIDs[msg.SequenceID] = true

			var record any
			var endOfTurn bool
			if full {
				r, err := fullMessage(msg, conversation)
				if err != nil {
					fatalf("Error parsing response: %v", err)
				}
				record, endOfTurn = r, r.EndOfTurn
			} else {
				e := simplifyMessage(msg)
				record, endOfTurn = e, e.EndOfTurn
			}
			if err := json.NewEncoder(os.Stdout).Encode(record); err != nil {
				fatalf("Error writing response: %v", err)
			}
			if (msg.Type == "agent" || msg.Type == "error") && endOfTurn {
				return true
			}
		}
		return false
	})
	if err != nil {
		fatalf("Error: %v", err)
	}
}

func cmdList(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client list", flag.ExitOnError)
	archived := fs.Bool("archived", false, "List archived conversations instead")
	limit := fs.Int("limit", 50, "Maximum number of conversations to return")
	query := fs.String("q", "", "Search query")
	fs.Parse(args)

	endpoint := "/api/conversations"
	if *archived {
		endpoint = "/api/conversations/archived"
	}
	params := fmt.Sprintf("?limit=%d", *limit)
	if *query != "" {
		params += "&q=" + url.QueryEscape(*query)
	}
	printConversations(cc, endpoint+params)
}

func cmdSearch(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client search", flag.ExitOnError)
	limit := fs.Int("limit", 20, "Maximum number of results")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: shelley client search [flags] QUERY\n\n")
		fmt.Fprintf(fs.Output(), "Search conversations by slug and message content.\n\n")
		fmt.Fprintf(fs.Output(), "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(1)
	}
	query := strings.Join(fs.Args(), " ")
	printConversations(cc, fmt.Sprintf("/api/conversations?q=%s&search_content=true&limit=%d", url.QueryEscape(query), *limit))
}

// printConversations prints one JSON summary line per conversation listed
// at path.
func printConversations(cc *clientConfig, path string) {
	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fatalf("Error: %v", err)
	}
	var conversations []struct {
		ConversationID string  `json:"conversation_id"`
		Slug           *string `json:"slug"`
		CreatedAt      string  `json:"created_at"`
		UpdatedAt      string  `json:"updated_at"`
		Working        bool    `json:"working"`
		Model          *string `json:"model"`
	}
	if err := cc.doJSON(client, http.MethodGet, baseURL+path, nil, &conversations); err != nil {
		fatalf("Error: %v", err)
	}
	for _, c := range conversations {
		json.NewEncoder(os.Stdout).Encode(c)
	}
}

func cmdArchive(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client archive", flag.ExitOnError)
	fs.Parse(args)

	if fs.NArg() == 0 {
		fatalf("Usage: shelley client archive CONVERSATION_ID")
	}
	conversationID := fs.Arg(0)

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fatalf("Error: %v", err)
	}
	archiveConversation(cc, client, baseURL, conversationID)
	fmt.Fprintf(os.Stderr, "Archived %s\n", conversationID)
}

func cmdCallTranscript(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client call-transcript", flag.ExitOnError)
	conversationID := fs.String("c", os.Getenv("SHELLEY_CONVERSATION_ID"), "conversation (default: $SHELLEY_CONVERSATION_ID)")
	fs.Parse(args)
	if fs.NArg() != 1 || *conversationID == "" {
		fatalf("Usage: shelley client call-transcript [-c CONVERSATION_ID] CALL")
	}
	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fatalf("Error: %v", err)
	}
	transcript, err := fetchCallTranscript(cc, client, baseURL, *conversationID, fs.Arg(0))
	if err != nil {
		fatalf("Error: %v", err)
	}
	fmt.Print(transcript)
}

// fetchCallTranscript reads a voice call's saved transcript.
func fetchCallTranscript(cc *clientConfig, client *http.Client, baseURL, conversationID, call string) (string, error) {
	u := baseURL + "/api/conversation/" + url.PathEscape(conversationID) + "/call/" + url.PathEscape(call) + "/transcript"
	req, err := cc.newRequest("GET", u, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

// --- Wire types for JSON parsing ---

type streamResponseWire struct {
	Messages     []messageWire   `json:"messages"`
	Conversation json.RawMessage `json:"conversation"`
	Heartbeat    bool            `json:"heartbeat"`
}

type messageWire struct {
	MessageID           string  `json:"message_id"`
	ConversationID      string  `json:"conversation_id"`
	SequenceID          int64   `json:"sequence_id"`
	Type                string  `json:"type"`
	LlmData             *string `json:"llm_data,omitempty"`
	UserData            *string `json:"user_data,omitempty"`
	UsageData           *string `json:"usage_data,omitempty"`
	OtherUsageData      *string `json:"other_usage_data,omitempty"`
	CreatedAt           string  `json:"created_at"`
	DisplayData         *string `json:"display_data,omitempty"`
	Generation          int64   `json:"generation"`
	EndOfTurn           *bool   `json:"end_of_turn,omitempty"`
	LLMAPIURL           *string `json:"llm_api_url,omitempty"`
	ModelName           *string `json:"model_name,omitempty"`
	ForkedFromMessageID *string `json:"forked_from_message_id,omitempty"`
	UserEmail           *string `json:"user_email,omitempty"`
}

type llmMessageWire struct {
	Content []llmContentWire `json:"Content"`
}

type llmContentWire struct {
	Type     int    `json:"Type"`
	Text     string `json:"Text,omitempty"`
	ToolName string `json:"ToolName,omitempty"`
}

// Content type constants matching llm.ContentType iota values from llm/llm.go.
const (
	contentTypeText       = 2
	contentTypeToolUse    = 5
	contentTypeToolResult = 6
)

func simplifyMessage(msg messageWire) streamEvent {
	event := streamEvent{
		SequenceID: msg.SequenceID,
		Type:       msg.Type,
	}

	if msg.EndOfTurn != nil {
		event.EndOfTurn = *msg.EndOfTurn
	}

	if msg.LlmData == nil {
		return event
	}

	var llmMsg llmMessageWire
	if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
		return event
	}

	var texts []string
	for _, c := range llmMsg.Content {
		switch c.Type {
		case contentTypeText:
			if c.Text != "" {
				texts = append(texts, c.Text)
			}
		case contentTypeToolUse:
			if event.ToolName == "" && c.ToolName != "" {
				event.ToolName = c.ToolName
			}
		case contentTypeToolResult:
			if c.Text != "" {
				texts = append(texts, c.Text)
			}
		}
	}
	event.Text = strings.Join(texts, "\n")

	return event
}

func cmdHelp() {
	fmt.Printf(`EXPERIMENTAL: Shelley CLI Client

Usage:
  shelley client [flags] <subcommand> [args...]

Flags:
  -url URL     Server URL (default: unix://$SHELLEY_SOCKET if set, else unix://%s)
  -H HEADER    Extra HTTP header "Name: Value" (can be repeated)

Subcommands:
  chat -p PROMPT [-c CONVERSATION_ID] [-model MODEL] [-cwd DIR]
       [-reasoning LEVEL] [-tool NAME=on|off ...] [-no-tools]
       [-tag TAG ...] [-ephemeral] [-disable-notifications]
      Send a message. Creates a new conversation unless -c is given.
      Prints JSON with conversation_id to stdout.
      -reasoning accepts off, minimal, low, medium, high, xhigh, or max.
      -tool is repeatable; unspecified tools keep their server defaults.
      -no-tools disables every tool, while -tool NAME=on can re-enable one.
      Reasoning and tool options apply to new conversations only.
      -tag is repeatable and adds tags after the message is accepted; it also
      works when continuing an existing conversation.
      With -ephemeral, waits for the agent turn to end and then archives
      the conversation (useful for cron-style invocations that clean up
      after themselves).
      With -disable-notifications, disables end-of-turn notifications (push,
      email, discord, ntfy) for the conversation. New conversations only.

  read [-wait] [-full | -usage] CONVERSATION_ID
      Read all messages in a conversation as JSON lines.
      With -wait, streams via SSE until the agent turn ends.
      With -full, emits complete API message records with parsed llm_data,
      content blocks, usage data, timestamps, model, and conversation metadata.
      With -usage, emits one aggregate usage object for the conversation and
      all descendant conversations, including per-model totals. input_tokens
      includes cache-creation tokens; cached_input_tokens is cache-read input.
      raw_input_tokens and both native cache fields are also included.

  list [-archived] [-limit N] [-q QUERY]
      List conversations as JSON lines.

  search [-limit N] QUERY
      Search conversations by slug and message content.
      Prints matching conversations as JSON lines.

  tag [-rm | -set] CONVERSATION_ID [TAG...]
      Show, add, or remove conversation tags. With no TAGs, prints the
      current list. Tags may be brand new or ones already in use on other
      conversations. -rm removes the listed tags; -set replaces the whole
      list (with no TAGs it clears them).
      Prints JSON with the resulting tag list.

  tags [-limit N]
      List the tags already in use across active and archived
      conversations, most-used first, as JSON lines ({tag, count}).
      Use it to pick an existing tag rather than inventing a near-duplicate.

  archive CONVERSATION_ID
      Archive a conversation.

  call-transcript [-c CONVERSATION_ID] CALL
      Print a voice call's saved transcript, one line per turn, numbered
      by its last caption; captions are numbered 1, 2, ... through the call.
      Calls are numbered 1, 2, ... within their conversation, which
      defaults to $SHELLEY_CONVERSATION_ID.

  help
      Print this help text.

Connecting over HTTP with auth headers:
  shelley client -url http://localhost:9999 -H "X-Exedev-Userid: user" list

Examples:
  # Start a high-reasoning conversation without bash, tagged benchmark
  ID=$(shelley client chat -model glm-5.3-fireworks -reasoning high \
    -tool bash=off -tag benchmark -p "list files" | jq -r .conversation_id)
  shelley client read -wait "$ID"

  # Read complete records or aggregate usage
  shelley client read -full "$ID"
  shelley client read -usage "$ID"

  # Continue a conversation
  shelley client chat -c "$ID" -p "now count them"

  # Read current state
  shelley client read "$ID"

NOTE: This feature is EXPERIMENTAL and may change without notice.
`, DefaultSocketPath())
}
