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

func TestDefaultDirsReturnsExistingCandidates(t *testing.T) {
	// Create a fake home directory with skill directories
	tmpHome := t.TempDir()

	// Create all three candidate directories
	configShelley := filepath.Join(tmpHome, ".config", "shelley")
	configAgents := filepath.Join(tmpHome, ".config", "agents", "skills")
	dotShelley := filepath.Join(tmpHome, ".shelley")

	for _, dir := range []string{configShelley, configAgents, dotShelley} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Override HOME
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	dirs := DefaultDirs()

	if len(dirs) != 3 {
		t.Fatalf("expected 3 dirs, got %d: %v", len(dirs), dirs)
	}

	// Verify all three candidates are returned
	want := map[string]bool{
		configShelley: true,
		configAgents:  true,
		dotShelley:    true,
	}
	for _, d := range dirs {
		if !want[d] {
			t.Errorf("unexpected dir in result: %s", d)
		}
	}
}

func TestDefaultDirsSkipsMissingDirs(t *testing.T) {
	tmpHome := t.TempDir()

	// Only create one of the candidate directories
	configAgents := filepath.Join(tmpHome, ".config", "agents", "skills")
	if err := os.MkdirAll(configAgents, 0o755); err != nil {
		t.Fatal(err)
	}

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	dirs := DefaultDirs()

	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir, got %d: %v", len(dirs), dirs)
	}
	if dirs[0] != configAgents {
		t.Errorf("expected %s, got %s", configAgents, dirs[0])
	}
}

func TestSkillsFoundRegardlessOfWorkingDir(t *testing.T) {
	// This is a regression test for:
	// https://github.com/boldsoftware/shelley/issues/83
	//
	// Skills from ~/.config/agents/skills should be discovered
	// regardless of the current working directory.

	tmpHome := t.TempDir()

	// Create a skill in ~/.config/agents/skills/
	skillDir := filepath.Join(tmpHome, ".config", "agents", "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: A test skill.\n---\nContent\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	// Create a project directory far from home
	projectDir := t.TempDir()

	// Simulate what collectSkills does:
	// DefaultDirs + Discover should find the skill regardless of project dir
	dirs := DefaultDirs()
	found := Discover(dirs)

	if len(found) != 1 {
		t.Fatalf("expected 1 skill, got %d (dirs=%v)", len(found), dirs)
	}
	if found[0].Name != "my-skill" {
		t.Errorf("expected my-skill, got %s", found[0].Name)
	}

	// DiscoverInTree from the project dir should NOT find user-level skills
	// (they're in hidden directories which are skipped)
	treeSkills := DiscoverInTree(projectDir, projectDir)
	if len(treeSkills) != 0 {
		t.Errorf("expected 0 tree skills from unrelated project, got %d", len(treeSkills))
	}

	// But the combined result should still have the skill
	all := append(found, treeSkills...)
	if len(all) != 1 {
		t.Fatalf("expected 1 total skill, got %d", len(all))
	}

	_ = projectDir // used above
}

func TestDiscoverFollowsSymlinks(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a real skill directory (the symlink target)
	realSkillDir := filepath.Join(tmpDir, "real-skills", "my-skill")
	if err := os.MkdirAll(realSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillContent := `---
name: my-skill
description: A symlinked skill.
---

Test instructions.
`
	if err := os.WriteFile(filepath.Join(realSkillDir, "SKILL.md"), []byte(skillContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Directory containing only a symlink to the skill
	symlinkParent := filepath.Join(tmpDir, "symlinked-skills")
	if err := os.MkdirAll(symlinkParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSkillDir, filepath.Join(symlinkParent, "my-skill")); err != nil {
		t.Fatal(err)
	}

	// A broken symlink should be silently skipped
	if err := os.Symlink(filepath.Join(tmpDir, "nonexistent"), filepath.Join(symlinkParent, "broken-skill")); err != nil {
		t.Fatal(err)
	}

	skills := Discover([]string{symlinkParent})

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill via symlink, got %d", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("skill name = %q, want %q", skills[0].Name, "my-skill")
	}
}
