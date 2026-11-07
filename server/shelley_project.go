package server

import (
	"errors"
	"os"
	"path/filepath"

	"shelley.exe.dev/db"
)

// shelleyProjectPrompt is how a Shelley-project conversation changes
// Shelley; it amends the project's instructions.
const shelleyProjectPrompt = `This task changes Shelley itself, in the canonical customization checkout that every Shelley task shares: coordinate with the others, and commit your changes to its custom branch. Read AGENTS.md and the customizing-shelley skill, which says how to install and restart. Preserve upstream conversation schemas; customization persistence belongs in new tables.`

// isShelleyProject reports whether project is Shelley's own: workspace mode
// is on and its directory is the canonical customization checkout, on this
// host. Until the checkout exists, the seeded Self project still names its
// path.
func (s *Server) isShelleyProject(project *db.Project) (bool, error) {
	if project.VM != nil || project.Cwd == "" || !s.workspaceMode() {
		return false, nil
	}
	canonical := CustomizationDir()
	a, err := filepath.EvalSymlinks(project.Cwd)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(project.Cwd) == filepath.Clean(canonical), nil
	}
	if err != nil {
		return false, err
	}
	b, err := filepath.EvalSymlinks(canonical)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return a == b, err
}
