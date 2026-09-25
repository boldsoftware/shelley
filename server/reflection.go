package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tailscale.com/util/singleflight"

	"shelley.exe.dev/exeenv"
)

// exeReflectionHTTPClient is used to query reflection integration endpoints.
// It is read from background goroutines (an end-of-turn publish can probe the
// notify integration) while tests swap in fake transports, so access goes
// through an atomic pointer rather than a bare variable.
var exeReflectionHTTPClient atomic.Pointer[http.Client]

func init() {
	exeReflectionHTTPClient.Store(http.DefaultClient)
}

// reflectionHTTPClient returns the client used for reflection integration
// requests.
func reflectionHTTPClient() *http.Client {
	return exeReflectionHTTPClient.Load()
}

// setReflectionHTTPClient installs c and returns the client it replaced, so
// tests can restore the previous value in a cleanup.
func setReflectionHTTPClient(c *http.Client) *http.Client {
	return exeReflectionHTTPClient.Swap(c)
}

// exeReflectionEmojiHTTPClient is separate so tests that mock integration
// discovery do not also receive root-document requests during page rendering.
var exeReflectionEmojiHTTPClient = http.DefaultClient

const (
	reflectionEmojiTimeout  = time.Second
	reflectionEmojiCacheTTL = time.Minute
)

var (
	reflectionEmojiMu    sync.Mutex
	reflectionEmojiValue string
	reflectionEmojiAt    time.Time
	reflectionEmojiOnce  bool
	reflectionEmojiFly   singleflight.Group[string, string]
)

type reflectionEmojiResponse struct {
	Emoji string `json:"emoji"`
}

// cachedReflectionEmoji returns the VM emoji from reflection. A short cache
// keeps concurrent page loads from making redundant requests.
func cachedReflectionEmoji(ctx context.Context) string {
	if testing.Testing() && exeReflectionEmojiHTTPClient == http.DefaultClient {
		return ""
	}
	env, err := exeenv.Current()
	if err != nil {
		return ""
	}
	return cachedReflectionEmojiIn(ctx, env)
}

func cachedReflectionEmojiIn(ctx context.Context, env exeenv.Environment) string {
	reflectionEmojiMu.Lock()
	if reflectionEmojiOnce && time.Since(reflectionEmojiAt) < reflectionEmojiCacheTTL {
		emoji := reflectionEmojiValue
		reflectionEmojiMu.Unlock()
		return emoji
	}
	reflectionEmojiMu.Unlock()

	emoji, _, _ := reflectionEmojiFly.Do("emoji", func() (string, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reflectionEmojiTimeout)
		defer cancel()

		emoji := reflectionEmoji(fetchCtx, env)
		reflectionEmojiMu.Lock()
		reflectionEmojiValue, reflectionEmojiAt, reflectionEmojiOnce = emoji, time.Now(), true
		reflectionEmojiMu.Unlock()
		return emoji, nil
	})
	return emoji
}

func reflectionEmoji(ctx context.Context, env exeenv.Environment) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.ReflectionURL(), nil)
	if err != nil {
		return ""
	}
	resp, err := exeReflectionEmojiHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var body reflectionEmojiResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ""
	}
	return strings.TrimSpace(body.Emoji)
}

func resetReflectionEmojiCache() {
	reflectionEmojiMu.Lock()
	reflectionEmojiValue = ""
	reflectionEmojiAt = time.Time{}
	reflectionEmojiOnce = false
	reflectionEmojiMu.Unlock()
}

// reflectionIntegration is one entry in the reflection /integrations response.
type reflectionIntegration struct {
	Name    string              `json:"name"`
	Type    string              `json:"type"`
	Comment string              `json:"comment,omitempty"`
	Help    string              `json:"help,omitempty"`
	Team    bool                `json:"team,omitempty"`
	URL     string              `json:"url,omitempty"`
	Details *integrationDetails `json:"details,omitempty"`
}

