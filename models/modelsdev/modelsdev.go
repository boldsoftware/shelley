// Package modelsdev consults a snapshot of https://models.dev/api.json to
// answer capability questions (currently: does a given model accept image
// inputs?).
//
// The snapshot is embedded at build time. Updating it is a manual exercise
// of replacing api.json in this directory.
package modelsdev

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"shelley.exe.dev/llm"
)

//go:embed api.json
var apiJSON []byte

type modelEntry struct {
	Reasoning        bool              `json:"reasoning"`
	ReasoningOptions []reasoningOption `json:"reasoning_options"`
	ReleaseDate      string            `json:"release_date"`
	Modalities       struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Cost Cost `json:"cost"`
}

type reasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

// ReasoningCapabilities describes the reasoning controls models.dev records
// for a model. Levels is empty when the model supports reasoning but its
// reasoning_options do not contain an explicit effort list.
type ReasoningCapabilities struct {
	Supported bool
	Levels    []llm.ThinkingLevel
}

// Cost is a model's pricing in USD per million tokens, as recorded by
// models.dev. CacheRead/CacheWrite are zero for providers that don't price
// caching separately.
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

func (c Cost) isZero() bool { return c == Cost{} }

type providerEntry struct {
	// API is the provider's base URL (the "api" field in models.dev), e.g.
	// "https://opencode.ai/zen/v1". Used to match custom models by their
	// configured endpoint instead of by Shelley's internal provider name.
	API    string                `json:"api"`
	Models map[string]modelEntry `json:"models"`
}

var (
	parseOnce sync.Once
	parsed    map[string]providerEntry
	// hostIndex maps a normalized host (e.g. "opencode.ai") to the provider
	// entries whose "api" URL lives on that host. A host may serve more than
	// one provider (e.g. opencode / opencode-go), so this is a slice.
	hostIndex map[string][]providerEntry
)

func load() map[string]providerEntry {
	parseOnce.Do(func() {
		if err := json.Unmarshal(apiJSON, &parsed); err != nil {
			// Embedded data is shipped with the binary; failing to parse it
			// is a programmer error.
			panic("modelsdev: failed to parse embedded api.json: " + err.Error())
		}
		hostIndex = make(map[string][]providerEntry)
		for _, p := range parsed {
			if h := hostOf(p.API); h != "" {
				hostIndex[h] = append(hostIndex[h], p)
			}
		}
		// models.dev omits the "api" field for the major first-party
		// providers (their base URL is implicit in the official SDKs), so they
		// never make it into hostIndex above. Wire up their well-known hosts
		// explicitly so custom models pointed at the official endpoints still
		// resolve.
		for host, key := range knownHosts {
			if p, ok := parsed[key]; ok {
				hostIndex[host] = append(hostIndex[host], p)
			}
		}
	})
	return parsed
}

// knownHosts maps the well-known first-party API hosts to their models.dev
// provider keys. models.dev does not record an "api" URL for these, so they
// are seeded into the host index manually.
var knownHosts = map[string]string{
	"api.anthropic.com":                 "anthropic",
	"api.openai.com":                    "openai",
	"generativelanguage.googleapis.com": "google",
}

// hostOf extracts a normalized host from a URL or bare host string. It strips
// a leading "www." and lowercases the result. Returns "" if no host is found.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// url.Parse needs a scheme to populate Host; add one if missing.
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	return strings.TrimPrefix(h, "www.")
}

// LookupImageSupport reports whether models.dev says (endpoint, modelName)
// accepts image inputs. The second return value is false if we have no
// information about this model.
//
// endpoint is the base URL the model is configured to talk to (e.g.
// "https://opencode.ai/zen/v1"); it may be empty. modelName is the value sent
// to the underlying provider.
//
// Models are matched to a models.dev provider by the HOST of their endpoint,
// matched softly on the path. Callers don't always configure the exact path
// models.dev records, so the host must agree and the path is used only to
// disambiguate when a single host serves more than one provider (e.g.
// opencode at /zen/v1 vs opencode-go at /zen/go/v1): the provider whose "api"
// path shares the longest leading run of path segments with the endpoint
// wins. First-party hosts that models.dev omits an "api" field for are seeded
// from knownHosts. The model id is then resolved within the chosen
// provider's catalog (exact, case-insensitive, or last "/" segment).
//
// Lookup strategy, in order:
//  1. the best-path-matching provider whose host matches the endpoint host
//  2. the "openrouter" catalog (full "vendor/model" slugs), as a last resort
func LookupImageSupport(endpoint, modelName string) (supported, found bool) {
	m, found := lookup(endpoint, modelName)
	return entryHasImage(m), found
}

