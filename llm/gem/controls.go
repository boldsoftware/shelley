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

func (s *Service) modelName() string {
	return strings.TrimPrefix(cmp.Or(s.Model, DefaultModel), "models/")
}

func (s *Service) modelKey() string {
	return modelDateSuffix.ReplaceAllString(strings.ToLower(path.Base(s.modelName())), "")
}

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

// Request effort > request generic level > service effort > service generic level.
func (s *Service) thinkingConfig(req *llm.Request) (*gemini.ThinkingConfig, error) {
	var level llm.ThinkingLevel
	var effort string
	switch {
	case req != nil && req.ReasoningEffort != "":
		effort = req.ReasoningEffort
	case req != nil && req.ThinkingLevel != llm.ThinkingLevelDefault:
		level = req.ThinkingLevel
	case s.ReasoningEffort != "":
		effort = s.ReasoningEffort
	default:
		level = s.ThinkingLevel
	}
	if level == llm.ThinkingLevelDefault && effort == "" {
		return nil, nil
	}
	caps, found := s.thinkingCapabilities()
	if !found || caps.Supported && len(caps.Levels) == 0 {
		if effort != "" {
			return &gemini.ThinkingConfig{ThinkingLevel: effort, IncludeThoughts: effort != "off" && effort != "none"}, nil
		}
		return nil, fmt.Errorf("unknown thinking controls for %s; use its provider default or an explicit reasoning effort", s.modelName())
	}
	if !caps.Supported {
		if level == llm.ThinkingLevelOff || effort == "off" || effort == "none" {
			return nil, nil
		}
		return nil, fmt.Errorf("%s does not support thinking", s.modelName())
	}

	if effort != "" {
		level = llm.ParseThinkingLevel(effort)
		if effort == "none" {
			level = llm.ThinkingLevelOff
		}
	}
	if level == llm.ThinkingLevelOff && !slices.Contains(caps.Levels, level) {
		return nil, fmt.Errorf("%s cannot disable thinking", s.modelName())
	}
	if effort != "" {
		if !slices.Contains(caps.Levels, level) || effort != level.Name() && effort != "none" {
			return nil, fmt.Errorf("thinking level %q is not supported by %s", effort, s.modelName())
		}
	} else {
		level = llm.ClampThinkingLevel(level, caps.Levels)
	}
	if caps.budget {
		budget := level.ThinkingBudgetTokens()
		return &gemini.ThinkingConfig{ThinkingBudget: &budget, IncludeThoughts: budget != 0}, nil
	}
	return &gemini.ThinkingConfig{ThinkingLevel: level.Name(), IncludeThoughts: level != llm.ThinkingLevelOff}, nil
}
