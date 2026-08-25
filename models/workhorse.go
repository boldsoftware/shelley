package models

import (
	"context"
	"fmt"
	"strings"

	"shelley.exe.dev/llm"
)

// WorkhorseProvider is the subset of Manager needed to run workhorse calls.
type WorkhorseProvider interface {
	GetAvailableModels() []string
	GetModelInfo(modelID string) *ModelInfo
	GetService(modelID string) (llm.Service, error)
}

type workhorseFamily struct {
	contains string
	excludes []string
}

// workhorseFamilies identifies one cheap, fast model family per provider for
// background tasks. The newest available release in the family is selected.
var workhorseFamilies = map[Provider]workhorseFamily{
	ProviderOpenAI:    {contains: "luna"},
	ProviderAnthropic: {contains: "haiku"},
	ProviderGemini: {
		contains: "flash",
		excludes: []string{"lite", "image", "tts", "live", "omni"},
	},
	ProviderFireworks: {contains: "deepseek-v4-flash"},
}

// WorkhorseDo sends req to a cheap model from the conversation's provider. If
// that call fails, or no workhorse is configured, it uses the conversation
// model once.
func WorkhorseDo(ctx context.Context, p WorkhorseProvider, conversationModelID string, req *llm.Request) (*llm.Response, error) {
	modelID := workhorseModel(p, conversationModelID)
	if modelID == "" {
		return nil, fmt.Errorf("no workhorse model available (conversation model %q)", conversationModelID)
	}
	do := func(modelID string) (*llm.Response, error) {
		svc, err := p.GetService(modelID)
		if err != nil {
			return nil, err
		}
		request := *req
		request.ThinkingLevel = llm.ThinkingLevelOff
		request.ReasoningEffort = ""
		return svc.Do(ctx, &request)
	}
	resp, err := do(modelID)
	if err == nil || modelID == conversationModelID {
		return resp, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return do(conversationModelID)
}

func workhorseModel(c WorkhorseProvider, conversationModelID string) string {
	info := c.GetModelInfo(conversationModelID)
	if info == nil {
		return conversationModelID
	}
	family, ok := workhorseFamilies[info.Provider]
	if !ok {
		return conversationModelID
	}
	bestID := ""
	bestDate := ""
	for _, modelID := range c.GetAvailableModels() {
		candidate := c.GetModelInfo(modelID)
		if candidate == nil || candidate.Provider != info.Provider || !matchesWorkhorseFamily(modelID, family) {
			continue
		}
		if bestID == "" || candidate.ReleaseDate > bestDate {
			bestID = modelID
			bestDate = candidate.ReleaseDate
		}
	}
	if bestID != "" {
		return bestID
	}
	return conversationModelID
}

func matchesWorkhorseFamily(modelID string, family workhorseFamily) bool {
	if !strings.Contains(modelID, family.contains) {
		return false
	}
	for _, excluded := range family.excludes {
		if strings.Contains(modelID, excluded) {
			return false
		}
	}
	return true
}