// LookupReasoningSupport reports whether models.dev says the model supports
// reasoning. The second return value is false when the model is unknown.
func LookupReasoningSupport(endpoint, modelName string) (supported, found bool) {
	m, found := lookup(endpoint, modelName)
	return m.Reasoning, found
}

// LookupReasoningCapabilities returns the reasoning support and explicit effort
// levels models.dev records for a model. Unknown models return found=false.
//
// Only explicit effort values become levels. "none" maps to off, and a toggle
// adds off when an effort list is also present. Toggle-only and budget-token
// controls do not define exact generic levels, so Levels stays empty.
func LookupReasoningCapabilities(endpoint, modelName string) (ReasoningCapabilities, bool) {
	m, found := lookupBroad(endpoint, modelName, func(modelEntry) bool { return true })
	if !found {
		return ReasoningCapabilities{}, false
	}
	return parseReasoningCapabilities(m), true
}

func parseReasoningCapabilities(m modelEntry) (caps ReasoningCapabilities) {
	caps.Supported = m.Reasoning
	if !m.Reasoning {
		return caps
	}

	set := map[llm.ThinkingLevel]bool{}
	hasEffort := false
	hasToggle := false
	for _, option := range m.ReasoningOptions {
		switch option.Type {
		case "effort":
			for _, value := range option.Values {
				level := llm.ParseThinkingLevel(value)
				if value == "none" {
					level = llm.ThinkingLevelOff
				}
				if level == llm.ThinkingLevelDefault {
					continue
				}
				set[level] = true
				hasEffort = true
			}
		case "toggle":
			hasToggle = true
		}
	}
	if !hasEffort {
		return caps
	}
	if hasToggle {
		set[llm.ThinkingLevelOff] = true
	}
	for _, level := range reasoningLevels {
		if set[level] {
			caps.Levels = append(caps.Levels, level)
		}
	}
	return caps
}

var reasoningLevels = []llm.ThinkingLevel{
	llm.ThinkingLevelOff,
	llm.ThinkingLevelMinimal,
	llm.ThinkingLevelLow,
	llm.ThinkingLevelMedium,
	llm.ThinkingLevelHigh,
	llm.ThinkingLevelXHigh,
	llm.ThinkingLevelMax,
}

// LookupReleaseDate returns the ISO release date models.dev records for a
// model. Gateway endpoints fall back to first-party catalogs by model name.
func LookupReleaseDate(endpoint, modelName string) (string, bool) {
	m, found := lookupBroad(endpoint, modelName, func(m modelEntry) bool { return m.ReleaseDate != "" })
	return m.ReleaseDate, found
}

// LookupCost returns models.dev pricing (USD per million tokens) for
// (endpoint, modelName). The second return value is false when the model is
// unknown or has no pricing.
//
// Cost lookups are more permissive than capability lookups: Shelley usually
// reaches first-party models through gateway hosts models.dev has never heard
// of, and providers snapshot model names with date suffixes (e.g.
// "gpt-5.5-2026-04-23") that models.dev omits. Each strategy — endpoint host,
// then first-party catalogs by name, then the OpenRouter catalog — is tried
// with the full name and with a trailing -YYYY-MM-DD stripped.
func LookupCost(endpoint, modelName string) (Cost, bool) {
	m, found := lookupBroad(endpoint, modelName, func(m modelEntry) bool { return !m.Cost.isZero() })
	return m.Cost, found
}

// lookupBroad resolves models that may be reached through gateway hosts absent
// from models.dev: endpoint host first, then first-party catalogs by bare name,
// then OpenRouter. Each path also tries a trailing date suffix stripped.
func lookupBroad(endpoint, modelName string, usable func(modelEntry) bool) (modelEntry, bool) {
	data := load()
	names := []string{modelName}
	if stripped := dateSuffixRe.ReplaceAllString(modelName, ""); stripped != modelName {
		names = append(names, stripped)
	}
	tryProvider := func(p providerEntry, name string) (modelEntry, bool) {
		m, found := lookupInProvider(p, name)
		return m, found && usable(m)
	}
	for _, name := range names {
		if host := hostOf(endpoint); host != "" {
			if p, ok := bestProviderForPath(hostIndex[host], pathSegments(endpoint), name); ok {
				if m, ok := tryProvider(p, name); ok {
					return m, true
				}
			}
		}
	}
	for _, name := range names {
		for _, provider := range firstPartyProviders {
			if p, ok := data[provider]; ok {
				if m, ok := tryProvider(p, name); ok {
					return m, true
				}
			}
		}
	}
	for _, name := range names {
		if p, ok := data["openrouter"]; ok {
			if m, ok := tryProvider(p, name); ok {
				return m, true
			}
		}
	}
	return modelEntry{}, false
}

