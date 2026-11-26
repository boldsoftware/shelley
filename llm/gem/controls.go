package gem

import (
	"cmp"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/gemini"
	"shelley.exe.dev/models/modelsdev"
)

var modelDateSuffix = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

func normalizeModelName(model string) string {
	return strings.TrimPrefix(cmp.Or(model, DefaultModel), "models/")
}

func modelKeyFor(modelName string) string {
	return modelDateSuffix.ReplaceAllString(strings.ToLower(path.Base(modelName)), "")
}

func supportsServerSideWebSearch(key string) bool {
	return strings.HasPrefix(key, "gemini-3.") || strings.HasPrefix(key, "gemini-3-") ||
		strings.HasPrefix(key, "gemini-4-") || strings.HasPrefix(key, "gemini-4.")
}

func maxOutputTokensFor(url, modelName string, configuredMax, fallback int) int {
	limit, found := modelsdev.LookupOutputLimit(url, modelName)
	switch modelKeyFor(modelName) {
	case "gemini-4-argon", "gemini-4-carbon":
		// The catalog's 32K limit is non-streaming; these adapters always stream.
		limit, found = 65536, true
	}
	if !found {
		return cmp.Or(configuredMax, fallback)
	}
	return min(cmp.Or(configuredMax, limit), limit)
}

func documentedDefaultReasoningLevel(key string, found, supported bool) string {
	if found && !supported {
		return "off"
	}
	switch key {
	case "gemini-4-argon", "gemini-4-carbon", "gemini-3.8-flash", "gemini-3.6-flash":
		return "medium"
	case "gemini-4-neon", "gemini-4-hydrogen", "gemini-3-flash-preview":
		return "high"
	case "gemini-2.5-flash-lite":
		return "off"
	}
	return ""
}

func (s *Service) modelName() string { return normalizeModelName(s.Model) }

func (s *Service) modelKey() string { return modelKeyFor(s.modelName()) }

type modelThinkingCapabilities struct {
	modelsdev.ReasoningCapabilities
	budget bool
}

func (s *Service) thinkingCapabilities() (modelThinkingCapabilities, bool) {
	caps, found := modelsdev.LookupGeminiReasoningCapabilities(s.modelName())
	result := modelThinkingCapabilities{ReasoningCapabilities: caps}
	switch s.modelKey() {
	case "gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite":
		result.budget = true
		result.Levels = []llm.ThinkingLevel{
			llm.ThinkingLevelMinimal, llm.ThinkingLevelLow, llm.ThinkingLevelMedium, llm.ThinkingLevelHigh,
		}
		if s.modelKey() != "gemini-2.5-pro" {
			result.Levels = append([]llm.ThinkingLevel{llm.ThinkingLevelOff}, result.Levels...)
		}
	}
	return result, found
}

// SupportsThinkingOff reports whether native capabilities allow an off setting.
func (s *Service) SupportsThinkingOff() bool {
	caps, found := s.thinkingCapabilities()
	return found && (!caps.Supported || slices.Contains(caps.Levels, llm.ThinkingLevelOff))
}

// resolveThinking resolves request/service reasoning overrides against model capabilities.
// Returns (resolvedLevel, verbatimEffort, active, err). When verbatimEffort != "", the model's
// controls are unknown and the effort string should be forwarded as-is.
func resolveThinking(modelName string, caps modelsdev.ReasoningCapabilities, found bool, req *llm.Request, svcEffort string, svcLevel llm.ThinkingLevel) (llm.ThinkingLevel, string, bool, error) {
	var level llm.ThinkingLevel
	var effort string
	switch {
	case req != nil && req.ReasoningEffort != "":
		effort = req.ReasoningEffort
	case req != nil && req.ThinkingLevel != llm.ThinkingLevelDefault:
		level = req.ThinkingLevel
	case svcEffort != "":
		effort = svcEffort
	default:
		level = svcLevel
	}
	if level == llm.ThinkingLevelDefault && effort == "" {
		return llm.ThinkingLevelDefault, "", false, nil
	}
	if !found || caps.Supported && len(caps.Levels) == 0 {
		if effort != "" {
			return llm.ThinkingLevelDefault, effort, true, nil
		}
		return llm.ThinkingLevelDefault, "", false, fmt.Errorf("unknown thinking controls for %s; use its provider default or an explicit reasoning effort", modelName)
	}
	if !caps.Supported {
		if level == llm.ThinkingLevelOff || effort == "off" || effort == "none" {
			return llm.ThinkingLevelDefault, "", false, nil
		}
		return llm.ThinkingLevelDefault, "", false, fmt.Errorf("%s does not support thinking", modelName)
	}
	if effort != "" {
		level = llm.ParseThinkingLevel(effort)
		if effort == "none" {
			level = llm.ThinkingLevelOff
		}
	}
	if level == llm.ThinkingLevelOff && !slices.Contains(caps.Levels, level) {
		return llm.ThinkingLevelDefault, "", false, fmt.Errorf("%s cannot disable thinking", modelName)
	}
	if effort != "" {
		if !slices.Contains(caps.Levels, level) || effort != level.Name() && effort != "none" {
			return llm.ThinkingLevelDefault, "", false, fmt.Errorf("thinking level %q is not supported by %s", effort, modelName)
		}
	} else {
		level = llm.ClampThinkingLevel(level, caps.Levels)
	}
	return level, "", true, nil
}

// Request effort > request generic level > service effort > service generic level.
func (s *Service) thinkingConfig(req *llm.Request) (*gemini.ThinkingConfig, error) {
	caps, found := s.thinkingCapabilities()
	level, verbatim, active, err := resolveThinking(s.modelName(), caps.ReasoningCapabilities, found, req, s.ReasoningEffort, s.ThinkingLevel)
	if err != nil || !active {
		return nil, err
	}
	if verbatim != "" {
		return &gemini.ThinkingConfig{ThinkingLevel: verbatim, IncludeThoughts: verbatim != "off" && verbatim != "none"}, nil
	}
	if caps.budget {
		budget := level.ThinkingBudgetTokens()
		return &gemini.ThinkingConfig{ThinkingBudget: &budget, IncludeThoughts: budget != 0}, nil
	}
	return &gemini.ThinkingConfig{ThinkingLevel: level.Name(), IncludeThoughts: level != llm.ThinkingLevelOff}, nil
}
