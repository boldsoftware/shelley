package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const unknownModel = "unknown"

type fullMessageRecord struct {
	MessageID           string  `json:"message_id"`
	ConversationID      string  `json:"conversation_id"`
	SequenceID          int64   `json:"sequence_id"`
	Type                string  `json:"type"`
	Role                string  `json:"role"`
	EndOfTurn           bool    `json:"end_of_turn"`
	Model               string  `json:"model"`
	LlmData             any     `json:"llm_data"`
	Content             any     `json:"content"`
	UserData            any     `json:"user_data"`
	UsageData           any     `json:"usage_data"`
	OtherUsageData      any     `json:"other_usage_data"`
	CreatedAt           string  `json:"created_at"`
	DisplayData         any     `json:"display_data"`
	Generation          int64   `json:"generation"`
	ExcludedFromContext bool    `json:"excluded_from_context"`
	LLMAPIURL           *string `json:"llm_api_url"`
	ModelName           *string `json:"model_name"`
	ForkedFromMessageID *string `json:"forked_from_message_id"`
	UserEmail           *string `json:"user_email"`
	Conversation        any     `json:"conversation"`
}

func decodeJSONString(raw *string) (any, error) {
	if raw == nil {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(*raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func decodeRawJSON(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func numberAsInt(value any) (int, bool) {
	switch n := value.(type) {
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case float64:
		return int(n), n == float64(int(n))
	default:
		return 0, false
	}
}

func roleName(value any, messageType string) string {
	if role, ok := numberAsInt(value); ok {
		switch role {
		case 0:
			return "user"
		case 1:
			return "assistant"
		default:
			return fmt.Sprintf("unknown(%d)", role)
		}
	}
	switch messageType {
	case "user":
		return "user"
	case "agent", "error":
		return "assistant"
	case "system":
		return "system"
	default:
		return ""
	}
}

func contentTypeName(value any) string {
	typ, ok := numberAsInt(value)
	if !ok {
		return "unknown"
	}
	switch typ {
	case 2:
		return "text"
	case 3:
		return "thinking"
	case 4:
		return "redacted_thinking"
	case 5:
		return "tool_use"
	case 6:
		return "tool_result"
	case 7:
		return "server_tool_use"
	case 8:
		return "web_search_tool_result"
	case 9:
		return "web_search_result"
	default:
		return fmt.Sprintf("unknown(%d)", typ)
	}
}

func normalizeContent(value any) any {
	blocks, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, len(blocks))
	for i, block := range blocks {
		object, ok := block.(map[string]any)
		if !ok {
			out[i] = block
			continue
		}
		copy := make(map[string]any, len(object)+1)
		for key, value := range object {
			copy[key] = value
		}
		copy["type"] = contentTypeName(object["Type"])
		if nested, ok := object["ToolResult"]; ok {
			copy["ToolResult"] = normalizeContent(nested)
		}
		out[i] = copy
	}
	return out
}

func conversationModel(conversation any) string {
	object, ok := conversation.(map[string]any)
	if !ok {
		return ""
	}
	model, _ := object["model"].(string)
	return model
}

func fullMessage(msg messageWire, conversation any) (fullMessageRecord, error) {
	llmData, err := decodeJSONString(msg.LlmData)
	if err != nil {
		return fullMessageRecord{}, fmt.Errorf("message %d llm_data: %w", msg.SequenceID, err)
	}
	userData, err := decodeJSONString(msg.UserData)
	if err != nil {
		return fullMessageRecord{}, fmt.Errorf("message %d user_data: %w", msg.SequenceID, err)
	}
	usageData, err := decodeJSONString(msg.UsageData)
	if err != nil {
		return fullMessageRecord{}, fmt.Errorf("message %d usage_data: %w", msg.SequenceID, err)
	}
	otherUsageData, err := decodeJSONString(msg.OtherUsageData)
	if err != nil {
		return fullMessageRecord{}, fmt.Errorf("message %d other_usage_data: %w", msg.SequenceID, err)
	}
	displayData, err := decodeJSONString(msg.DisplayData)
	if err != nil {
		return fullMessageRecord{}, fmt.Errorf("message %d display_data: %w", msg.SequenceID, err)
	}

	var roleValue, content any
	endOfTurn := false
	excludedFromContext := false
	if object, ok := llmData.(map[string]any); ok {
		roleValue = object["Role"]
		content = normalizeContent(object["Content"])
		if value, ok := object["EndOfTurn"].(bool); ok {
			endOfTurn = value
		}
		if value, ok := object["ExcludedFromContext"].(bool); ok {
			excludedFromContext = value
		}
	}
	if msg.EndOfTurn != nil {
		endOfTurn = *msg.EndOfTurn
	}

	model := conversationModel(conversation)
	if msg.ModelName != nil && *msg.ModelName != "" {
		model = *msg.ModelName
	}

	return fullMessageRecord{
		MessageID:           msg.MessageID,
		ConversationID:      msg.ConversationID,
		SequenceID:          msg.SequenceID,
		Type:                msg.Type,
		Role:                roleName(roleValue, msg.Type),
		EndOfTurn:           endOfTurn,
		Model:               model,
		LlmData:             llmData,
		Content:             content,
		UserData:            userData,
		UsageData:           usageData,
		OtherUsageData:      otherUsageData,
		CreatedAt:           msg.CreatedAt,
		DisplayData:         displayData,
		Generation:          msg.Generation,
		ExcludedFromContext: excludedFromContext,
		LLMAPIURL:           msg.LLMAPIURL,
		ModelName:           msg.ModelName,
		ForkedFromMessageID: msg.ForkedFromMessageID,
		UserEmail:           msg.UserEmail,
		Conversation:        conversation,
	}, nil
}

type usageBreakdown struct {
	InputTokens              uint64 `json:"input_tokens"`
	CachedInputTokens        uint64 `json:"cached_input_tokens"`
	OutputTokens             uint64 `json:"output_tokens"`
	LLMCalls                 uint64 `json:"llm_calls"`
	RawInputTokens           uint64 `json:"raw_input_tokens"`
	CacheCreationInputTokens uint64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     uint64 `json:"cache_read_input_tokens"`
}

type conversationUsage struct {
	ConversationID      string `json:"conversation_id"`
	IncludesDescendants bool   `json:"includes_descendants"`
	ConversationCount   int    `json:"conversation_count"`
	SubagentCount       int    `json:"subagent_count"`
	usageBreakdown
	PerModel map[string]*usageBreakdown `json:"per_model"`
}

type usageWire struct {
	InputTokens              uint64  `json:"input_tokens"`
	CacheCreationInputTokens uint64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     uint64  `json:"cache_read_input_tokens"`
	OutputTokens             uint64  `json:"output_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
	Model                    string  `json:"model"`
	URL                      string  `json:"url"`
}

func (b *usageBreakdown) add(usage usageWire) {
	b.InputTokens += usage.InputTokens + usage.CacheCreationInputTokens
	b.CachedInputTokens += usage.CacheReadInputTokens
	b.OutputTokens += usage.OutputTokens
	b.LLMCalls++
	b.RawInputTokens += usage.InputTokens
	b.CacheCreationInputTokens += usage.CacheCreationInputTokens
	b.CacheReadInputTokens += usage.CacheReadInputTokens
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return unknownModel
}

func addUsage(summary *conversationUsage, model string, usage usageWire) {
	summary.usageBreakdown.add(usage)
	model = firstNonEmpty(model)
	breakdown := summary.PerModel[model]
	if breakdown == nil {
		breakdown = &usageBreakdown{}
		summary.PerModel[model] = breakdown
	}
	breakdown.add(usage)
}

func aggregateMessageUsage(summary *conversationUsage, messages []messageWire, configuredModel string) error {
	for _, msg := range messages {
		if msg.ForkedFromMessageID != nil {
			continue
		}
		if msg.Type == "agent" && msg.UsageData != nil {
			var usage usageWire
			if err := json.Unmarshal([]byte(*msg.UsageData), &usage); err != nil {
				return fmt.Errorf("message %d usage_data: %w", msg.SequenceID, err)
			}
			modelName, apiURL := "", ""
			if msg.ModelName != nil {
				modelName = *msg.ModelName
			}
			if msg.LLMAPIURL != nil {
				apiURL = *msg.LLMAPIURL
			}
			hasCall := modelName != "" || apiURL != "" || usage.Model != "" || usage.URL != "" || usage.CostUSD != 0 ||
				usage.InputTokens != 0 || usage.CacheCreationInputTokens != 0 ||
				usage.CacheReadInputTokens != 0 || usage.OutputTokens != 0
			if hasCall {
				addUsage(summary, firstNonEmpty(modelName, usage.Model, configuredModel), usage)
			}
		}
		if msg.OtherUsageData == nil {
			continue
		}
		var entries []usageWire
		if err := json.Unmarshal([]byte(*msg.OtherUsageData), &entries); err != nil {
			return fmt.Errorf("message %d other_usage_data: %w", msg.SequenceID, err)
		}
		for _, usage := range entries {
			addUsage(summary, usage.Model, usage)
		}
	}
	return nil
}

func fetchConversationSnapshot(cc *clientConfig, client *http.Client, baseURL, conversationID string) (streamResponseWire, error) {
	var snapshot streamResponseWire
	err := cc.doJSON(client, http.MethodGet, baseURL+"/api/conversation/"+conversationID, nil, &snapshot)
	return snapshot, err
}

func fetchSubagentIDs(cc *clientConfig, client *http.Client, baseURL, conversationID string) ([]string, error) {
	var rows []struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := cc.doJSON(client, http.MethodGet, baseURL+"/api/conversation/"+conversationID+"/subagents", nil, &rows); err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ConversationID
	}
	return ids, nil
}

func collectConversationUsage(cc *clientConfig, client *http.Client, baseURL, rootID string) (conversationUsage, error) {
	summary := conversationUsage{
		ConversationID:      rootID,
		IncludesDescendants: true,
		PerModel:            make(map[string]*usageBreakdown),
	}
	visited := map[string]bool{rootID: true}
	queue := []string{rootID}
	for len(queue) > 0 {
		conversationID := queue[0]
		queue = queue[1:]

		snapshot, err := fetchConversationSnapshot(cc, client, baseURL, conversationID)
		if err != nil {
			return conversationUsage{}, fmt.Errorf("reading conversation %s: %w", conversationID, err)
		}
		conversation, err := decodeRawJSON(snapshot.Conversation)
		if err != nil {
			return conversationUsage{}, fmt.Errorf("conversation %s metadata: %w", conversationID, err)
		}
		if err := aggregateMessageUsage(&summary, snapshot.Messages, conversationModel(conversation)); err != nil {
			return conversationUsage{}, fmt.Errorf("conversation %s: %w", conversationID, err)
		}
		summary.ConversationCount++

		children, err := fetchSubagentIDs(cc, client, baseURL, conversationID)
		if err != nil {
			return conversationUsage{}, fmt.Errorf("listing subagents of %s: %w", conversationID, err)
		}
		for _, child := range children {
			if child == "" || visited[child] {
				continue
			}
			visited[child] = true
			queue = append(queue, child)
		}
	}
	summary.SubagentCount = summary.ConversationCount - 1
	return summary, nil
}
