package modelsdev

import "shelley.exe.dev/llm"

// Lookup uses the controls supplied for this service's (model, endpoint) pair.
// A nil override leaves the models.dev lookup unchanged.
func (override *ReasoningCapabilities) Lookup(endpoint, model string) (ReasoningCapabilities, bool) {
	if override != nil {
		return *override, true
	}
	return LookupReasoningCapabilities(endpoint, model)
}

// ResolveLevel applies explicit endpoint controls. Unlike the general model
// switch clamp, an off-only endpoint cannot receive an enabled default effort.
// Catalog-only callers keep their existing clamping rules.
func (caps ReasoningCapabilities) ResolveLevel(level llm.ThinkingLevel) llm.ThinkingLevel {
	if !caps.Supported || len(caps.Levels) == 1 && caps.Levels[0] == llm.ThinkingLevelOff {
		return llm.ThinkingLevelOff
	}
	return llm.ClampThinkingLevel(level, caps.Levels)
}
