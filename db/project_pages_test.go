package db

import (
	"errors"
	"testing"
)

func TestProjectPage(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	page := func(name string) string {
		t.Helper()
		project, err := database.GetProject(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return *project.PageURL
	}
	url := " https://hub.exe.dev "
	if _, err := database.CreateProject(ctx, Project{Name: "hub", PageURL: &url}, ""); err != nil {
		t.Fatal(err)
	}
	if got := page("hub"); got != "https://hub.exe.dev" {
		t.Fatalf("page %q", got)
	}
	// Absent keeps it, across a rename too.
	if err := database.UpdateProject(ctx, "hub", Project{Name: "Hub"}); err != nil {
		t.Fatal(err)
	}
	if got := page("Hub"); got != "https://hub.exe.dev" {
		t.Fatalf("page after update without it %q", got)
	}
	for _, bad := range []string{"hub.exe.dev", "javascript:alert(1)", "ftp://x/", "https://"} {
		err := database.UpdateProject(ctx, "Hub", Project{Name: "Hub", PageURL: &bad})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("page %q: %v", bad, err)
		}
	}
	none := ""
	if err := database.UpdateProject(ctx, "Hub", Project{Name: "Hub", PageURL: &none}); err != nil {
		t.Fatal(err)
	}
	if got := page("Hub"); got != "" {
		t.Fatalf("cleared page %q", got)
	}
}
