package models

import (
	"strings"

	"shelley.exe.dev/models/modelsdev"
)

// UpgradeKind says why one model points at another.
type UpgradeKind string

const (
	// UpgradeVersion: same lineage, newer version.
	UpgradeVersion UpgradeKind = "version"
	// UpgradeGeneration: the provider renamed its family; the target is the
	// flagship model of the new set.
	UpgradeGeneration UpgradeKind = "generation"
)

// generationEdges are the hand-written hops out of the old GPT-5.x names,
// which carry no size in their id. Everything else is derived from shadowPairs.
var generationEdges = map[string]string{
	"gpt-5.5":       "gpt-6-sol",
	"gpt-5.4":       "gpt-6-sol",
	"gpt-5.3-codex": "gpt-6-sol",
	"gpt-5.4-mini":  "gpt-6.1-terra",
	"gpt-5.4-nano":  "gpt-6-luna",
}

// lineTokens are an id's name parts with version-like ones (4.7, v4.1, 1017)
// dropped: claude-opus-4.7 -> [claude opus], deepseek-v4.1-flash-1017-fireworks
// -> [deepseek flash fireworks].
func lineTokens(id string) []string {
	var out []string
	for _, t := range strings.Split(id, "-") {
		if t != "" && (t[0] < '0' || t[0] > '9') && !(t[0] == 'v' && len(t) > 1 && t[1] >= '0' && t[1] <= '9') {
			out = append(out, t)
		}
	}
	return out
}

// Line is the model family an id belongs to, e.g. "claude-opus" or
// "gpt-sol": successive versions of the same thing share it.
func Line(id string) string { return lineOf(id) }

// lineOf is the model class: two ids with the same line are successive
// versions of the same thing.
func lineOf(id string) string { return strings.Join(lineTokens(id), "-") }

// brandNames keep a line's spelling when the id's first token is a brand.
var brandNames = map[string]string{
	"deepseek": "DeepSeek",
	"glm":      "GLM",
	"grok":     "Grok",
	"kimi":     "Kimi",
	"minimax":  "MiniMax",
	"qwen":     "Qwen",
}

// lineName is the line as shown to the user, e.g. "Opus", "DeepSeek Flash".
func lineName(id string, provider Provider) string {
	var words []string
	for _, t := range lineTokens(id) {
		if t == "claude" || t == "gpt" || t == string(provider) {
			continue
		}
		if b, ok := brandNames[t]; ok {
			words = append(words, b)
			continue
		}
		words = append(words, strings.ToUpper(t[:1])+t[1:])
	}
	return strings.Join(words, " ")
}

// providerNames spell the provider family as a banner title wants it.
var providerNames = map[Provider]string{
	ProviderOpenAI:    "OpenAI",
	ProviderAnthropic: "Anthropic",
	ProviderFireworks: "Fireworks",
	ProviderBuiltIn:   "Shelley",
}

func providerName(p Provider) string {
	if n, ok := providerNames[p]; ok {
		return n
	}
	return string(p)
}

func releaseDate(id string) string {
	m := ByID(id)
	if m == nil {
		return ""
	}
	date, _ := modelsdev.LookupReleaseDate("", m.APIModelName)
	return date
}

// BestUpgrade picks the newest ready model that shadowPairs says beats
// `from`. Candidates share the provider, and the line too unless the id
// carries no line of its own: the old GPT-5.x names are matched by provider
// and reach the GPT-6 set through the hand-written generationEdges hop. The
// newest models.dev release date wins; an undated candidate only wins when
// nothing dated is on offer.
func BestUpgrade(from string, ready map[string]bool) (to string, kind UpgradeKind, line string) {
	m := ByID(from)
	if m == nil {
		return "", "", ""
	}
	kind = UpgradeVersion
	seed := from
	if hop, ok := generationEdges[from]; ok {
		kind, seed = UpgradeGeneration, hop
	}
	sameLine := seed == from
	best, bestDate := "", ""
	consider := func(id string) {
		if id == from || !ready[id] {
			return
		}
		d := releaseDate(id)
		if best != "" && (d == "" || (bestDate != "" && d <= bestDate)) {
			return
		}
		best, bestDate = id, d
	}
	consider(seed)
	// Walk "better than" links, so one new pair extends the whole chain.
	seen := map[string]bool{from: true, seed: true}
	for queue := []string{from, seed}; len(queue) > 0; queue = queue[1:] {
		for _, p := range shadowPairs {
			b := ByID(p.Better)
			if p.Worse != queue[0] || seen[p.Better] || b == nil || b.Provider != m.Provider {
				continue
			}
			if sameLine && lineOf(p.Better) != lineOf(from) {
				continue
			}
			seen[p.Better] = true
			queue = append(queue, p.Better)
			consider(p.Better)
		}
	}
	if best == "" {
		return "", "", ""
	}
	if kind == UpgradeGeneration {
		return best, kind, providerName(m.Provider)
	}
	return best, kind, lineName(best, m.Provider)
}
