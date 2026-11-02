package db

import (
	"context"
	"fmt"
	"path"
	"regexp"

	"shelley.exe.dev/db/generated"
)

// ProjectRepo is a GitHub repository attached to a project through an exe.dev
// GitHub integration (one repository per integration).
type ProjectRepo struct {
	// Integration names the exe.dev GitHub integration granting access.
	Integration string `json:"integration"`
	// Repository is OWNER/REPO on GitHub.
	Repository string `json:"repository"`
}

// Name is the repository's name, which is also its directory under the
// VM user's home.
func (r ProjectRepo) Name() string { return path.Base(r.Repository) }

var (
	integrationNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	repositoryPattern      = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

func validateProjectRepos(repos []ProjectRepo) error {
	if len(repos) > 32 {
		return fmt.Errorf("%w: at most 32 repositories per project", ErrInvalid)
	}
	names := map[string]bool{}
	for _, repo := range repos {
		if !integrationNamePattern.MatchString(repo.Integration) || !repositoryPattern.MatchString(repo.Repository) {
			return fmt.Errorf("%w: repos need an exe.dev integration name and an OWNER/REPO repository", ErrInvalid)
		}
		if names[repo.Name()] {
			return fmt.Errorf("%w: two repositories named %q would share a VM directory", ErrInvalid, repo.Name())
		}
		names[repo.Name()] = true
	}
	return nil
}

func setProjectRepos(ctx context.Context, q *generated.Queries, project string, repos []ProjectRepo) error {
	if err := q.DeleteProjectRepos(ctx, project); err != nil {
		return err
	}
	for i, repo := range repos {
		if err := q.CreateProjectRepo(ctx, generated.CreateProjectRepoParams{
			Profile: project, Position: int64(i), Integration: repo.Integration, Repository: repo.Repository,
		}); err != nil {
			return err
		}
	}
	return nil
}

func attachProjectRepos(ctx context.Context, q *generated.Queries, projects []Project) error {
	rows, err := q.ListProjectRepos(ctx)
	if err != nil {
		return err
	}
	repos := map[string][]ProjectRepo{}
	for _, row := range rows {
		repos[row.Profile] = append(repos[row.Profile], ProjectRepo{Integration: row.Integration, Repository: row.Repository})
	}
	for i := range projects {
		projects[i].Repos = repos[projects[i].Name]
		if projects[i].Repos == nil {
			projects[i].Repos = []ProjectRepo{}
		}
	}
	return nil
}
