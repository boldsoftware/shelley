package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantName  string
		wantDesc  string
		wantError bool
	}{
		{
			name: "basic skill",
			content: `---
name: pdf-processing
description: Extract text and tables from PDF files.
---

Instructions here.
`,
			wantName:  "pdf-processing",
			wantDesc:  "Extract text and tables from PDF files.",
			wantError: false,
		},
		{
			name: "with metadata",
			content: `---
name: data-analysis
description: Analyzes datasets and generates reports.
license: MIT
metadata:
  author: example-org
  version: "1.0"
---

Body content.
`,
			wantName:  "data-analysis",
			wantDesc:  "Analyzes datasets and generates reports.",
			wantError: false,
		},
		{
			name:      "missing frontmatter",
			content:   "# Just markdown\n\nNo frontmatter here.",
			wantError: true,
		},
		{
			name: "missing name",
			content: `---
description: A skill without a name
---
`,
			wantError: true,
		},
		{
			name: "invalid name - uppercase",
			content: `---
name: PDF-Processing
description: A skill with uppercase name
---
`,
			wantError: true,
		},
		{
			name: "invalid name - starts with hyphen",
			content: `---
name: -pdf
description: A skill starting with hyphen
---
`,
			wantError: true,
		},
		{
			name: "invalid name - consecutive hyphens",
			content: `---
name: pdf--processing
description: A skill with consecutive hyphens
---
`,
			wantError: true,
		},
		{
			name: "quoted values",
			content: `---
name: "my-skill"
description: 'A skill with quoted values'
---
`,
			wantName:  "my-skill",
			wantDesc:  "A skill with quoted values",
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, "SKILL.md")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("failed to write test file: %v", err)
			}

			skill, err := Parse(path)
			if tt.wantError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if skill.Name != tt.wantName {
				t.Errorf("name = %q, want %q", skill.Name, tt.wantName)
			}

			if skill.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", skill.Description, tt.wantDesc)
			}
		})
	}
}

