package lazycue

import (
	"encoding/json"
	"os"
)

// RunSummary is a machine-readable summary of a suite of test runs. It is
// intended for CI: it makes the cache hit/miss breakdown and agent spend
// explicit so a pipeline can surface "how much was cached" prominently.
type RunSummary struct {
	Total            int           `json:"total"`
	Passed           int           `json:"passed"`
	Failed           int           `json:"failed"`
	Cached           int           `json:"cached"`    // ran from cache, no agent
	Generated        int           `json:"generated"` // agent generated fresh (cache miss)
	Healed           int           `json:"healed"`    // cached but self-healed
	CacheHitPct      float64       `json:"cache_hit_pct"`
	InputTokens      int           `json:"input_tokens"`
	OutputTokens     int           `json:"output_tokens"`
	EstimatedCostUSD float64       `json:"estimated_cost_usd"`
	Useful           int           `json:"useful"` // times the tests have been useful (see Useful), a count that tests share counted once
	Tests            []TestSummary `json:"tests"`
}

// TestSummary is the per-test slice of a RunSummary.
type TestSummary struct {
	Description   string    `json:"description"`
	Name          string    `json:"name,omitempty"`
	Pass          bool      `json:"pass"`
	Mode          string    `json:"mode"`
	CacheVersion  int       `json:"cache_version"`
	DurationMS    int64     `json:"duration_ms"`
	AgentMS       int64     `json:"agent_ms"`
	InputTokens   int       `json:"input_tokens"`
	OutputTokens  int       `json:"output_tokens"`
	EstimatedCost float64   `json:"estimated_cost_usd"`
	VideoPath     string    `json:"video_path,omitempty"`
	Error         string    `json:"error,omitempty"`
	Heal          *HealInfo `json:"heal,omitempty"`       // why/how the test self-healed (only set when Mode == "healed")
	Useful        int       `json:"useful"`               // times the test has been useful (see Useful)
	UsefulAt      string    `json:"useful_at,omitempty"`  // where that count is written
	AskUseful     bool      `json:"ask_useful,omitempty"` // the test's steps failed, so it may have caught a bug: if so, increment its count
}

// Summarize aggregates results into a RunSummary.
func Summarize(results []*TestResult) RunSummary {
	var s RunSummary
	s.Total = len(results)
	// Tests that share a count (one call in a loop, or in a helper) add it
	// once.
	counted := make(map[string]bool)
	for _, r := range results {
		if r.UsefulAt == "" || !counted[r.UsefulAt] {
			counted[r.UsefulAt] = true
			s.Useful += r.Useful
		}
		if r.Pass {
			s.Passed++
		} else {
			s.Failed++
		}
		switch r.Mode {
		case RunModeCached:
			s.Cached++
		case RunModeGenerated:
			s.Generated++
		case RunModeHealed:
			s.Healed++
		}
		s.InputTokens += r.InputTokens
		s.OutputTokens += r.OutputTokens
		s.EstimatedCostUSD += r.EstimatedCost
		s.Tests = append(s.Tests, TestSummary{
			Description:   r.Description,
			Name:          r.Name,
			Pass:          r.Pass,
			Mode:          string(r.Mode),
			CacheVersion:  r.CacheVersion,
			DurationMS:    r.TotalDuration.Milliseconds(),
			AgentMS:       r.AgentDuration.Milliseconds(),
			InputTokens:   r.InputTokens,
			OutputTokens:  r.OutputTokens,
			EstimatedCost: r.EstimatedCost,
			VideoPath:     r.VideoPath,
			Error:         r.Error,
			Heal:          r.Heal,
			Useful:        r.Useful,
			UsefulAt:      r.UsefulAt,
			AskUseful:     r.asksUseful(),
		})
	}
	if s.Total > 0 {
		s.CacheHitPct = float64(s.Cached) / float64(s.Total) * 100
	}
	return s
}

// WriteSummary writes a RunSummary as JSON to path.
func WriteSummary(path string, results []*TestResult) error {
	s := Summarize(results)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
