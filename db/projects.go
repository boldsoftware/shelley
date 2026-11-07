package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"shelley.exe.dev/db/generated"
)

// Project is a profile's project data: what conversations in the profile
// work on, read live on every request. Their settings (model, effort, tools,
// base system prompt) are the profile's, copied as for any profile. A
// conversation is in a project when its profile label names one. See
// 050-profile-projects.sql.
type Project struct {
	// ID is assigned on creation, kept across renames and never reused.
	// Position is the project's place in the user's order, 0 first; a new
	// project goes last (see ReorderProjects). Both are ignored on input.
	ID       int64 `json:"id"`
	Position int64 `json:"position"`
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
	// PageURL is the web page the project's view opens on, "" for none. On
	// input, nil (absent) keeps the current one.
	PageURL *string `json:"page_url"`
	// CommitIdentity is who the project's tasks commit as, the zero value
	// for the default commit identity. On input, nil (absent) keeps the
	// current one.
	CommitIdentity *CommitIdentity `json:"commit_identity"`
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
	if err := validateProjectPage(project.PageURL); err != nil {
		return err
	}
	if project.CommitIdentity != nil {
		if err := ValidateCommitIdentity(project.CommitIdentity); err != nil {
			return err
		}
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
	var created *Project
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		if err := createProject(ctx, q, project, from); err != nil {
			return err
		}
		var err error
		created, err = getProject(ctx, q, project.Name)
		return err
	})
	return created, err
}

func createProject(ctx context.Context, q *generated.Queries, project Project, from string) error {
	exists, err := found(q.GetProfile(ctx, project.Name))
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: a profile named %q already exists", ErrConflict, project.Name)
	}
	err = rowsChanged(q.CopyProfile(ctx, generated.CopyProfileParams{Name: project.Name, FromProfile: from}))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: profile %q not found", ErrInvalid, from)
	} else if err != nil {
		return err
	}
	actions, err := json.Marshal(project.Actions)
	if err != nil {
		return err
	}
	if err := q.CreateProject(ctx, generated.CreateProjectParams{
		Profile: project.Name, Instructions: project.Instructions, Cwd: project.Cwd, Actions: string(actions),
	}); err != nil {
		return err
	}
	if err := setProjectRepos(ctx, q, project.Name, project.Repos); err != nil {
		return err
	}
	if err := setProjectPage(ctx, q, project.Name, project.PageURL); err != nil {
		return err
	}
	if err := setProjectCommitIdentity(ctx, q, project.Name, project.CommitIdentity); err != nil {
		return err
	}
	return setProjectVM(ctx, q, project.Name, project.VM)
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
		q := generated.New(tx.Conn())
		if err := rowsChanged(q.UpdateProject(ctx, generated.UpdateProjectParams{
			Instructions: project.Instructions, Cwd: project.Cwd, Actions: string(actions), Profile: name,
		})); err != nil {
			return err
		}
		if err := setProjectRepos(ctx, q, name, project.Repos); err != nil {
			return err
		}
		if err := setProjectPage(ctx, q, name, project.PageURL); err != nil {
			return err
		}
		if err := setProjectCommitIdentity(ctx, q, name, project.CommitIdentity); err != nil {
			return err
		}
		if err := setProjectVM(ctx, q, name, project.VM); err != nil {
			return err
		}
		if project.Name == name {
			return nil
		}
		return renameProfile(ctx, q, name, project.Name)
	})
}

// renameProfile renames a custom profile. Its project data follows by
// foreign key; conversation labels and action runs are updated here.
func renameProfile(ctx context.Context, q *generated.Queries, from, to string) error {
	profile, err := q.GetProfile(ctx, from)
	if err != nil {
		return err
	}
	taken, err := found(q.GetProfile(ctx, to))
	switch {
	case err != nil:
		return err
	case profile.Builtin:
		return fmt.Errorf("%w: built-in profile %q can't be renamed", ErrInvalid, from)
	case taken:
		return fmt.Errorf("%w: a profile named %q already exists", ErrConflict, to)
	}
	if err := q.RenameProfile(ctx, generated.RenameProfileParams{ToName: to, FromName: from}); err != nil {
		return err
	}
	if err := q.RenameConversationsProfile(ctx, generated.RenameConversationsProfileParams{ToProfile: to, FromProfile: from}); err != nil {
		return err
	}
	return q.RenameActionRunsProfile(ctx, generated.RenameActionRunsProfileParams{ToProfile: to, FromProfile: from})
}

func newProject(row generated.ProfileProject) (*Project, error) {
	project := Project{ID: row.ID, Position: row.Position, Name: row.Profile, Instructions: row.Instructions, Cwd: row.Cwd}
	if err := json.Unmarshal([]byte(row.Actions), &project.Actions); err != nil {
		return nil, err
	}
	if project.Actions == nil {
		project.Actions = []ProjectAction{}
	}
	return &project, nil
}

func getProject(ctx context.Context, q *generated.Queries, name string) (*Project, error) {
	row, err := q.GetProject(ctx, name)
	if err != nil {
		return nil, err
	}
	project, err := newProject(row)
	if err != nil {
		return nil, err
	}
	projects := []Project{*project}
	err = attachProjectDetails(ctx, q, projects)
	return &projects[0], err
}

