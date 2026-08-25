package client

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
)

// normalizeTagList trims whitespace, drops a leading "#" (people type it
// out of habit; stored tags never carry one), and removes empty/duplicate
// entries while preserving first-seen order. Mirrors the server's
// normalizeTags so the local view matches what gets persisted.
func normalizeTagList(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimLeft(strings.TrimSpace(t), "#")
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// mergeTags returns existing plus any of add not already present.
func mergeTags(existing, add []string) []string {
	return normalizeTagList(append(append([]string{}, existing...), add...))
}

// removeTags returns existing minus everything in rm.
func removeTags(existing, rm []string) []string {
	drop := make(map[string]struct{}, len(rm))
	for _, t := range normalizeTagList(rm) {
		drop[t] = struct{}{}
	}
	out := make([]string, 0, len(existing))
	for _, t := range normalizeTagList(existing) {
		if _, ok := drop[t]; ok {
			continue
		}
		out = append(out, t)
	}
	return out
}

// parseTagsField decodes the JSON-array-in-a-string form the server uses
// for the conversations.tags column. Empty and malformed values read as
// "no tags" rather than an error: an untagged conversation is normal.
func parseTagsField(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil
	}
	return normalizeTagList(tags)
}

// fetchConversationTags reads the current tag list for a conversation.
func fetchConversationTags(cc *clientConfig, client *http.Client, baseURL, conversationID string) ([]string, error) {
	req, err := cc.newRequest("GET", baseURL+"/api/conversation/"+conversationID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d fetching conversation %s", resp.StatusCode, conversationID)
	}
	var body struct {
		Conversation struct {
			Tags string `json:"tags"`
		} `json:"conversation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("parsing conversation: %w", err)
	}
	return parseTagsField(body.Conversation.Tags), nil
}

// setConversationTags replaces a conversation's tags and returns the
// server-normalized result.
func setConversationTags(cc *clientConfig, client *http.Client, baseURL, conversationID string, tags []string) ([]string, error) {
	payload, err := json.Marshal(map[string]any{"tags": normalizeTagList(tags)})
	if err != nil {
		return nil, err
	}
	req, err := cc.newRequest("POST", baseURL+"/api/conversation/"+conversationID+"/tags", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d updating tags on %s", resp.StatusCode, conversationID)
	}
	var body struct {
		Tags string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return parseTagsField(body.Tags), nil
}

// tagCount is one row of "tags already in use".
type tagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// listAllTags returns every tag in use across active and archived
// conversations, most-used first (ties broken alphabetically). Archived
// conversations count because a tag you used last month is exactly the
// one you want offered for reuse today.
func listAllTags(cc *clientConfig, client *http.Client, baseURL string, limit int) ([]tagCount, error) {
	counts := map[string]int{}
	for _, endpoint := range []string{"/api/conversations", "/api/conversations/archived"} {
		req, err := cc.newRequest("GET", baseURL+endpoint+fmt.Sprintf("?limit=%d", limit), nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var rows []struct {
			Tags string `json:"tags"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&rows)
		status := resp.StatusCode
		resp.Body.Close()
		if status != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d listing %s", status, endpoint)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("parsing %s: %w", endpoint, decodeErr)
		}
		for _, row := range rows {
			for _, tag := range parseTagsField(row.Tags) {
				counts[tag]++
			}
		}
	}
	out := make([]tagCount, 0, len(counts))
	for tag, n := range counts {
		out = append(out, tagCount{Tag: tag, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	return out, nil
}

// cmdTag implements "shelley client tag".
func cmdTag(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client tag", flag.ExitOnError)
	remove := fs.Bool("rm", false, "Remove the given tags instead of adding them")
	set := fs.Bool("set", false, "Replace the tag list with the given tags (no args clears all tags)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: shelley client tag [flags] CONVERSATION_ID [TAG...]\n\n")
		fmt.Fprintf(fs.Output(), "With no TAGs (and no -set), prints the conversation's current tags.\n")
		fmt.Fprintf(fs.Output(), "Otherwise adds them; tags may be new or already in use elsewhere.\n\n")
		fmt.Fprintf(fs.Output(), "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(1)
	}
	if *remove && *set {
		fmt.Fprintf(os.Stderr, "Error: -rm and -set are mutually exclusive\n")
		os.Exit(1)
	}
	conversationID := fs.Arg(0)
	tagArgs := fs.Args()[1:]

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var tags []string
	switch {
	case *set:
		tags, err = setConversationTags(cc, client, baseURL, conversationID, tagArgs)
	case len(tagArgs) == 0:
		tags, err = fetchConversationTags(cc, client, baseURL, conversationID)
	default:
		var current []string
		current, err = fetchConversationTags(cc, client, baseURL, conversationID)
		if err == nil {
			next := mergeTags(current, tagArgs)
			if *remove {
				next = removeTags(current, tagArgs)
			}
			tags, err = setConversationTags(cc, client, baseURL, conversationID, next)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if tags == nil {
		tags = []string{}
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{
		"conversation_id": conversationID,
		"tags":            tags,
	})
}

// cmdTags implements "shelley client tags": the tags already in use, for
// pickers and shell completion.
func cmdTags(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client tags", flag.ExitOnError)
	limit := fs.Int("limit", 5000, "Maximum conversations to scan per list")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: shelley client tags [flags]\n\n")
		fmt.Fprintf(fs.Output(), "List tags already in use (active + archived), most-used first.\n\n")
		fmt.Fprintf(fs.Output(), "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	tags, err := listAllTags(cc, client, baseURL, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	for _, t := range tags {
		enc.Encode(t)
	}
}
