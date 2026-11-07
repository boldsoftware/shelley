package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/skills"
)

func TestDiscoverIntegrationSkills(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}

	var requestedMu sync.Mutex
	var requested []string
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestedMu.Lock()
		requested = append(requested, req.URL.String())
		requestedMu.Unlock()
		body := ""
		status := http.StatusOK
		switch req.URL.String() {
		case "https://reflection.int.example.test/integrations":
			body = `{"integrations":[
				{"name":"z-personal","type":"file","attributes":["type:shelley-skill"]},
				{"name":"a-team","type":"file","team":true,"attributes":["type:shelley-skill","other:value"]},
				{"name":"beta","type":"file","attributes":["type:shelley-skill"]},
				{"name":"broken","type":"file","attributes":["type:shelley-skill"]},
				{"name":"missing","type":"file","attributes":["type:shelley-skill"]},
				{"name":"ignored-near-match","type":"file","attributes":["prefix:type:shelley-skill"]},
				{"name":"bad;touch-x","type":"file","attributes":["type:shelley-skill"]},
				{"name":"ignored-type","type":"http-proxy","attributes":["type:shelley-skill"]}
			]}`
		case "https://a-team.team.example.test/":
			body = "---\nname: shared\ndescription: First duplicate.\nlicense: MIT\nmetadata:\n  owner: team\n---\nteam body\n"
		case "https://beta.int.example.test/":
			body = "---\nname: beta\ndescription: Beta skill.\n---\nbeta body\n"
		case "https://broken.int.example.test/":
			body = "not frontmatter"
		case "https://missing.int.example.test/":
			status = http.StatusNotFound
			body = "missing"
		case "https://z-personal.int.example.test/":
			body = "---\nname: shared\ndescription: Later duplicate.\n---\npersonal body\n"
		default:
			return nil, errors.New("unexpected request: " + req.URL.String())
		}
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	got, err := discoverIntegrationSkills(t.Context(), httpc, logger, env, nil)
	if err != nil || len(got) != 2 {
		t.Fatalf("skills = %+v, err = %v", got, err)
	}
	if got[0].Name != "beta" || got[1].Name != "shared" {
		t.Fatalf("skill order = [%s, %s], want [beta, shared]", got[0].Name, got[1].Name)
	}
	shared := got[1]
	if shared.Description != "Later duplicate." {
		t.Fatalf("shared metadata = %+v", shared)
	}
	if shared.Source != "https://z-personal.int.example.test/" || shared.Origin != "Integration" || shared.ActivationCommand() != "curl -s https://z-personal.int.example.test/" {
		t.Fatalf("shared source metadata = %+v", shared)
	}

	for _, unexpected := range []string{"ignored-near-match", "bad;touch-x", "ignored-type"} {
		for _, request := range requested {
			if strings.Contains(request, unexpected) {
				t.Fatalf("non-matching integration fetched: %s", request)
			}
		}
	}
	for _, warning := range []string{"malformed reflection entry", "malformed SKILL.md", "skill unavailable", "duplicate skill name"} {
		if !strings.Contains(logs.String(), warning) {
			t.Errorf("logs missing %q:\n%s", warning, logs.String())
		}
	}
}

func TestDiscoverIntegrationSkillsReflectionFailureErrors(t *testing.T) {
	env, err := exeenv.New("http", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	got, err := discoverIntegrationSkills(t.Context(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}, slog.Default(), env, nil)
	if err == nil || !strings.Contains(err.Error(), "reflection fetch failed") || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("skills = %+v, err = %v", got, err)
	}
}

