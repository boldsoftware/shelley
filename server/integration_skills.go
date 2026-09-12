package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/skills"
)

const (
	integrationSkillAttribute    = "type:shelley-skill"
	integrationSkillMaxBytes     = 64 << 10
	integrationSkillMaxFiles     = 64
	integrationSkillFetchWorkers = 8
	integrationSkillTimeout      = 2 * time.Second
)

type integrationSkillEntry struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Team       bool     `json:"team,omitempty"`
	Attributes []string `json:"attributes,omitempty"`
}

var integrationSkillHTTPClient = http.DefaultClient

func discoverIntegrationSkillsAtStartup(logger *slog.Logger) []skills.Skill {
	if !isExeDev() {
		return nil
	}
	httpc := integrationSkillHTTPClient
	if !shouldDiscoverIntegrationSkills(testing.Testing(), httpc) {
		return nil
	}
	env, err := exeenv.Current()
	if err != nil {
		logger.Warn("Shelley skill integration discovery: environment detection failed", "error", err)
		return nil
	}
	return discoverIntegrationSkills(context.Background(), httpc, logger, env)
}

// shouldDiscoverIntegrationSkills prevents ordinary Go tests from probing the
// real reflection endpoint. Tests exercising discovery inject a fake client.
func shouldDiscoverIntegrationSkills(isTest bool, httpc *http.Client) bool {
	return !isTest || httpc != http.DefaultClient
}

func discoverIntegrationSkills(ctx context.Context, httpc *http.Client, logger *slog.Logger, env exeenv.Environment) []skills.Skill {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	if logger == nil {
		logger = slog.Default()
	}

	var response struct {
		Integrations []integrationSkillEntry `json:"integrations"`
	}
	if err := fetchIntegrationSkillJSON(ctx, httpc, env.ReflectionURL()+"/integrations", &response); err != nil {
		logger.Warn("Shelley skill integration discovery: reflection fetch failed", "error", err)
		return nil
	}

	type candidate struct {
		entry integrationSkillEntry
		root  string
	}
	var candidates []candidate
	for _, entry := range response.Integrations {
		if entry.Type != "file" || !slices.Contains(entry.Attributes, integrationSkillAttribute) {
			continue
		}
		if !validIntegrationSkillEntryName(entry.Name) {
			logger.Warn("Shelley skill integration discovery: malformed reflection entry", "name", entry.Name)
			continue
		}
		candidates = append(candidates, candidate{
			entry: entry,
			root:  env.IntegrationURL(entry.Name, entry.Team) + "/",
		})
		if len(candidates) == integrationSkillMaxFiles {
			logger.Warn("Shelley skill integration discovery: candidate limit reached", "limit", integrationSkillMaxFiles)
			break
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].entry.Team != candidates[j].entry.Team {
			return !candidates[i].entry.Team
		}
		return candidates[i].root < candidates[j].root
	})

	type result struct {
		content string
		err     error
	}
	results := make([]result, len(candidates))
	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(min(len(candidates), integrationSkillFetchWorkers))
	for range min(len(candidates), integrationSkillFetchWorkers) {
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i].content, results[i].err = fetchIntegrationSkill(ctx, httpc, candidates[i].root)
			}
		}()
	}
	for i := range candidates {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	seen := make(map[string]bool)
	var discovered []skills.Skill
	for i, candidate := range candidates {
		if results[i].err != nil {
			logger.Warn("Shelley skill integration discovery: skill fetch failed", "integration", candidate.entry.Name, "url", candidate.root, "error", results[i].err)
			continue
		}
		skill, err := skills.ParseContent(results[i].content)
		if err != nil {
			logger.Warn("Shelley skill integration discovery: malformed SKILL.md", "integration", candidate.entry.Name, "url", candidate.root, "error", err)
			continue
		}
		if seen[skill.Name] {
			logger.Warn("Shelley skill integration discovery: duplicate skill name; skipping", "skill", skill.Name, "integration", candidate.entry.Name, "url", candidate.root)
			continue
		}
		seen[skill.Name] = true
		skill.Path = ""
		skill.Body = ""
		skill.Source = candidate.root
		skill.Origin = "Integration"
		skill.Activate = "curl -fsS --max-time 5 -- " + candidate.root
		discovered = append(discovered, skill)
	}
	sort.Slice(discovered, func(i, j int) bool { return discovered[i].Name < discovered[j].Name })
	return discovered
}

func validIntegrationSkillEntryName(name string) bool {
	if len(name) == 0 || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func fetchIntegrationSkillJSON(ctx context.Context, httpc *http.Client, url string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, integrationSkillTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}

func fetchIntegrationSkill(ctx context.Context, httpc *http.Client, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, integrationSkillTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, integrationSkillMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > integrationSkillMaxBytes {
		return "", fmt.Errorf("SKILL.md exceeds %d bytes", integrationSkillMaxBytes)
	}
	return string(body), nil
}
