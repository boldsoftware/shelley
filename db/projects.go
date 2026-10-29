package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Project is a profile's project data: what conversations in the profile
// work on, read live on every request. Their settings (model, effort, tools,
// base system prompt) are the profile's, copied as for any profile. A
// conversation is in a project when its profile label names one. See
// 050-profile-projects.sql.
type Project struct {
	// Name is the profile's name.
	Name string `json:"name"`
	// Instructions are appended to every request of the project's
	// conversations, like an AGENTS.md; their start is what the Assistant
	// routes on.
	Instructions string          `json:"instructions"`
	Cwd          string          `json:"cwd"`
	Actions      []ProjectAction `json:"actions"`
	// VM, if set, runs each conversation's tools and actions on its own VM; Cwd is
	// then a directory on that VM.
	VM *ProjectVM `json:"vm"`
	// Repos are the GitHub repositories attached to the project.
	Repos []ProjectRepo `json:"repos"`
}

// ProfileNameProblem says what's wrong with a profile name, or "". Names are
// path segments in the API.
func ProfileNameProblem(name string) string {
	switch {
	case name == "":
		return "name is required"
	case len(name) > 64:
		return "name is longer than 64 bytes"
	case strings.Contains(name, "/") || strings.ContainsFunc(name, unicode.IsControl):
		return "name can't contain / or control characters"
	case name == "." || name == "..":
		return "name can't be . or .." // they don't survive in a URL path
	}
	return ""
}

// actionID is the form of a project action's ID, which API paths name.
var actionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validateProject(project *Project) error {
	project.Name = strings.TrimSpace(project.Name)
	if msg := ProfileNameProblem(project.Name); msg != "" {
		return fmt.Errorf("%w: project %s", ErrInvalid, msg)
	}
	if project.Actions == nil {
		project.Actions = []ProjectAction{}
	}
	if len(project.Actions) > 32 {
		return fmt.Errorf("%w: at most 32 project actions are allowed", ErrInvalid)
	}
	if err := validateProjectVM(project.VM, project.Cwd); err != nil {
		return err
	}
	if err := validateProjectRepos(project.Repos); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, action := range project.Actions {
		if !actionID.MatchString(action.ID) || seen[action.ID] ||
			strings.TrimSpace(action.Label) == "" || strings.TrimSpace(action.Command) == "" ||
			strings.ContainsRune(action.Command, '\x00') || len(action.Command) > 16384 {
			return fmt.Errorf("%w: actions require labels, commands, and unique IDs of up to 64 letters, digits, - and _", ErrInvalid)
		}
		seen[action.ID] = true
	}
	return nil
}

// CreateProject creates the profile project.Name with its project data. The
// profile starts with a copy of the settings of profile from, or of the
// default profile if from is ""; it doesn't follow that profile's later edits.
func (db *DB) CreateProject(ctx context.Context, project Project, from string) (*Project, error) {
	if err := validateProject(&project); err != nil {
		return nil, err
	}
	actions, err := json.Marshal(project.Actions)
	if err != nil {
		return nil, err
	}
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var exists bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM profiles WHERE name=?)", project.Name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: a profile named %q already exists", ErrConflict, project.Name)
		}
		result, err := tx.Exec(`INSERT INTO profiles(name,settings)
			SELECT ?,settings FROM profiles WHERE CASE WHEN ?='' THEN is_default ELSE name=? END`, project.Name, from, from)
		if err := rowsChanged(result, err); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: profile %q not found", ErrInvalid, from)
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO profile_projects(profile,instructions,cwd,actions) VALUES(?,?,?,?)",
			project.Name, project.Instructions, project.Cwd, string(actions)); err != nil {
			return err
		}
		if err := setProjectRepos(tx, project.Name, project.Repos); err != nil {
			return err
		}
		return setProjectVM(tx, project.Name, project.VM)
	})
	return &project, err
}

// UpdateProject replaces the project data of profile name, and renames the
// profile if project.Name differs: its conversations and runs follow.
// Built-in profiles keep their names.
func (db *DB) UpdateProject(ctx context.Context, name string, project Project) error {
	if err := validateProject(&project); err != nil {
		return err
	}
	actions, err := json.Marshal(project.Actions)
	if err != nil {
		return err
	}
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		result, err := tx.Exec("UPDATE profile_projects SET instructions=?,cwd=?,actions=? WHERE profile=?",
			project.Instructions, project.Cwd, string(actions), name)
		if err := rowsChanged(result, err); err != nil {
			return err
		}
		if err := setProjectRepos(tx, name, project.Repos); err != nil {
			return err
		}
		if err := setProjectVM(tx, name, project.VM); err != nil {
			return err
		}
		if project.Name == name {
			return nil
		}
		return renameProfile(tx, name, project.Name)
	})
}