func TestDiscover(t *testing.T) {
	// Create a temp directory structure
	tmpDir := t.TempDir()

	// Create a valid skill
	skillDir := filepath.Join(tmpDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillContent := `---
name: my-skill
description: A test skill for discovery.
---

Test instructions.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create an invalid skill (name doesn't match directory)
	badSkillDir := filepath.Join(tmpDir, "bad-skill")
	if err := os.MkdirAll(badSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	badSkillContent := `---
name: different-name
description: Name doesn't match directory.
---
`
	if err := os.WriteFile(filepath.Join(badSkillDir, "SKILL.md"), []byte(badSkillContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a directory without SKILL.md
	emptyDir := filepath.Join(tmpDir, "empty-skill")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	skills := Discover([]string{tmpDir})

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}

	if skills[0].Name != "my-skill" {
		t.Errorf("skill name = %q, want %q", skills[0].Name, "my-skill")
	}
}

func TestDiscoverPrecedence(t *testing.T) {
	// Test that skills from earlier directories take precedence over later ones
	// This simulates shelley-specific skills overriding shared agent skills
	dir1 := t.TempDir() // Higher precedence (e.g., ~/.config/shelley)
	dir2 := t.TempDir() // Lower precedence (e.g., ~/.config/agents/skills)

	// Create same-named skill in both directories with different descriptions
	skillContent1 := `---
name: my-skill
description: Shelley-specific version.
---
`
	skillContent2 := `---
name: my-skill
description: Shared agent version.
---
`

	skillDir1 := filepath.Join(dir1, "my-skill")
	skillDir2 := filepath.Join(dir2, "my-skill")

	if err := os.MkdirAll(skillDir1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(skillDir2, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(skillDir1, "SKILL.md"), []byte(skillContent1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir2, "SKILL.md"), []byte(skillContent2), 0o644); err != nil {
		t.Fatal(err)
	}

	// Discover with dir1 first (higher precedence)
	skills := Discover([]string{dir1, dir2})

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill (duplicate by name should be skipped), got %d", len(skills))
	}

	if skills[0].Description != "Shelley-specific version." {
		t.Errorf("expected shelley-specific skill to win, got description: %q", skills[0].Description)
	}

	// Also test unique skills from both directories are included
	uniqueContent := `---
name: unique-skill
description: Only in shared directory.
---
`
	uniqueDir := filepath.Join(dir2, "unique-skill")
	if err := os.MkdirAll(uniqueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uniqueDir, "SKILL.md"), []byte(uniqueContent), 0o644); err != nil {
		t.Fatal(err)
	}

	skills = Discover([]string{dir1, dir2})
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
}

func TestToPromptXML(t *testing.T) {
	skills := []Skill{
		{
			Name:        "pdf-processing",
			Description: "Extract text & tables from PDF files.",
			Path:        "/home/user/.shelley/skills/pdf-processing/SKILL.md",
		},
		{
			Name:        "data-analysis",
			Description: "Analyze datasets and generate reports.",
			Path:        "/home/user/.shelley/skills/data-analysis/SKILL.md",
		},
	}

	xml := ToPromptXML(skills)

	// Check that it contains expected elements
	expected := []string{
		"<available_skills>",
		"</available_skills>",
		"<skill>",
		"</skill>",
		"<name>pdf-processing</name>",
		"<description>Extract text &amp; tables from PDF files.</description>",
		"<location>/home/user/.shelley/skills/pdf-processing/SKILL.md</location>",
		"<name>data-analysis</name>",
	}

	for _, s := range expected {
		if !contains(xml, s) {
			t.Errorf("expected XML to contain %q", s)
		}
	}
}

func TestToPromptXMLEmpty(t *testing.T) {
	xml := ToPromptXML(nil)
	if xml != "" {
		t.Errorf("expected empty string for nil skills, got %q", xml)
	}

	xml = ToPromptXML([]Skill{})
	if xml != "" {
		t.Errorf("expected empty string for empty skills, got %q", xml)
	}
}

func TestValidateName(t *testing.T) {
	validNames := []string{
		"a",
		"pdf-processing",
		"data-analysis",
		"code-review",
		"my-skill-123",
		"skill",
	}

	for _, name := range validNames {
		if err := validateName(name); err != nil {
			t.Errorf("validateName(%q) returned error: %v", name, err)
		}
	}

	invalidNames := []string{
		"",
		"PDF-Processing",
		"-pdf",
		"pdf-",
		"pdf--processing",
		"pdf processing",
		"pdf_processing",
		"pdf.processing",
	}

	for _, name := range invalidNames {
		if err := validateName(name); err == nil {
			t.Errorf("validateName(%q) should return error", name)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestDiscoverInTree(t *testing.T) {
	// Create a directory structure:
	// tmpDir/
	//   skill-root/
	//     SKILL.md
	//   subdir/
	//     nested/
	//       skill-nested/
	//         SKILL.md
	//   .hidden/
	//     skill-hidden/
	//       SKILL.md  (should be skipped)
	//   node_modules/
	//     skill-nm/
	//       SKILL.md  (should be skipped)

	tmpDir := t.TempDir()

	// Create root-level skill
	rootSkillDir := filepath.Join(tmpDir, "skill-root")
	if err := os.MkdirAll(rootSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootSkillDir, "SKILL.md"), []byte("---\nname: skill-root\ndescription: Root skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create nested skill
	nestedSkillDir := filepath.Join(tmpDir, "subdir", "nested", "skill-nested")
	if err := os.MkdirAll(nestedSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedSkillDir, "SKILL.md"), []byte("---\nname: skill-nested\ndescription: Nested skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create hidden directory skill (should be skipped)
	hiddenSkillDir := filepath.Join(tmpDir, ".hidden", "skill-hidden")
	if err := os.MkdirAll(hiddenSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hiddenSkillDir, "SKILL.md"), []byte("---\nname: skill-hidden\ndescription: Hidden skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create node_modules skill (should be skipped)
	nmSkillDir := filepath.Join(tmpDir, "node_modules", "skill-nm")
	if err := os.MkdirAll(nmSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nmSkillDir, "SKILL.md"), []byte("---\nname: skill-nm\ndescription: Node modules skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Test with git root
	skills := DiscoverInTree(tmpDir, tmpDir)

	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d: %v", len(skills), skillNames(skills))
	}

	// Check we found the right skills
	names := make(map[string]bool)
	for _, s := range skills {
		names[s.Name] = true
	}

	if !names["skill-root"] {
		t.Error("expected to find skill-root")
	}
	if !names["skill-nested"] {
		t.Error("expected to find skill-nested")
	}
	if names["skill-hidden"] {
		t.Error("should not find skill-hidden (in hidden directory)")
	}
	if names["skill-nm"] {
		t.Error("should not find skill-nm (in node_modules)")
	}
}

func TestDiscoverInTreeNoGitRoot(t *testing.T) {
	// When no git root, should search from working dir
	tmpDir := t.TempDir()

	// Create a skill
	skillDir := filepath.Join(tmpDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: Test skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Test with empty git root
	skills := DiscoverInTree(tmpDir, "")

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("expected my-skill, got %s", skills[0].Name)
	}
}

func skillNames(skills []Skill) []string {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return names
}

func TestProjectSkillsDirs(t *testing.T) {
	// Create a directory structure:
	// tmpDir/
	//   .skills/
	//     skill-a/
	//       SKILL.md
	//   subdir/
	//     .skills/
	//       skill-b/
	//         SKILL.md
	//     deeper/
	//       (working dir)

	tmpDir := t.TempDir()

	// Create root .skills
	rootSkillsDir := filepath.Join(tmpDir, ".skills", "skill-a")
	if err := os.MkdirAll(rootSkillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootSkillsDir, "SKILL.md"), []byte("---\nname: skill-a\ndescription: Skill A\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create subdir .skills
	subSkillsDir := filepath.Join(tmpDir, "subdir", ".skills", "skill-b")
	if err := os.MkdirAll(subSkillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subSkillsDir, "SKILL.md"), []byte("---\nname: skill-b\ndescription: Skill B\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create deeper working directory
	workingDir := filepath.Join(tmpDir, "subdir", "deeper")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Test walking up from working dir to git root (tmpDir)
	dirs := ProjectSkillsDirs(workingDir, tmpDir)

	// Should find both .skills directories
	if len(dirs) != 2 {
		t.Fatalf("expected 2 .skills dirs, got %d: %v", len(dirs), dirs)
	}

	// subdir/.skills should come first (closer to working dir)
	expectedFirst := filepath.Join(tmpDir, "subdir", ".skills")
	expectedSecond := filepath.Join(tmpDir, ".skills")

	if dirs[0] != expectedFirst {
		t.Errorf("first dir = %q, want %q", dirs[0], expectedFirst)
	}
	if dirs[1] != expectedSecond {
		t.Errorf("second dir = %q, want %q", dirs[1], expectedSecond)
	}
}
