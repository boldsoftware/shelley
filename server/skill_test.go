package server

import "testing"

func TestParseSkillPreamble(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name: "name and inline description",
			content: `---
name: test-skill
description: A simple test skill
---
# Test Skill
`,
			expected: "test-skill: A simple test skill",
		},
		{
			name: "multiline description with >",
			content: `---
name: pdf-processor
description: >
  Process PDF files including extraction,
  form filling, and merging.
---
# PDF Processor
`,
			expected: "pdf-processor: Process PDF files including extraction, form filling, and merging.",
		},
		{
			name: "multiline description with |",
			content: `---
name: code-reviewer
description: |
  Reviews code for quality.
  Checks for common issues.
---
`,
			expected: "code-reviewer: Reviews code for quality. Checks for common issues.",
		},
		{
			name: "name only",
			content: `---
name: minimal-skill
---
# Minimal
`,
			expected: "minimal-skill",
		},
		{
			name: "no name",
			content: `---
description: A skill without a name
---
`,
			expected: "",
		},
		{
			name:     "no frontmatter",
			content:  "# Just a markdown file\n",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSkillPreamble(tt.content)
			if got != tt.expected {
				t.Errorf("parseSkillPreamble() = %q, want %q", got, tt.expected)
			}
		})
	}
}
