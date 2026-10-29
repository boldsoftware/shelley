package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
)

// Projects attach GitHub repositories through exe.dev GitHub integrations.
// Each integration grants one repository, so the repositories a project can
// attach are the GitHub integrations reflection lists for this VM.

// githubRepo is a repository this VM reaches through a GitHub integration.
type githubRepo struct {
	db.ProjectRepo
	CloneURL string `json:"clone_url"`
	Team     bool   `json:"team"`
}

var githubCloneURL = regexp.MustCompile(`https://github\.[^/\s]+/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?(?:\s|$)`)

func githubReposOf(integrations []reflectionIntegration) []githubRepo {
	repos := []githubRepo{}
	for _, ig := range integrations {
		if ig.Type != "github" {
			continue
		}
		m := githubCloneURL.FindStringSubmatch(ig.Help)
		if m == nil {
			continue
		}
		cloneURL := strings.TrimSpace(m[0])
		if !strings.HasSuffix(cloneURL, ".git") {
			cloneURL += ".git"
		}
		repos = append(repos, githubRepo{ProjectRepo: db.ProjectRepo{Integration: ig.Name, Repository: m[1]}, CloneURL: cloneURL, Team: ig.Team})
	}
	return repos
}

// githubConnectURL opens exe.dev's form to connect a GitHub repository and
// attach it to this VM, or is "" off exe.dev.
func githubConnectURL(env exeenv.Environment) string {
	if env == (exeenv.Environment{}) {
		return ""
	}
	q := url.Values{"service": {"github"}, "source": {"shelley"}}
	if name := env.VMName(); name != "" {
		q.Set("attach", "vm:"+name)
	}
	return "https://exe.dev/integrations/add?" + q.Encode()
}

func (s *Server) handleGithubRepos(w http.ResponseWriter, r *http.Request) {
	env, integrations, err := s.listIntegrations(r.Context())
	if err != nil {
		s.internalError(w, "Failed to list GitHub integrations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": githubReposOf(integrations), "connect_url": githubConnectURL(env)})
}

// projectReposPrompt tells the base VM setup conversation where to clone each of
// the project's repositories.
func (s *Server) projectReposPrompt(ctx context.Context, project *db.Project) (string, error) {
	if len(project.Repos) == 0 {
		return "The project has no repositories attached: create its directory (an empty git repository) unless its instructions say otherwise.", nil
	}
	_, integrations, err := s.listIntegrations(ctx)
	if err != nil {
		return "", fmt.Errorf("list GitHub integrations: %w", err)
	}
	urls := map[string]string{}
	for _, repo := range githubReposOf(integrations) {
		urls[repo.Integration] = repo.CloneURL
	}
	var out strings.Builder
	out.WriteString("Clone each of the project's repositories into the VM user's home, ~/REPO:")
	for _, repo := range project.Repos {
		fmt.Fprintf(&out, "\n- %s into ~/%s through exe.dev GitHub integration %q", repo.Repository, repo.Name(), repo.Integration)
		if u := urls[repo.Integration]; u != "" {
			fmt.Fprintf(&out, ": git clone %s", u)
		} else {
			out.WriteString(" (not attached to this host now: ask the user to attach it)")
		}
	}
	out.WriteString("\nGitHub integrations reach a VM through its tags or a direct attachment, and copies keep their base's tags but not direct attachments. Give the base this host's tags (`curl https://reflection.int.exe.xyz/tags`) and check every clone works there; if one needs an attachment a tag can't give, ask the user to attach that integration to a tag the base has.")
	return out.String(), nil
}

// defaultExeDevIntegration picks the exe.dev integration new projects' VMs
// use: the only one, or else the one named after this VM (e.g. "myvm-exe"
// on myvm); "" when that doesn't decide.
func defaultExeDevIntegration(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	var match string
	for _, name := range names {
		if name == host || strings.HasPrefix(name, host+"-") {
			if match != "" {
				return ""
			}
			match = name
		}
	}
	return match
}