// renameProfile renames a custom profile. Its project data follows by
// foreign key; conversation labels and action runs are updated here.
func renameProfile(tx *Tx, from, to string) error {
	var builtin, taken bool
	if err := tx.QueryRow("SELECT builtin, EXISTS(SELECT 1 FROM profiles WHERE name=?) FROM profiles WHERE name=?", to, from).Scan(&builtin, &taken); err != nil {
		return err
	}
	switch {
	case builtin:
		return fmt.Errorf("%w: built-in profile %q can't be renamed", ErrInvalid, from)
	case taken:
		return fmt.Errorf("%w: a profile named %q already exists", ErrConflict, to)
	}
	if _, err := tx.Exec("UPDATE profiles SET name=? WHERE name=?", to, from); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE conversations AS c SET conversation_options=json_set(c.conversation_options,'$.profile',?)
		WHERE `+conversationProfile+`=?`, to, from); err != nil {
		return err
	}
	_, err := tx.Exec("UPDATE action_runs SET profile=? WHERE profile=?", to, from)
	return err
}

const projectColumns = "profile,instructions,cwd,actions"

func scanProject(scanner rowScanner) (*Project, error) {
	var project Project
	var actions string
	if err := scanner.Scan(&project.Name, &project.Instructions, &project.Cwd, &actions); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(actions), &project.Actions); err != nil {
		return nil, err
	}
	if project.Actions == nil {
		project.Actions = []ProjectAction{}
	}
	return &project, nil
}

// getProject loads the project named by the SQL expression nameExpr.
func getProject(rx *Rx, nameExpr string, args ...any) (*Project, error) {
	project, err := scanProject(rx.QueryRow("SELECT "+projectColumns+" FROM profile_projects WHERE profile="+nameExpr, args...))
	if err != nil {
		return nil, err
	}
	projects := []Project{*project}
	err = attachProjectDetails(rx, projects)
	return &projects[0], err
}

func (db *DB) GetProject(ctx context.Context, name string) (*Project, error) {
	var project *Project
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		project, err = getProject(rx, "?", name)
		return err
	})
	return project, err
}

func listProjects(rx *Rx) ([]Project, error) {
	out := []Project{}
	rows, err := rx.Query("SELECT " + projectColumns + " FROM profile_projects ORDER BY profile")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *project)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, attachProjectDetails(rx, out)
}

func attachProjectDetails(rx *Rx, projects []Project) error {
	if err := attachProjectVMs(rx, projects); err != nil {
		return err
	}
	return attachProjectRepos(rx, projects)
}

func (db *DB) ListProjects(ctx context.Context) ([]Project, error) {
	var out []Project
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		out, err = listProjects(rx)
		return err
	})
	return out, err
}

// DeleteProject deletes a project and its profile. It must have no active
// conversations or running action, and not be the default profile. Its archived conversations
// keep their history, and the name as a label, outside any project.
func (db *DB) DeleteProject(ctx context.Context, name string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.QueryRow(`SELECT 1 FROM profile_projects WHERE profile=?`, name).Scan(new(int)); err != nil {
			return err
		}
		var active int
		var running bool
		if err := tx.QueryRow(`WITH members AS (`+workspaceConversationQuery+` AND project=?)
			SELECT (SELECT COUNT(*) FROM members WHERE NOT archived),
			EXISTS(SELECT 1 FROM action_runs r JOIN members t ON t.conversation_id=r.conversation_id WHERE r.status='running')`,
			name).Scan(&active, &running); err != nil {
			return err
		}
		switch {
		case active == 1:
			return fmt.Errorf("%w: project %q has an active conversation; archive it or move it to another project first", ErrConflict, name)
		case active > 1:
			return fmt.Errorf("%w: project %q has %d active conversations; archive them or move them to another project first", ErrConflict, name, active)
		case running:
			return fmt.Errorf("%w: project %q has a running action; wait for it to finish", ErrConflict, name)
		}
		_, err := tx.Exec("DELETE FROM profiles WHERE name=?", name)
		return err
	})
}
