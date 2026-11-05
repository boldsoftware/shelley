package db

import (
	"errors"
	"slices"
	"testing"
)

func projectNames(t *testing.T, database *DB) []string {
	t.Helper()
	projects, err := database.ListProjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i, project := range projects {
		if project.Position != int64(i) {
			t.Fatalf("project %s at index %d has position %d", project.Name, i, project.Position)
		}
		names = append(names, project.Name)
	}
	return names
}

func TestProjectIDAndOrder(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	var ids []int64
	for _, name := range []string{"b", "a", "c"} {
		project, err := database.CreateProject(ctx, Project{Name: name}, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, project.ID)
	}
	// New projects go last, whatever their names.
	if got := projectNames(t, database); !slices.Equal(got, []string{"b", "a", "c"}) {
		t.Fatalf("order %v", got)
	}

	// A rename keeps the id and the place.
	b, err := database.GetProject(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	b.Name = "z"
	if err := database.UpdateProject(ctx, "b", *b); err != nil {
		t.Fatal(err)
	}
	if z, err := database.GetProject(ctx, "z"); err != nil || z.ID != ids[0] || z.Position != 0 {
		t.Fatalf("renamed project: %+v %v", z, err)
	}

	if err := database.ReorderProjects(ctx, []string{"c", "z", "a"}); err != nil {
		t.Fatal(err)
	}
	if got := projectNames(t, database); !slices.Equal(got, []string{"c", "z", "a"}) {
		t.Fatalf("reordered %v", got)
	}
	for _, names := range [][]string{{"c", "z"}, {"c", "z", "a", "a"}, {"c", "z", "x"}, {"c", "z", "a", "x"}} {
		if err := database.ReorderProjects(ctx, names); !errors.Is(err, ErrConflict) {
			t.Fatalf("reorder %v: %v", names, err)
		}
	}

	// Deleting renumbers densely; the newest project's id is not reused.
	if err := database.DeleteProject(ctx, "z"); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteProject(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if got := projectNames(t, database); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("after deletion %v", got)
	}
	d, err := database.CreateProject(ctx, Project{Name: "d"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID <= ids[2] || d.Position != 1 {
		t.Fatalf("new project after deleting the newest: %+v, ids %v", d, ids)
	}
}

func TestProjectOrderMigration(t *testing.T) {
	t.Parallel()
	database := setupDBMigratedThrough(t, 62)
	defer database.Close()
	ctx := t.Context()
	// rowids c=1, a=3, b=4 (x had 2); listed by name before.
	if err := database.pool.Exec(ctx, `
		INSERT INTO profiles (name) VALUES ('c'), ('x'), ('a'), ('b');
		INSERT INTO profile_projects (profile, cwd) VALUES ('c', '/c'), ('x', ''), ('a', '/a');
		DELETE FROM profiles WHERE name = 'x';
		INSERT INTO profile_projects (profile, instructions, actions) VALUES ('b', 'Do b.', '[{"id":"t","label":"t","command":"t"}]');
		INSERT INTO conversations (conversation_id, user_initiated, conversation_options) VALUES ('in-a', 1, '{"profile":"a"}');
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id       int64
		name     string
		position int64
		cwd      string
	}
	var got []row
	for _, p := range projects {
		got = append(got, row{p.ID, p.Name, p.Position, p.Cwd})
	}
	want := []row{{3, "a", 0, "/a"}, {4, "b", 1, ""}, {1, "c", 2, "/c"}}
	if !slices.Equal(got, want) {
		t.Fatalf("migrated projects %+v, want %+v", got, want)
	}
	if projects[1].Instructions != "Do b." || len(projects[1].Actions) != 1 {
		t.Fatalf("migrated b: %+v", projects[1])
	}
	if c, err := database.GetWorkspaceConversation(ctx, "in-a"); err != nil || c.Project != "a" {
		t.Fatalf("conversation in a: %+v %v", c, err)
	}
	// Ids go on from the largest.
	e, err := database.CreateProject(ctx, Project{Name: "e"}, "")
	if err != nil || e.ID != 5 || e.Position != 3 {
		t.Fatalf("new project: %+v %v", e, err)
	}
}