// firstPartyProviders are the models.dev catalogs scanned by bare model name
// for cost lookups, in preference order.
var firstPartyProviders = []string{"anthropic", "openai", "google", "fireworks-ai", "xai"}

// dateSuffixRe matches provider snapshot date suffixes like "-2026-04-23".
var dateSuffixRe = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

func lookup(endpoint, modelName string) (modelEntry, bool) {
	data := load()
	if host := hostOf(endpoint); host != "" {
		if p, ok := bestProviderForPath(hostIndex[host], pathSegments(endpoint), modelName); ok {
			return lookupInProvider(p, modelName)
		}
	}
	// Last-resort: OpenRouter keeps a full slug catalog.
	if p, ok := data["openrouter"]; ok {
		return lookupInProvider(p, modelName)
	}
	return modelEntry{}, false
}

// bestProviderForPath picks, among providers that carry modelName, the one
// whose "api" path best matches endpointSegs (longest shared leading run of
// path segments). Only providers that actually contain the model are
// considered, so a better-path provider that lacks the model never shadows a
// worse-path provider that has it. Returns ok=false if no provider carries
// the model.
func bestProviderForPath(providers []providerEntry, endpointSegs []string, modelName string) (providerEntry, bool) {
	var best providerEntry
	bestScore := -1
	found := false
	for _, p := range providers {
		if _, hit := lookupInProvider(p, modelName); !hit {
			continue
		}
		score := commonPrefixLen(pathSegments(p.API), endpointSegs)
		if score > bestScore {
			best, bestScore, found = p, score, true
		}
	}
	return best, found
}

// pathSegments splits a URL's path into non-empty, lowercased segments.
func pathSegments(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	var segs []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segs = append(segs, strings.ToLower(s))
		}
	}
	return segs
}

// commonPrefixLen returns the number of equal leading elements of a and b.
func commonPrefixLen(a, b []string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// lookupInProvider tries exact, case-insensitive, and last-segment matches
// for modelName within p.Models.
func lookupInProvider(p providerEntry, modelName string) (modelEntry, bool) {
	if m, ok := p.Models[modelName]; ok {
		return m, true
	}
	lower := strings.ToLower(modelName)
	for id, entry := range p.Models {
		if strings.ToLower(id) == lower {
			return entry, true
		}
	}
	// Try the last "/"-separated segment (e.g. "openai/gpt-4o" -> "gpt-4o").
	if i := strings.LastIndex(modelName, "/"); i >= 0 && i+1 < len(modelName) {
		tail := modelName[i+1:]
		if m, ok := p.Models[tail]; ok {
			return m, true
		}
		tailLower := strings.ToLower(tail)
		for id, entry := range p.Models {
			if strings.ToLower(id) == tailLower {
				return entry, true
			}
		}
	}
	return modelEntry{}, false
}

func entryHasImage(m modelEntry) bool {
	for _, mod := range m.Modalities.Input {
		if mod == "image" {
			return true
		}
	}
	return false
}

// Modalities are the input and output modalities models.dev records for a
// model (e.g. Input: ["text", "image"], Output: ["text"]).
type Modalities struct {
	Input  []string
	Output []string
}

// LookupModalities returns the input/output modalities models.dev records for
// (endpoint, modelName). The second return value is false when the model is
// unknown or has no recorded modalities. Matching follows the same strategy
// as LookupImageSupport: endpoint host with path affinity, then the
// OpenRouter catalog.
func LookupModalities(endpoint, modelName string) (Modalities, bool) {
	m, found := lookup(endpoint, modelName)
	if !found || (len(m.Modalities.Input) == 0 && len(m.Modalities.Output) == 0) {
		return Modalities{}, false
	}
	return Modalities{
		Input:  append([]string(nil), m.Modalities.Input...),
		Output: append([]string(nil), m.Modalities.Output...),
	}, true
}
