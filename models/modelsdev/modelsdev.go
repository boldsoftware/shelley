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
	"sort"
	"strings"
	"sync"

	"shelley.exe.dev/llm"
)

//go:embed api.json
var apiJSON []byte

type modelEntry struct {
	Reasoning        bool                `json:"reasoning"`
	ReasoningOptions []reasoningOption   `json:"reasoning_options"`
	Interleaved      interleavedMetadata `json:"interleaved"`
	ReleaseDate      string              `json:"release_date"`
	Limit            struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Cost Cost `json:"cost"`
}

type reasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

type interleavedMetadata struct {
	Supported bool
	Field     string
}

func (m *interleavedMetadata) UnmarshalJSON(data []byte) error {
	*m = interleavedMetadata{}
	var supported bool
	if err := json.Unmarshal(data, &supported); err == nil {
		m.Supported = supported
		return nil
	}
	var value struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	m.Supported = true
	m.Field = value.Field
	return nil
}

// ReasoningCapabilities describes the controls for a (model, endpoint) pair.
// Levels is empty when the model supports reasoning but its
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
	// Tiers marks where pricing changes; a "context" tier records the prompt
	// size above which the model bills at a higher rate.
	Tiers []costTier `json:"tiers"`
}

type costTier struct {
	Tier struct {
		Type string `json:"type"`
		Size int    `json:"size"`
	} `json:"tier"`
}

func (c Cost) isZero() bool {
	return c.Input == 0 && c.Output == 0 && c.CacheRead == 0 && c.CacheWrite == 0
}