type integrationDetails struct {
	Repositories []integrationRepository `json:"repositories,omitempty"`
	ModelCounts  []integrationModelCount `json:"model_counts,omitempty"`
	ModelsError  string                  `json:"models_error,omitempty"`
}

type integrationRepository struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	CloneCommand string `json:"clone_command"`
}

// githubRepositories finds owner/repo clone URLs in a GitHub integration's help text.
func githubRepositories(help, baseURL string) []integrationRepository {
	pattern := regexp.MustCompile(regexp.QuoteMeta(baseURL+"/") + `([A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+)`)
	var repos []integrationRepository
	for _, match := range pattern.FindAllStringSubmatch(help, -1) {
		name := strings.TrimSuffix(match[1], ".git")
		if slices.ContainsFunc(repos, func(r integrationRepository) bool { return r.Name == name }) {
			continue
		}
		repos = append(repos, integrationRepository{
			Name:         name,
			URL:          "https://github.com/" + name,
			CloneCommand: "git clone " + baseURL + "/" + name + ".git",
		})
	}
	return repos
}

type integrationModelCount struct {
	Provider      string `json:"provider"`
	Mode          string `json:"mode,omitempty"`
	Chat          int    `json:"chat,omitempty"`
	Embeddings    int    `json:"embeddings,omitempty"`
	Transcription int    `json:"transcription,omitempty"`
	Other         int    `json:"other,omitempty"`
}

func listIntegrationModelCounts(ctx context.Context, client *http.Client, baseURL string) ([]integrationModelCount, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog returned %d", resp.StatusCode)
	}
	var catalog struct {
		SchemaVersion int `json:"schema_version"`
		Providers     map[string]struct {
			Mode string `json:"mode"`
		} `json:"providers"`
		Models []struct {
			ID       string   `json:"id"`
			Provider string   `json:"provider"`
			APIs     []string `json:"apis"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, (2<<20)+1)).Decode(&catalog); err != nil {
		return nil, err
	}
	if catalog.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported model catalog version %d", catalog.SchemaVersion)
	}
	byProvider := make(map[string]*integrationModelCount)
	for name, provider := range catalog.Providers {
		byProvider[name] = &integrationModelCount{Provider: name, Mode: provider.Mode}
	}
	for _, model := range catalog.Models {
		if model.ID == "" || model.Provider == "" {
			continue
		}
		count := byProvider[model.Provider]
		if count == nil {
			count = &integrationModelCount{Provider: model.Provider}
			byProvider[model.Provider] = count
		}
		switch {
		case slices.Contains(model.APIs, "openai_chat"), slices.Contains(model.APIs, "openai_responses"), slices.Contains(model.APIs, "anthropic_messages"):
			count.Chat++
		case slices.Contains(model.APIs, "openai_embeddings"):
			count.Embeddings++
		case slices.Contains(model.APIs, "openai_transcriptions"), slices.Contains(model.APIs, "deepgram_listen"):
			count.Transcription++
		default:
			count.Other++
		}
	}
	counts := make([]integrationModelCount, 0, len(byProvider))
	for _, count := range byProvider {
		counts = append(counts, *count)
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].Provider < counts[j].Provider })
	return counts, nil
}

// listAttachedIntegrations returns what reflection reports for this VM.
// It needs no exe.dev account access.
func listAttachedIntegrations(ctx context.Context, client *http.Client, env exeenv.Environment) ([]reflectionIntegration, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.ReflectionURL()+"/integrations", nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("reflection returned %d", resp.StatusCode)
	}
	var body struct {
		Integrations []reflectionIntegration `json:"integrations"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, resp.StatusCode, err
	}
	if body.Integrations == nil {
		return nil, resp.StatusCode, fmt.Errorf("reflection response has no integrations")
	}
	return body.Integrations, resp.StatusCode, nil
}

