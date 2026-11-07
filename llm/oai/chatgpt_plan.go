package oai

import (
	"context"
	"encoding/json"
	"strings"
)

// ChatGPTUsageURL is where users review and limit their ChatGPT plan usage.
const ChatGPTUsageURL = "https://chatgpt.com/settings/usage"

// chatGPTToolNamespace groups Shelley's tools: ChatGPT plan requests only
// accept function and custom tools inside a namespace.
const chatGPTToolNamespace = "shelley"

// ChatGPTPlan sends Responses requests on a user's ChatGPT plan through Sign in
// with ChatGPT, within that route's preview limits: no max_output_tokens or
// prompt prewarming, and tools grouped in a namespace.
// See https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations.
type ChatGPTPlan struct {
	// Account identifies the connection. Encrypted reasoning is replayed only
	// to the account that produced it.
	Account     string
	Credentials ChatGPTCredentials
}

// ChatGPTCredentials supplies OAuth access tokens for a ChatGPT plan.
type ChatGPTCredentials interface {
	// Token returns a current access token, refreshing it when it is about to expire.
	Token(ctx context.Context) (string, error)
	// Refresh replaces rejected, which the API refused with 401, and returns its successor.
	Refresh(ctx context.Context, rejected string) (string, error)
}

func chatGPTNamespace(tools []responsesTool) []responsesTool {
	if len(tools) == 0 {
		return nil
	}
	return []responsesTool{{
		Type:        "namespace",
		Name:        chatGPTToolNamespace,
		Description: "Shelley's tools for working in the user's environment.",
		Tools:       tools,
	}}
}

// chatGPTPlanErrorNote explains ChatGPT plan errors that need the user to act.
// Codes are from https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery.
func chatGPTPlanErrorNote(code string) string {
	switch code {
	case "subscription_sharing_usage_limit_exceeded":
		return "ChatGPT plan usage limit reached; manage usage at " + ChatGPTUsageURL
	case "subscription_sharing_user_not_eligible":
		return "ChatGPT plan usage is not available for this account or workspace"
	case "subscription_sharing_invalid_user", "chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context":
		return "ChatGPT did not authorize plan usage for this connection; sign in with ChatGPT again"
	}
	return ""
}

// normalizePlanCode maps legacy subscription_sharing_v2_ codes to their
// current names.
func normalizePlanCode(code string) string {
	return strings.Replace(code, "subscription_sharing_v2_", "subscription_sharing_", 1)
}

// chatGPTPlanErrorCode returns the error code of a ChatGPT plan error body.
func chatGPTPlanErrorCode(apiErr *responsesError) string {
	if apiErr == nil {
		return ""
	}
	var code string
	_ = json.Unmarshal(apiErr.Code, &code)
	return normalizePlanCode(code)
}