type providerEntry struct {
	// ID is the stable models.dev provider key. It is populated after decoding
	// and used to make same-host path ties deterministic.
	ID string `json:"-"`
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
		for id, p := range parsed {
			p.ID = id
			parsed[id] = p
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

// LookupInterleavedReasoningField returns the named assistant-message field
// when models.dev records one for this model.
func LookupInterleavedReasoningField(endpoint, modelName string) (string, bool) {
	m, found := lookupInterleaved(endpoint, modelName)
	return m.Interleaved.Field, found && m.Interleaved.Supported && m.Interleaved.Field != ""
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

// LookupAnthropicOutputLimit reports the positive output limit for a model
// sent through the Anthropic Messages API. It first uses the endpoint's
// catalog, so a provider-specific model never inherits another provider's
// limit. If that endpoint is unknown, or does not list the model, it accepts
// only an exact (or date-suffix alias) match in the canonical Anthropic
// catalog. This supports Claude models routed through integration gateways
// without treating arbitrary cost-style metadata as an Anthropic limit.
func LookupAnthropicOutputLimit(endpoint, modelName string) (int, bool) {
	data := load()
	names := modelNames(modelName)
	if host := hostOf(endpoint); host != "" {
		for _, name := range names {
			if p, ok := bestProviderForPath(hostIndex[host], pathSegments(endpoint), name); ok {
				if m, ok := lookupInProvider(p, name); ok && m.Limit.Output > 0 {
					return m.Limit.Output, true
				}
			}
		}
	}
	p, ok := data["anthropic"]
	if !ok {
		return 0, false
	}
	for _, name := range names {
		if m, ok := lookupInProvider(p, name); ok && m.Limit.Output > 0 {
			return m.Limit.Output, true
		}
	}
	return 0, false
}

// LookupOutputLimit reports the positive models.dev output limit for a model.
// Endpoint matches take precedence; gateway hosts can then fall back to a
// first-party catalog by model name.
func LookupOutputLimit(endpoint, modelName string) (int, bool) {
	m, found := lookupBroad(endpoint, modelName, func(m modelEntry) bool { return m.Limit.Output > 0 })
	return m.Limit.Output, found
}

// LookupContextLimit reports the models.dev context window for a model,
// clamped to the smallest "context" pricing tier when one exists. The tier is
// a pricing cliff (e.g. gpt-5.6 lists a 1,050,000 window but doubles its price
// past 272,000), not a hard limit; the UI uses this value as the denominator of
// its context usage readout, so the clamp keeps the readout honest about where
// a conversation gets expensive.
func LookupContextLimit(endpoint, modelName string) (int, bool) {
	m, found := lookupBroad(endpoint, modelName, func(m modelEntry) bool { return m.Limit.Context > 0 })
	if !found {
		return 0, false
	}
	limit := m.Limit.Context
	for _, t := range m.Cost.Tiers {
		if t.Tier.Type == "context" && t.Tier.Size > 0 && t.Tier.Size < limit {
			limit = t.Tier.Size
		}
	}
	return limit, true
}

func lookupInterleaved(endpoint, modelName string) (modelEntry, bool) {
	data := load()
	names := modelNames(modelName)
	endpointProviders := hostIndex[hostOf(endpoint)]
	endpointSegs := pathSegments(endpoint)

	// A full ID identifies the model more precisely than a trailing path
	// segment, regardless of which catalog contains it.
	for _, name := range names {
		if p, ok := bestProviderForPathMatching(endpointProviders, endpointSegs, name, lookupExactInProvider); ok {
			return lookupExactInProvider(p, name)
		}
		if m, ok := lookupInterleavedAcross(data, name, lookupExactInProvider); ok {
			return m, true
		}
	}

	// Once no catalog has a full-ID match, prefer the endpoint's own catalog.
	for _, name := range names {
		if p, ok := bestProviderForPathMatching(endpointProviders, endpointSegs, name, lookupTailInProvider); ok {
			return lookupTailInProvider(p, name)
		}
	}

	// Fireworks' public shorthand is an established alias for its native
	// accounts/fireworks/models IDs, rather than a generic vendor slug.
	for _, name := range names {
		if strings.HasPrefix(strings.ToLower(name), "fireworks/") {
			if p, ok := data["fireworks-ai"]; ok {
				if m, ok := lookupTailInProvider(p, name); ok {
					return m, true
				}
			}
		}
	}

	// Bare tail matches are useful for gateways, but only when every matching
	// fallback catalog agrees on the interleaved capability and field.
	for _, name := range names {
		if m, ok := lookupInterleavedAcross(data, name, lookupTailInProvider); ok {
			return m, true
		}
	}
	return modelEntry{}, false
}

func lookupInterleavedAcross(data map[string]providerEntry, modelName string, match func(providerEntry, string) (modelEntry, bool)) (modelEntry, bool) {
	providers := append(append([]string(nil), firstPartyProviders...), "openrouter")
	var result modelEntry
	found := false
	for _, provider := range providers {
		p, ok := data[provider]
		if !ok {
			continue
		}
		m, ok := match(p, modelName)
		if !ok {
			continue
		}
		if found && m.Interleaved != result.Interleaved {
			return modelEntry{}, false
		}
		result = m
		found = true
	}
	return result, found
}

// lookupBroad resolves models that may be reached through gateway hosts absent
// from models.dev: endpoint host first, then first-party catalogs by bare name,
// then OpenRouter. Each path also tries a trailing date suffix stripped.
func lookupBroad(endpoint, modelName string, usable func(modelEntry) bool) (modelEntry, bool) {
	data := load()
	names := modelNames(modelName)
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

func modelNames(modelName string) []string {
	names := []string{modelName}
	if stripped := dateSuffixRe.ReplaceAllString(modelName, ""); stripped != modelName {
		names = append(names, stripped)
	}
	return names
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
	return bestProviderForPathMatching(providers, endpointSegs, modelName, lookupInProvider)
}

func bestProviderForPathMatching(providers []providerEntry, endpointSegs []string, modelName string, match func(providerEntry, string) (modelEntry, bool)) (providerEntry, bool) {
	var best providerEntry
	bestScore := -1
	found := false
	for _, p := range providers {
		if _, hit := match(p, modelName); !hit {
			continue
		}
		score := commonPrefixLen(pathSegments(p.API), endpointSegs)
		if score > bestScore || score == bestScore && providerStableKey(p) < providerStableKey(best) {
			best, bestScore, found = p, score, true
		}
	}
	return best, found
}

func providerStableKey(p providerEntry) string {
	return p.ID + "\x00" + p.API
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
	if m, ok := lookupExactInProvider(p, modelName); ok {
		return m, true
	}
	return lookupTailInProvider(p, modelName)
}

func lookupExactInProvider(p providerEntry, modelName string) (modelEntry, bool) {
	if m, ok := p.Models[modelName]; ok {
		return m, true
	}
	lower := strings.ToLower(modelName)
	for _, id := range sortedModelIDs(p) {
		if strings.ToLower(id) == lower {
			return p.Models[id], true
		}
	}
	return modelEntry{}, false
}

func lookupTailInProvider(p providerEntry, modelName string) (modelEntry, bool) {
	// Match the final path segment on both sides (e.g. "glm-5p2" or
	// "fireworks/glm-5p2" -> "accounts/fireworks/models/glm-5p2").
	// Refuse ambiguous matches instead of depending on map iteration order.
	tail := modelName
	if i := strings.LastIndex(modelName, "/"); i >= 0 && i+1 < len(modelName) {
		tail = modelName[i+1:]
	}
	if tail == "" {
		return modelEntry{}, false
	}
	if m, ok := p.Models[tail]; ok {
		return m, true
	}
	tailLower := strings.ToLower(tail)
	ids := sortedModelIDs(p)
	for _, id := range ids {
		if strings.ToLower(id) == tailLower {
			return p.Models[id], true
		}
	}
	var match modelEntry
	found := false
	for _, id := range ids {
		idTail := id
		if j := strings.LastIndex(id, "/"); j >= 0 && j+1 < len(id) {
			idTail = id[j+1:]
		}
		if strings.ToLower(idTail) != tailLower {
			continue
		}
		if found {
			return modelEntry{}, false
		}
		match = p.Models[id]
		found = true
	}
	return match, found
}

func sortedModelIDs(p providerEntry) []string {
	ids := make([]string, 0, len(p.Models))
	for id := range p.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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
