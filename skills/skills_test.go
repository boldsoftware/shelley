package skills

import (
	"os"
	"path/filepath"
	"strings"
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

	expected := []string{
		"<available_skills>",
		"</available_skills>",
		"<skill>",
		"</skill>",
		"<name>pdf-processing</name>",
		"<description>Extract text &amp; tables from PDF files.</description>",
		"<activate>shelley skill cat pdf-processing</activate>",
		"<activate>shelley skill cat data-analysis</activate>",
		"<name>data-analysis</name>",
	}

	for _, s := range expected {
		if !contains(xml, s) {
			t.Errorf("expected XML to contain %q", s)
		}
	}

	// Should not contain filesystem paths
	if contains(xml, "/home/user") {
		t.Error("XML should use uniform 'shelley skill cat' commands, not file paths")
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
	skills, _ := DiscoverInTree(tmpDir, tmpDir)

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

	// Create a valid skill
	skillDir := filepath.Join(tmpDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: Test skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create an empty skill (suppresses builtins but won't parse)
	emptyDir := filepath.Join(tmpDir, "suppressed")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(emptyDir, "SKILL.md"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	// Test with empty git root
	skills, names := DiscoverInTree(tmpDir, "")

	if len(skills) != 1 {
		t.Fatalf("expected 1 parsed skill, got %d", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("expected my-skill, got %s", skills[0].Name)
	}

	// names should include both valid and empty skills
	if !names["my-skill"] {
		t.Error("expected names to include my-skill")
	}
	if !names["suppressed"] {
		t.Error("expected names to include suppressed (empty SKILL.md)")
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
	treeSkills, _ := DiscoverInTree(projectDir, projectDir)
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

func TestBuiltinSkills(t *testing.T) {
	builtins := BuiltinSkills()
	if len(builtins) != 5 {
		t.Fatalf("expected exactly 5 built-in skills, got %d: %v", len(builtins), skillNames(builtins))
	}

	wantSkills := []string{"install-node", "previous-conversations", "reflection-integration", "schedule", "shelley-hooks"}
	for _, wantName := range wantSkills {
		var found *Skill
		for i := range builtins {
			if builtins[i].Name == wantName {
				found = &builtins[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("expected built-in %q skill", wantName)
		}
		if found.Description == "" {
			t.Errorf("%s skill has empty description", wantName)
		}
		if found.Body == "" {
			t.Errorf("%s skill has empty body", wantName)
		}
		if found.Path != "" {
			t.Errorf("%s: built-in skill should have empty Path, got %q", wantName, found.Path)
		}
	}
}

func TestToPromptXMLBuiltinSkill(t *testing.T) {
	skills := []Skill{
		{
			Name:        "schedule",
			Description: "Create recurring tasks",
			Body:        "# Schedule\n\nInstructions here.",
		},
	}

	xml := ToPromptXML(skills)

	// Built-in skills use the same uniform location as filesystem skills
	if !contains(xml, "<activate>shelley skill cat schedule</activate>") {
		t.Error("built-in skill should use 'shelley skill cat' location")
	}
	// Content should NOT be inlined
	if contains(xml, "# Schedule") {
		t.Error("skill content should not be inlined (progressive disclosure)")
	}
}

func TestExtractBody(t *testing.T) {
	content := "---\nname: test\n---\n\n# Body\n\nContent here."
	body := extractBody(content)
	if body != "# Body\n\nContent here." {
		t.Errorf("extractBody = %q, want %q", body, "# Body\n\nContent here.")
	}

	// No frontmatter
	body = extractBody("just content")
	if body != "" {
		t.Errorf("extractBody with no frontmatter = %q, want empty", body)
	}
}

func TestFindByNameBuiltin(t *testing.T) {
	content, err := FindByName("schedule", t.TempDir())
	if err != nil {
		t.Fatalf("FindByName(schedule): %v", err)
	}
	if !strings.Contains(content, "name: schedule") {
		t.Error("expected content to contain frontmatter")
	}
	if !strings.Contains(content, "systemd") {
		t.Error("expected content to contain skill body")
	}
}

func TestFindByNameNotFound(t *testing.T) {
	_, err := FindByName("nonexistent-skill", t.TempDir())
	if err == nil {
		t.Error("expected error for nonexistent skill")
	}
}

func TestFindByNameFilesystem(t *testing.T) {
	// Create a skill on the filesystem in a default dir location
	tmpHome := t.TempDir()
	skillDir := filepath.Join(tmpHome, ".config", "shelley", "my-fs-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillContent := "---\nname: my-fs-skill\ndescription: A filesystem skill.\n---\n\nFS instructions.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644); err != nil {
		t.Fatal(err)
	}

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	content, err := FindByName("my-fs-skill", t.TempDir())
	if err != nil {
		t.Fatalf("FindByName(my-fs-skill): %v", err)
	}
	if !strings.Contains(content, "FS instructions") {
		t.Error("expected filesystem skill content")
	}
}

func TestEmptySkillMDSuppressesBuiltin(t *testing.T) {
	tmpHome := t.TempDir()

	// Create an empty SKILL.md for "schedule" — should suppress the built-in.
	skillDir := filepath.Join(tmpHome, ".config", "shelley", "schedule")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	// FindByName should report the skill as disabled.
	_, err := FindByName("schedule", t.TempDir())
	if err == nil {
		t.Fatal("expected error for suppressed built-in skill")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected 'disabled' in error, got: %v", err)
	}

	// ListAll should not include the suppressed built-in.
	for _, s := range ListAll(t.TempDir(), "") {
		if s.Name == "schedule" {
			t.Error("suppressed built-in 'schedule' should not appear in ListAll")
		}
	}
}

func TestMalformedSkillMDShowsParseError(t *testing.T) {
	tmpHome := t.TempDir()

	// Create a non-empty but malformed SKILL.md for "schedule".
	skillDir := filepath.Join(tmpHome, ".config", "shelley", "schedule")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("this is not valid frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	// FindByName should return the parse error, not a generic "disabled" message.
	_, err := FindByName("schedule", t.TempDir())
	if err == nil {
		t.Fatal("expected error for malformed SKILL.md")
	}
	if strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected parse error, not 'disabled': %v", err)
	}
	if !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("expected error to mention frontmatter issue, got: %v", err)
	}
}

func TestFilesystemSkillOverridesBuiltin(t *testing.T) {
	tmpHome := t.TempDir()

	// Create a filesystem "schedule" skill that overrides the built-in.
	skillDir := filepath.Join(tmpHome, ".config", "shelley", "schedule")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customContent := "---\nname: schedule\ndescription: My custom schedule skill.\n---\n\nCustom instructions.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(customContent), 0o644); err != nil {
		t.Fatal(err)
	}

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	// FindByName should return the filesystem version, not the built-in.
	content, err := FindByName("schedule", t.TempDir())
	if err != nil {
		t.Fatalf("FindByName(schedule): %v", err)
	}
	if !strings.Contains(content, "Custom instructions") {
		t.Error("expected filesystem skill content, got built-in")
	}

	// ListAll should include the filesystem version.
	found := false
	for _, s := range ListAll(t.TempDir(), "") {
		if s.Name == "schedule" {
			found = true
			if s.Description != "My custom schedule skill." {
				t.Errorf("expected filesystem description, got %q", s.Description)
			}
		}
	}
	if !found {
		t.Error("expected 'schedule' in ListAll (filesystem override)")
	}
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

func TestParseWhen(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "SKILL.md")
	content := "---\nname: foo\ndescription: Foo skill.\nwhen: exe.dev\n---\n\nbody\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.When != "exe.dev" {
		t.Errorf("When = %q, want %q", s.When, "exe.dev")
	}
}

func TestFilterWhen(t *testing.T) {
	in := []Skill{
		{Name: "always", Description: "x"},
		{Name: "on-exe", Description: "x", When: "exe.dev"},
		{Name: "unknown", Description: "x", When: "mars"},
	}

	got := Filter(in, Env{ExeDev: false})
	if len(got) != 1 || got[0].Name != "always" {
		t.Errorf("ExeDev=false: got %v, want [always]", skillNames(got))
	}

	got = Filter(in, Env{ExeDev: true})
	if len(got) != 2 || got[0].Name != "always" || got[1].Name != "on-exe" {
		t.Errorf("ExeDev=true: got %v, want [always on-exe]", skillNames(got))
	}
}
