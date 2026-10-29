package db

import (
	"errors"
	"reflect"
	"testing"
)

func TestProjectRepos(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := database.EnsureWorkspace(ctx, "predictable", ConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	repos := []ProjectRepo{{Integration: "team-acme-api", Repository: "acme/api"}, {Integration: "acme-web", Repository: "acme/web"}}
	created, err := database.CreateProject(ctx, Project{Name: "Acme", Repos: repos}, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.GetProject(ctx, created.Name)
	if err != nil || !reflect.DeepEqual(got.Repos, repos) {
		t.Fatalf("repos = %+v, %v", got.Repos, err)
	}
	got.Repos = repos[1:]
	if err := database.UpdateProject(ctx, got.Name, *got); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		want := []ProjectRepo{}
		if p.Name == created.Name {
			want = repos[1:]
		}
		if !reflect.DeepEqual(p.Repos, want) {
			t.Fatalf("%s repos = %+v, want %+v", p.Name, p.Repos, want)
		}
	}
	for _, bad := range [][]ProjectRepo{
		{{Integration: "x", Repository: "no-owner"}},
		{{Integration: "", Repository: "a/b"}},
		{{Integration: "x", Repository: "a/b"}, {Integration: "y", Repository: "c/b"}},
	} {
		got.Repos = bad
		if err := database.UpdateProject(ctx, got.Name, *got); !errors.Is(err, ErrInvalid) {
			t.Fatalf("repos %+v: %v", bad, err)
		}
	}
	if err := database.DeleteProject(ctx, created.Name); err != nil {
		t.Fatal(err)
	}
}
