package oai

import (
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models/modelsdev"
)

// overrideReasoningEffort is the shared explicit-endpoint policy for OpenAI
// Chat and Responses. Without explicit controls each keeps its legacy behavior.
// Defaults use this same resolution as requests; provider-verbatim efforts are
// never clamped, while generic efforts must belong to the endpoint's list.
func overrideReasoningEffort(caps *modelsdev.ReasoningCapabilities, req *llm.Request, serviceLevel llm.ThinkingLevel, serviceEffort string) (string, bool) {
	if caps == nil || caps.Supported && caps.Levels == nil {
		return "", false
	}
	if req.ReasoningEffort != "" {
		return req.ReasoningEffort, true
	}
	if !caps.Supported || req.ThinkingLevel == llm.ThinkingLevelDefault && serviceEffort != "" {
		return serviceEffort, true
	}
	level := llm.EffectiveThinkingLevel(serviceLevel, req.ThinkingLevel)
	// An unset/off service default leaves effort to the provider, as before.
	if level == llm.ThinkingLevelDefault || req.ThinkingLevel == llm.ThinkingLevelDefault && level == llm.ThinkingLevelOff {
		return "", true
	}
	return effortForThinkingLevel(caps.ResolveLevel(level)), true
}
