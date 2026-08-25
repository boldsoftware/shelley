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
	{Better: "claude-opus-5", Worse: "claude-opus-4.8"},
	{Better: "claude-opus-5", Worse: "claude-opus-4.7"},
	{Better: "claude-opus-4.8", Worse: "claude-opus-4.7"},
	{Better: "claude-opus-4.6", Worse: "claude-opus-4.5"},
	{Better: "claude-sonnet-5", Worse: "claude-sonnet-4.6"},
	{Better: "claude-sonnet-5", Worse: "claude-sonnet-4.5"},
	{Better: "gpt-5.6-sol", Worse: "gpt-5.5"},
	{Better: "gpt-5.6-sol", Worse: "gpt-5.4"},
	{Better: "gpt-5.6-terra", Worse: "gpt-5.4-mini"},
	{Better: "gpt-5.6-luna", Worse: "gpt-5.4-nano"},
	{Better: "kimi-k2.7-code-fireworks", Worse: "kimi-k2.6-fireworks"},
	{Better: "kimi-k3-fireworks", Worse: "kimi-k2.7-code-fireworks"},
	{Better: "kimi-k3-fireworks", Worse: "kimi-k2.6-fireworks"},

	// Arguable / cross-family supersessions. We still encode them so the
	// default list stays lean; the reasoning is noted inline.
	{Better: "claude-opus-4.8", Worse: "claude-opus-4.6"}, // 4.6 is cheaper (older tokenizer) but 4.8 is stronger
	{Better: "claude-opus-5", Worse: "claude-opus-4.6"},   // same reasoning as 4.8 vs 4.6
	// Sonnet 5 is cheap per token but not per task: it takes many more turns
	// and emits far more output than its siblings, which puts its cost per
	// finished task above opus-4.8's. Its one genuine strength is grinding
	// autonomously toward an objective finish line, which is not what a
	// subagent dispatched on a scoped task is doing — and the subagent tool
	// has no way to hand it a turn budget or a stopping criterion.
	{Better: "claude-opus-4.8", Worse: "claude-sonnet-5"},
	{Better: "claude-opus-5", Worse: "claude-sonnet-5"},              // in case opus-4.8 isn't served
	{Better: "gpt-5.6-terra", Worse: "claude-sonnet-5"},              // cheaper and stronger, and it doesn't run away with fan-out work
	{Better: "glm-5.2-fireworks", Worse: "kimi-k2.7-code-fireworks"}, // different families; glm costs a bit more; kimi-k3 costs far more, so it doesn't shadow glm
	{Better: "glm-5.2-fireworks", Worse: "deepseek-v4-flash-0731-fireworks"},
	{Better: "gpt-5.6-luna", Worse: "claude-haiku-4.5"},
	{Better: "gpt-5.6-luna", Worse: "gpt-5.3-codex"},
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