func TestDiscoverIntegrationSkillsKeepsPreviousSkillWhenItsFetchFails(t *testing.T) {
	env, err := exeenv.New("http", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "http://reflection.int.example.test/integrations":
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"integrations":[
				{"name":"fresh","type":"file","attributes":["type:shelley-skill"]},
				{"name":"slow","type":"file","attributes":["type:shelley-skill"]},
				{"name":"garbled","type":"file","attributes":["type:shelley-skill"]},
				{"name":"new-and-broken","type":"file","attributes":["type:shelley-skill"]}
			]}`)), Header: make(http.Header)}, nil
		case "http://fresh.int.example.test/":
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("---\nname: fresh\ndescription: Updated.\n---\n")), Header: make(http.Header)}, nil
		case "http://slow.int.example.test/":
			// Simulate the refresh deadline expiring mid-fetch.
			cancel()
			return nil, req.Context().Err()
		case "http://garbled.int.example.test/":
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("<html>maintenance</html>")), Header: make(http.Header)}, nil
		case "http://new-and-broken.int.example.test/":
			return &http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		}
		return nil, errors.New("unexpected request: " + req.URL.String())
	})}
	previous := []skills.Skill{
		{Name: "fresh", Description: "Old.", Source: "http://fresh.int.example.test/", Origin: "Integration"},
		{Name: "slow", Description: "Kept.", Source: "http://slow.int.example.test/", Origin: "Integration"},
		{Name: "garbled", Description: "Kept too.", Source: "http://garbled.int.example.test/", Origin: "Integration"},
		{Name: "detached", Description: "No longer listed.", Source: "http://detached.int.example.test/", Origin: "Integration"},
	}
	var logs bytes.Buffer
	got, err := discoverIntegrationSkills(ctx, httpc, slog.New(slog.NewTextHandler(&logs, nil)), env, previous)
	if err != nil {
		t.Fatal(err)
	}
	var summary []string
	for _, skill := range got {
		summary = append(summary, skill.Name+":"+skill.Description)
	}
	if want := []string{"fresh:Updated.", "garbled:Kept too.", "slow:Kept."}; !slices.Equal(summary, want) {
		t.Fatalf("skills = %v, want %v", summary, want)
	}
	for _, want := range []string{`keeping previous" integration=slow`, `keeping previous" integration=garbled`, `skill unavailable" integration=new-and-broken`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestIntegrationSkillCacheRefreshesAtMostEveryInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		current := []skills.Skill{{Name: "first"}}
		cache := &integrationSkillCache{
			logger: slog.Default(),
			discover: func(_ context.Context, previous []skills.Skill) ([]skills.Skill, error) {
				calls++
				if calls == 2 && (len(previous) != 1 || previous[0].Name != "first") {
					t.Errorf("refresh got previous = %+v, want the first result", previous)
				}
				return current, nil
			},
		}
		names := func() []string {
			var names []string
			for _, skill := range cache.Skills(t.Context()) {
				names = append(names, skill.Name)
			}
			return names
		}

		if got := names(); !slices.Equal(got, []string{"first"}) || calls != 1 {
			t.Fatalf("initial skills = %v, calls = %d", got, calls)
		}
		current = []skills.Skill{{Name: "second"}}
		time.Sleep(integrationSkillRefreshInterval - time.Second)
		if got := names(); !slices.Equal(got, []string{"first"}) || calls != 1 {
			t.Fatalf("within interval: skills = %v, calls = %d", got, calls)
		}
		time.Sleep(time.Second)
		if got := names(); !slices.Equal(got, []string{"second"}) || calls != 2 {
			t.Fatalf("after interval: skills = %v, calls = %d", got, calls)
		}
	})
}

func TestIntegrationSkillCacheKeepsPreviousOnFailureAndTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		fail := errors.New("reflection offline")
		var discover func(context.Context) ([]skills.Skill, error)
		cache := &integrationSkillCache{
			logger:   slog.New(slog.NewTextHandler(&logs, nil)),
			discover: func(ctx context.Context, _ []skills.Skill) ([]skills.Skill, error) { return discover(ctx) },
		}

		discover = func(context.Context) ([]skills.Skill, error) { return []skills.Skill{{Name: "cached"}}, nil }
		if got := cache.Skills(t.Context()); len(got) != 1 || got[0].Name != "cached" {
			t.Fatalf("initial skills = %+v", got)
		}

		time.Sleep(integrationSkillRefreshInterval)
		discover = func(context.Context) ([]skills.Skill, error) { return nil, fail }
		if got := cache.Skills(t.Context()); len(got) != 1 || got[0].Name != "cached" {
			t.Fatalf("after failure: skills = %+v", got)
		}
		if !strings.Contains(logs.String(), "keeping previous skills") || !strings.Contains(logs.String(), fail.Error()) {
			t.Fatalf("failure not logged:\n%s", logs.String())
		}

		// A failed attempt counts against the interval so an outage costs
		// at most one refresh timeout per interval.
		calls := 0
		discover = func(context.Context) ([]skills.Skill, error) { calls++; return nil, fail }
		cache.Skills(t.Context())
		if calls != 0 {
			t.Fatalf("discovery re-ran %d times within the interval after a failure", calls)
		}

		time.Sleep(integrationSkillRefreshInterval)
		start := time.Now()
		discover = func(ctx context.Context) ([]skills.Skill, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if got := cache.Skills(t.Context()); len(got) != 1 || got[0].Name != "cached" {
			t.Fatalf("after timeout: skills = %+v", got)
		}
		if elapsed := time.Since(start); elapsed != integrationSkillRefreshTimeout {
			t.Fatalf("refresh blocked for %v, want the %v timeout", elapsed, integrationSkillRefreshTimeout)
		}
	})
}

func TestIntegrationSkillCacheRefreshOutlivesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cache := &integrationSkillCache{
		logger: slog.Default(),
		discover: func(ctx context.Context, _ []skills.Skill) ([]skills.Skill, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []skills.Skill{{Name: "fresh"}}, nil
		},
	}
	if got := cache.Skills(ctx); len(got) != 1 || got[0].Name != "fresh" {
		t.Fatalf("skills with cancelled caller = %+v", got)
	}
}

