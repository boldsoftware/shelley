package db

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"shelley.exe.dev/db/generated"
)

// validateProjectPage trims a project's page URL, which must be an absolute
// http(s) URL or "" for none.
func validateProjectPage(page *string) error {
	if page == nil {
		return nil
	}
	*page = strings.TrimSpace(*page)
	if *page == "" {
		return nil
	}
	u, err := url.Parse(*page)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		len(*page) > 2048 || strings.ContainsFunc(*page, unicode.IsControl) {
		return fmt.Errorf("%w: a project's page must be an http or https URL", ErrInvalid)
	}
	return nil
}

// setProjectPage sets the project's page; nil keeps it.
func setProjectPage(ctx context.Context, q *generated.Queries, project string, page *string) error {
	switch {
	case page == nil:
		return nil
	case *page == "":
		return q.DeleteProjectPage(ctx, project)
	}
	return q.UpsertProjectPage(ctx, generated.UpsertProjectPageParams{Profile: project, Url: *page})
}

func attachProjectPages(ctx context.Context, q *generated.Queries, projects []Project) error {
	rows, err := q.ListProjectPages(ctx)
	if err != nil {
		return err
	}
	pages := map[string]string{}
	for _, row := range rows {
		pages[row.Profile] = row.Url
	}
	for i := range projects {
		page := pages[projects[i].Name]
		projects[i].PageURL = &page
	}
	return nil
}