// handleIntegrations exposes attached integration metadata from reflection.
// Creating, attaching, and configuring integrations stays with exe.dev.
func handleIntegrations(w http.ResponseWriter, r *http.Request) {
	if !isExeDev() {
		http.NotFound(w, r)
		return
	}
	env, err := exeenv.Current()
	if err != nil {
		http.Error(w, "Cannot determine exe.dev environment", http.StatusInternalServerError)
		return
	}
	handleIntegrationsIn(w, r, env, reflectionHTTPClient())
}

func handleIntegrationsIn(w http.ResponseWriter, r *http.Request, env exeenv.Environment, client *http.Client) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	integrations, status, err := listAttachedIntegrations(ctx, client, env)
	switch {
	case status == http.StatusForbidden:
		http.Error(w, "Reflection integration is not attached to this VM", http.StatusServiceUnavailable)
		return
	case status == 0 && err != nil:
		http.Error(w, "Cannot reach reflection integration", http.StatusBadGateway)
		return
	case status != http.StatusOK:
		http.Error(w, "Reflection integration is unavailable", http.StatusBadGateway)
		return
	case err != nil:
		http.Error(w, "Invalid reflection integration response", http.StatusBadGateway)
		return
	}
	for i := range integrations {
		name := integrations[i].Name
		if integrations[i].Type == "github" {
			name = "github"
		}
		integrations[i].URL = env.IntegrationURL(name, integrations[i].Team)
	}
	if r.URL.Query().Has("details") {
		name := r.URL.Query().Get("details")
		team := r.URL.Query().Get("team") == "true"
		i := slices.IndexFunc(integrations, func(ig reflectionIntegration) bool { return ig.Name == name && ig.Team == team })
		if i < 0 {
			http.Error(w, "Integration is not attached to this VM", http.StatusNotFound)
			return
		}
		selected := integrations[i]
		if selected.Type == "github" {
			if repos := githubRepositories(selected.Help, selected.URL); len(repos) > 0 {
				selected.Details = &integrationDetails{Repositories: repos}
			}
		}

		if selected.Type == "llm" {
			selected.Details = &integrationDetails{}
			counts, err := listIntegrationModelCounts(r.Context(), client, selected.URL)
			if err != nil {
				selected.Details.ModelsError = "Model catalog unavailable from this VM"
			} else {
				selected.Details.ModelCounts = counts
			}
		}
		integrations = []reflectionIntegration{selected}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Integrations []reflectionIntegration `json:"integrations"`
	}{integrations})
}

// exeDevHasNotifyIntegration reports whether this VM has a "notify" integration
// available (i.e. push notifications to the owner's devices are possible). It
// queries the default "reflection" integration. Returns false if the
// integration is disabled/detached or on any network error.
func exeDevHasNotifyIntegration() bool {
	// Never probe the reflection API over the real network from a test binary
	// unless a test has explicitly injected its own client. Many server and
	// integration tests run with predictableOnly=false and mock LLMs; without
	// this guard every simulated end-of-turn would fire a REAL push to the VM
	// owner's devices whenever the host VM happens to have the "notify"
	// integration attached. Tests that exercise the reflection logic itself
	// override exeReflectionHTTPClient with a fake transport (see
	// exe_notify_test.go); those are unaffected because the client is no longer
	// the default.
	if testing.Testing() && reflectionHTTPClient() == http.DefaultClient {
		return false
	}
	env, err := exeenv.Current()
	if err != nil {
		return false
	}
	return exeDevHasNotifyIntegrationIn(env)
}

func exeDevHasNotifyIntegrationIn(env exeenv.Environment) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	available, err := hasNotifyIntegration(ctx, reflectionHTTPClient(), env)
	return err == nil && available
}

func hasNotifyIntegration(ctx context.Context, client *http.Client, env exeenv.Environment) (bool, error) {
	integrations, _, err := listAttachedIntegrations(ctx, client, env)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(integrations, func(ig reflectionIntegration) bool {
		return ig.Type == "notify"
	}), nil
}
