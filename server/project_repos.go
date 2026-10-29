package server

import (
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
	Team bool `json:"team"`
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
		repos = append(repos, githubRepo{ProjectRepo: db.ProjectRepo{Integration: ig.Name, Repository: m[1]}, Team: ig.Team})
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
