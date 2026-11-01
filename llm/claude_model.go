package llm

import (
	"strconv"
	"strings"
)

// ParseClaudeModel extracts the family ("opus", "sonnet", "haiku", "fable")
// and version from a Claude model identifier. It handles bare names
// ("claude-opus-5"), dotted versions ("claude-opus-4.8"), dated snapshots
// ("claude-opus-4-8-20260115"), and provider-qualified names
// ("us.anthropic.claude-opus-4-8-v1:0", "claude-opus-5@20260724"). Returns
// ok=false for strings that don't follow the claude-<family>-<version> shape,
// such as the legacy version-first names ("claude-3-opus-20240229").
func ParseClaudeModel(model string) (family string, major, minor int, ok bool) {
	tokens := strings.FieldsFunc(strings.ToLower(model), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	seenClaude := false
	for i, tok := range tokens {
		if tok == "claude" {
			seenClaude = true
			continue
		}
		if !seenClaude {
			continue
		}
		if tok != "opus" && tok != "sonnet" && tok != "haiku" && tok != "fable" {
			continue
		}
		// Parse up to two numeric tokens after the family as major/minor.
		// Date snapshots (20260115) are large; version components are small.
		var nums []int
		for _, t := range tokens[i+1:] {
			n, err := strconv.Atoi(t)
			if err != nil || n >= 1000 {
				break
			}
			nums = append(nums, n)
			if len(nums) == 2 {
				break
			}
		}
		if len(nums) == 0 {
			return "", 0, 0, false
		}
		if len(nums) > 1 {
			minor = nums[1]
		}
		return tok, nums[0], minor, true
	}
	return "", 0, 0, false
}