func TestNewIntegrationSkillCachePrimesAtStartup(t *testing.T) {
	calls := 0
	cache := newIntegrationSkillCache(slog.Default(), func(context.Context, []skills.Skill) ([]skills.Skill, error) {
		calls++
		return []skills.Skill{{Name: "primed"}}, nil
	})
	if calls != 1 {
		t.Fatalf("discovery ran %d times during construction, want 1", calls)
	}
	if got := cache.Skills(t.Context()); len(got) != 1 || got[0].Name != "primed" || calls != 1 {
		t.Fatalf("skills = %+v, calls = %d", got, calls)
	}
}

func TestCurrentIntegrationSkillDiscovererIsDisabledUnderGoTest(t *testing.T) {
	if currentIntegrationSkillDiscoverer(slog.Default()) != nil {
		t.Fatal("tests must not discover against the real reflection endpoint")
	}
	if got := newIntegrationSkillCache(slog.Default(), nil).Skills(t.Context()); got != nil {
		t.Fatalf("skills = %+v", got)
	}
}

// systemPromptSkillNames starts a new top-level or subagent conversation and
// returns the skill names recorded in its system prompt display data.
func systemPromptSkillNames(t *testing.T, h *TestHarness, subagent bool) []string {
	t.Helper()
	var conversationID string
	if subagent {
		parent, err := h.db.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		child, err := h.db.CreateSubagentConversation(t.Context(), "skills-subagent-"+parent.ConversationID, parent.ConversationID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.server.getOrCreateConversationManager(t.Context(), child.ConversationID, ""); err != nil {
			t.Fatal(err)
		}
		conversationID = child.ConversationID
	} else {
		conversationID = h.NewConversation("Hello", "").convID
	}

	messages, err := h.db.QueriesTxRes(t.Context(), func(q *generated.Queries) ([]generated.Message, error) {
		return q.ListMessages(t.Context(), conversationID)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range messages {
		if msg.Type != string(db.MessageTypeSystem) {
			continue
		}
		var displayData struct {
			Skills []struct {
				Name string `json:"name"`
			} `json:"skills"`
		}
		if err := json.Unmarshal([]byte(*msg.DisplayData), &displayData); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, skill := range displayData.Skills {
			names = append(names, skill.Name)
		}
		return names
	}
	t.Fatal("system message not found")
	return nil
}

// fakeSkillNames filters systemPromptSkillNames output down to the fake
// integration skills used by TestNewConversationsSeeRefreshedIntegrationSkills.
func fakeSkillNames(names []string) []string {
	var got []string
	for _, name := range names {
		if strings.HasPrefix(name, "fake-") {
			got = append(got, name)
		}
	}
	return got
}

func TestNewConversationsSeeRefreshedIntegrationSkills(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	var current []skills.Skill
	var discoverErr error
	cache := &integrationSkillCache{
		logger:   h.server.logger,
		discover: func(context.Context, []skills.Skill) ([]skills.Skill, error) { return current, discoverErr },
	}
	h.server.integrationSkills = cache
	// Reset the rate limit between conversations; the interval itself is
	// covered by TestIntegrationSkillCacheRefreshesAtMostEveryInterval.
	expireInterval := func() {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cache.attempted = time.Time{}
	}
	skill := func(name string) []skills.Skill { return []skills.Skill{{Name: name, Description: name + "."}} }

	// Both kinds of conversation refresh when the interval has elapsed and
	// keep the previous skills when the refresh fails.
	for _, subagent := range []bool{false, true} {
		fresh := fmt.Sprintf("fake-fresh-subagent-%v", subagent)
		expireInterval()
		current, discoverErr = skill(fresh), nil
		if got := systemPromptSkillNames(t, h, subagent); !slices.Equal(fakeSkillNames(got), []string{fresh}) {
			t.Fatalf("subagent=%v: skills = %v, want %s", subagent, got, fresh)
		}
		expireInterval()
		current, discoverErr = nil, errors.New("reflection offline")
		if got := systemPromptSkillNames(t, h, subagent); !slices.Equal(fakeSkillNames(got), []string{fresh}) {
			t.Fatalf("subagent=%v after failed refresh: skills = %v, want %s kept", subagent, got, fresh)
		}
		// Within the interval a changed skill set is not picked up yet.
		current, discoverErr = skill("fake-too-soon"), nil
		if got := systemPromptSkillNames(t, h, subagent); !slices.Equal(fakeSkillNames(got), []string{fresh}) {
			t.Fatalf("subagent=%v within interval: skills = %v, want %s only", subagent, got, fresh)
		}
	}
}