// getConversationProject returns the project of conversation conversationID, or
// sql.ErrNoRows if it is in none.
func getConversationProject(ctx context.Context, q *generated.Queries, conversationID string) (*Project, error) {
	c, err := q.GetWorkspaceConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	return getProject(ctx, q, c.Project)
}

func (db *DB) GetProject(ctx context.Context, name string) (*Project, error) {
	return db.QueriesRes(ctx, func(q *generated.Queries) (*Project, error) {
		return getProject(ctx, q, name)
	})
}

// GetConversationProject returns the project of conversation conversationID, or
// sql.ErrNoRows if it is in none.
func (db *DB) GetConversationProject(ctx context.Context, conversationID string) (*Project, error) {
	return db.QueriesRes(ctx, func(q *generated.Queries) (*Project, error) {
		return getConversationProject(ctx, q, conversationID)
	})
}

func listProjects(ctx context.Context, q *generated.Queries) ([]Project, error) {
	rows, err := q.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := []Project{}
	for _, row := range rows {
		project, err := newProject(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *project)
	}
	return out, attachProjectDetails(ctx, q, out)
}

func attachProjectDetails(ctx context.Context, q *generated.Queries, projects []Project) error {
	if err := attachProjectVMs(ctx, q, projects); err != nil {
		return err
	}
	if err := attachProjectPages(ctx, q, projects); err != nil {
		return err
	}
	if err := attachProjectCommitIdentities(ctx, q, projects); err != nil {
		return err
	}
	return attachProjectRepos(ctx, q, projects)
}

func (db *DB) ListProjects(ctx context.Context) ([]Project, error) {
	return db.QueriesRes(ctx, func(q *generated.Queries) ([]Project, error) {
		return listProjects(ctx, q)
	})
}

// CheckProjectDeletable returns the project's base VM setup conversation, if any, or
// why the project can't be deleted: it has unarchived conversations other than that
// setup conversation, or a running action, or its profile is the default.
func (db *DB) CheckProjectDeletable(ctx context.Context, name string) (setupID string, err error) {
	err = db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		setupID, err = checkProjectDeletable(ctx, generated.New(rx.Conn()), name)
		return err
	})
	return setupID, err
}

func checkProjectDeletable(ctx context.Context, q *generated.Queries, name string) (string, error) {
	setupID, err := q.GetProjectSetupConversationID(ctx, name)
	if err != nil {
		return "", err
	}
	activity, err := q.GetProjectActivity(ctx, generated.GetProjectActivityParams{Project: name, ExceptID: setupID})
	if err != nil {
		return "", err
	}
	switch {
	case activity.Active == 1:
		return "", fmt.Errorf("%w: project %q has an active conversation; archive it or move it to another project first", ErrConflict, name)
	case activity.Active > 1:
		return "", fmt.Errorf("%w: project %q has %d active conversations; archive them or move them to another project first", ErrConflict, name, activity.Active)
	case activity.Running:
		return "", fmt.Errorf("%w: project %q has a running action; wait for it to finish", ErrConflict, name)
	}
	return setupID, nil
}

// DeleteProject deletes a project and its profile. It must have no active
// conversations except its base VM setup conversation, which it archives, and no recorded
// base VMs left (the caller deletes them first). Its archived conversations keep
// their history, and the name as a label, outside any project.
func (db *DB) DeleteProject(ctx context.Context, name string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		setupID, err := checkProjectDeletable(ctx, q, name)
		if err != nil {
			return err
		}
		vms, err := listProjectBaseVMs(ctx, q, name)
		if err != nil {
			return err
		}
		if len(vms) > 0 {
			return fmt.Errorf("%w: base VM %s of the project is not deleted yet", ErrConflict, vms[0].Name)
		}
		if setupID != "" {
			if _, err := q.ArchiveConversation(ctx, setupID); err != nil {
				return err
			}
		}
		if err := q.DeleteProjectProfile(ctx, name); err != nil {
			return err
		}
		names, err := q.ListProjectNames(ctx)
		if err != nil {
			return err
		}
		return setProjectOrder(ctx, q, names)
	})
}

// ReorderProjects puts the projects in the order of names, which must name
// every project once.
func (db *DB) ReorderProjects(ctx context.Context, names []string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		current, err := q.ListProjectNames(ctx)
		if err != nil {
			return err
		}
		slices.Sort(current)
		if !slices.Equal(current, slices.Sorted(slices.Values(names))) {
			return fmt.Errorf("%w: the order must name every project once", ErrConflict)
		}
		return setProjectOrder(ctx, q, names)
	})
}

// setProjectOrder numbers the projects names 0, 1, … in that order.
func setProjectOrder(ctx context.Context, q *generated.Queries, names []string) error {
	for i, name := range names {
		if err := q.SetProjectPosition(ctx, generated.SetProjectPositionParams{Position: int64(i), Profile: name}); err != nil {
			return err
		}
	}
	return nil
}
