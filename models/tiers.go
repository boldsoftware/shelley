package models

// --- Model tiers -----------------------------------------------------------
//
// The catalog keeps growing, and many models are strictly obviated by a newer
// or better sibling that we also offer. Rather than drop the older models
// (people may still want them, and custom configs may pin them), we sort them
// into two tiers:
//
//   - Tier 1: the models worth surfacing prominently. Nothing we also offer
//     clearly supersedes them.
//   - Tier 2: models that are "overshadowed" by another model in the same
//     available set, or models not present in Shelley's built-in catalog. Still
//     selectable, just tucked behind a "more models" affordance in the UI and
//     omitted from the subagent tool's model enum.
//
// The shadow relationships below are hand-curated. Each pair reads
// "better shadows worse": if `better` is present in the available set, then
// `worse` is demoted to tier 2. A known model that is never shadowed by an
// available model stays in tier 1. Unknown integration models default to tier 2.

const (
	Tier1 = 1
	Tier2 = 2
)

// shadowPair records that model `Better` overshadows model `Worse`: when both
// are available, `Worse` drops to tier 2.
type shadowPair struct {
	Better string
	Worse  string
}

// shadowPairs is the curated list of "better shadows worse" relationships.
// See the package doc above for the semantics. IDs that aren't in the catalog
// (e.g. a not-yet-launched release) are harmless: they simply never match an
// available model.
var shadowPairs = []shadowPair{
	// Clear supersessions within a family.
	{Better: "claude-opus-4.8", Worse: "claude-opus-4.7"},
	{Better: "claude-opus-4.6", Worse: "claude-opus-4.5"},
	{Better: "claude-sonnet-5", Worse: "claude-sonnet-4.6"},
	{Better: "claude-sonnet-5", Worse: "claude-sonnet-4.5"},
	{Better: "gpt-5.6-sol", Worse: "gpt-5.5"},
	{Better: "gpt-5.6-sol", Worse: "gpt-5.4"},
	{Better: "glm-5.2-fireworks", Worse: "glm-5.1-fireworks"},
	{Better: "gpt-5.6-terra", Worse: "gpt-5.4-mini"},
	{Better: "gpt-5.6-luna", Worse: "gpt-5.4-nano"},
	{Better: "kimi-k2.7-code-fireworks", Worse: "kimi-k2.6-fireworks"},

	// Arguable / cross-family supersessions. We still encode them so the
	// default list stays lean; the reasoning is noted inline.
	{Better: "claude-opus-4.8", Worse: "claude-opus-4.6"},            // 4.6 is cheaper (older tokenizer) but 4.8 is stronger
	{Better: "glm-5.2-fireworks", Worse: "qwen3.7-plus-fireworks"},   // different families; glm costs a bit more
	{Better: "glm-5.2-fireworks", Worse: "kimi-k2.7-code-fireworks"}, // different families; glm costs a bit more; kimi-k3 likely soon
	{Better: "glm-5.2-fireworks", Worse: "deepseek-v4-flash-fireworks"},
	{Better: "gpt-5.6-luna", Worse: "claude-haiku-4.5"},
	{Better: "gpt-5.6-luna", Worse: "gpt-5.3-codex"},
	{Better: "gpt-5.6-luna", Worse: "gpt-5.2-codex"},
}

// AssignTiers computes the tier (Tier1 or Tier2) for each of the given model
// IDs. A model is Tier2 when it is outside Shelley's built-in catalog or when
// some other available model shadows it (per shadowPairs); otherwise it is
// Tier1. The result maps every input ID to a tier, so callers can look up any
// model they know about. Callers promote explicitly configured custom models
// back to Tier1.
func AssignTiers(availableIDs []string) map[string]int {
	available := make(map[string]bool, len(availableIDs))
	for _, id := range availableIDs {
		available[id] = true
	}
	tiers := make(map[string]int, len(availableIDs))
	for _, id := range availableIDs {
		if ByID(id) == nil {
			tiers[id] = Tier2
		} else {
			tiers[id] = Tier1
		}
	}
	for _, p := range shadowPairs {
		if available[p.Better] && available[p.Worse] {
			tiers[p.Worse] = Tier2
		}
	}
	return tiers
}
