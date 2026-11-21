package test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Ported from ui/e2e/background-jobs.spec.ts.

// patchSnapshot has f change the conversation list snapshot the browser
// loads, as the specs' routes of /api/conversations/snapshot did.
// f may fail, which fails the request, if the snapshot lacks what it changes.
func (s *shelley) patchSnapshot(f func(snapshot map[string]any, conversation func(id string) map[string]any) error) {
	s.route("GET /api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
		var snapshot map[string]any
		if err := s.fetchJSON(r, &snapshot); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		conversations, _ := snapshot["conversations"].([]any)
		err := f(snapshot, func(id string) map[string]any {
			for _, c := range conversations {
				if c, ok := c.(map[string]any); ok && c["conversation_id"] == id {
					return c
				}
			}
			return nil
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, snapshot)
	})
}

// jobIDs lists the IDs of a conversation's background jobs.
func (s *shelley) jobIDs(conversation string) []string {
	var jobs []struct {
		ID      string `json:"job_id"`
		Command string `json:"command"`
	}
	s.api("GET", "/api/conversation/"+conversation+"/background-jobs", nil, &jobs)
	var ids []string
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	return ids
}

func TestE2EBackgroundJobsRunningWork(t *testing.T) {
	e2e(t, `Running work exposes long commands and output in one expansion and stops the command. The test seeded a conversation at /c/bg-running whose bash tool started the background job "echo ready; sleep 600", with job ID {{JOB_ID}}. The browser is told (by a rewritten background-jobs response) that the job's command is {{DISPLAY_COMMAND}}, a long multi-line python3 command. "The strip" is '[aria-label="Running work"]', "the toggle" is the strip's [data-testid="running-work-toggle"], and "the job" is the strip's [data-testid="background-job"]. The viewport starts at 1280x900.
1. Navigate to /c/bg-running and wait up to 30s for [data-testid="message-input"] to be visible.
2. [data-testid="bash-tool-running-indicator"] should become visible (wait up to 15s).
3. The toggle should come to contain "python3" (wait up to 15s). The toggle's ".running-work-activity" should have exactly the text "ready", and the toggle must not contain "print(".
4. The toggle's ".running-work-name" must have the computed text-overflow "ellipsis", and its scrollWidth must be greater than its clientWidth (the preview is cut short).
5. Its getBoundingClientRect().width must be at most parseFloat(getComputedStyle(it).maxWidth) + 1, and that maxWidth must be a finite number (Number.isFinite).
6. Set the viewport to 390x500. document.documentElement.scrollWidth must be at most innerWidth (nothing overflows sideways).
7. Click the toggle. There should be exactly 1 job. The job's [data-testid="background-job-command"] should have exactly the text {{DISPLAY_COMMAND}} (with its newlines). The toggle must no longer contain "python3". The job must contain no details or summary elements (count 0). The job's [data-testid="background-job-tail"] should contain "ready". The job must not contain "PGID" nor ".log".
8. Click the job's [data-testid="background-job-kill"]. The running indicator should become hidden (wait up to 15s), and the toggle should come to contain "No running work" (wait up to 15s). The toggle should have aria-expanded "true". There should still be exactly 1 job; its ".running-command-time" should have exactly the text "Finished", it should have no kill button (count 0), and its tail should still contain "ready".
9. Click the toggle. The strip should disappear (count 0, wait up to 15s).
10. [data-testid="bash-tool-finished-job"] should become visible (wait up to 15s) with exactly the text "Failed". "The card" is the ".bash-tool" that contains it. The card should contain "echo ready; sleep 600". The card's button '[aria-label="Expand"]' should have aria-expanded "false", and the card should have no ".bash-tool-details" (count 0).
11. Click the card's Expand button. The card's first ".bash-tool-code" should have exactly the text "echo ready; sleep 600", and its last ".bash-tool-label" exactly "Output:". The card must not contain {{JOB_ID}}.`, func(s *shelley) {
		s.viewport(1280, 900)
		c := s.conversation("bash-bg: echo ready; sleep 600", "bg-running")
		display := "python3 /tmp/" + strings.Repeat("long-command-", 20) + ".py <<'PY'\n" +
			"print('a multiline command stays readable when expanded')\nPY"
		s.v("DISPLAY_COMMAND", display)
		var id string
		s.poll(15*time.Second, func() error {
			if ids := s.jobIDs(c.ID); len(ids) == 1 {
				id = ids[0]
				return nil
			}
			return errors.New("no background job yet")
		})
		s.v("JOB_ID", id)
		s.route("GET /api/conversation/{id}/background-jobs", func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") != c.ID {
				s.pass.ServeHTTP(w, r)
				return
			}
			var jobs []map[string]any
			if err := s.fetchJSON(r, &jobs); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			for _, j := range jobs {
				j["command"] = display
			}
			writeJSON(w, jobs)
		})
	})
}

func TestE2EBackgroundJobsHistoricalNotices(t *testing.T) {
	e2e(t, `Historical background job notices render collapsed bash cards. The test seeded a conversation at /c/job-notices; the browser is shown (through rewritten responses, with live updates cut off) these messages instead of its real ones, with no agent working:
- [data-message-id="job-abc12345"]: a notice that background job abc12345 finished (exit 0) with the command "bin/q --only 59e4fc203f" and the output "q: checking", a blank line, "q: passed";
- [data-message-id="job-def67890"]: an older, unrecognized notice of job def67890 whose whole text is "Older, unrecognized job notice" and, on a second line, "its output must still be readable";
- [data-message-id="job-1234abcd"]: a notice that background job 1234abcd was lost, with the command "cat /tmp/partial" and the output "one", "two" on two lines;
and a queued message (shown as [data-testid="queued-ghost"]) from background job fedcba98, which ran "sleep 100 && echo foo", exited 0 and printed "foo".
In each message, "the card" is its ".bash-tool", its status is the card's [data-testid="bash-tool-finished-job"], and its Expand button is the card's '[aria-label="Expand"]'. The viewport starts at 393x851. Check exact texts with assert_text (newlines included).
1. Navigate to /c/job-notices.
2. Pick the finished notice as the [data-testid="message"] that contains "bin/q --only 59e4fc203f" (has_text, not its data-message-id), and use that pick for the steps about it. It should have the class "message-tool-card", its card's ".bash-tool-command" should contain "bin/q --only 59e4fc203f" while the card is still collapsed, and it should contain no [data-testid="message-author-background-job"] (count 0). Its status should have exactly the text "Finished".
3. In its card's ".bash-tool-summary", compare getBoundingClientRect().top of ".bash-tool-emoji", ".bash-tool-command" and the status: the emoji's and the command's tops must differ by less than 2, and the status's top must be greater than the command's (the status sits below the command).
4. The card should have no ".bash-tool-details" (count 0). Click its Expand button. The card's last ".bash-tool-code" should have exactly the text "q: checking\n\nq: passed" (a blank line between), and its computed font-family must match /mono/i. The card must not contain "abc12345". The card's last ".bash-tool-label" should have exactly the text "Output:".
5. [data-message-id="job-def67890"] should have the class "message-tool-card"; its status should have exactly the text "Background update", and its card no ".bash-tool-details". Click its Expand button: its card should have exactly 1 ".bash-tool-section", its ".bash-tool-label" exactly "Output:", and its last ".bash-tool-code" exactly the notice's whole text, "Older, unrecognized job notice\nits output must still be readable".
6. In [data-message-id="job-1234abcd"], the status should have exactly the text "Lost". Click its Expand button: its last ".bash-tool-label" should be exactly "Output:", and its last ".bash-tool-code" should have a class containing "error" and exactly the text "one\ntwo".
7. [data-testid="queued-ghost"] should have the class "message-tool-card", and its [data-testid="queued-badge"] should be visible. Its status should have exactly the text "Finished" and it should have no ".bash-tool-details". Click its Expand button: its last ".bash-tool-code" should have exactly the text "foo". document.documentElement.scrollWidth must be at most window.innerWidth.
8. Set the viewport to 1280x720. The queued ghost's getBoundingClientRect().width and its ".message-content"'s width must differ by less than 2.
9. Reload. The finished notice's message (picked again as the [data-testid="message"] that contains "bin/q --only 59e4fc203f") should be visible, its card's ".bash-tool-command" should contain "bin/q --only 59e4fc203f", and it should have no ".bash-tool-details" (count 0, collapsed again), and its status should have exactly the text "Finished".`, func(s *shelley) {
		s.viewport(393, 851)
		c := s.conversation("echo: ready", "job-notices")
		var body map[string]any
		s.api("GET", "/api/conversation/"+c.ID, nil, &body)
		body["conversation"].(map[string]any)["agent_working"] = false
		notices := []struct{ jobID, text string }{
			{"abc12345", "Background job abc12345 finished: exit 0, 4m31s. Log: /tmp/shelley-jobs/abc12345.log\n" +
				"Command: bin/q --only 59e4fc203f\n\nq: checking\n\nq: passed"},
			{"def67890", "Older, unrecognized job notice\nits output must still be readable"},
			{"1234abcd", "Background job 1234abcd lost (host rebooted or killed). Log: /tmp/shelley-jobs/1234abcd.log\n" +
				"Command: cat /tmp/partial\n\none\ntwo"},
		}
		var messages []any
		for i, n := range notices {
			messages = append(messages, map[string]any{
				"message_id":      "job-" + n.jobID,
				"conversation_id": c.ID,
				"sequence_id":     i + 1,
				"type":            "user",
				"llm_data":        mustJSON(map[string]any{"Role": 0, "Content": []any{map[string]any{"Type": 2, "Text": n.text}}}),
				"user_data":       mustJSON(map[string]any{"background_job_id": n.jobID, "Text": n.text}),
				"created_at":      "2026-09-22T12:00:00Z",
				"generation":      1,
			})
		}
		body["messages"] = messages
		queued := mustJSON([]any{map[string]any{
			"id":         "queued-job",
			"created_at": "2026-09-22T12:00:01Z",
			"model":      "predictable",
			"llm":        map[string]any{"Role": 0, "Content": []any{map[string]any{"Type": 2, "Text": "queued job output"}}},
			"user_data": map[string]any{
				"background_job_id": "fedcba98",
				"command":           "sleep 100 && echo foo",
				"exit_code":         0,
				"duration":          "1m40s",
				"log_path":          "/tmp/shelley-jobs/fedcba98.log",
				"tail":              "foo",
			},
		}})
		body["conversation"].(map[string]any)["queued_messages"] = queued
		rewritten, err := json.Marshal(body)
		s.check(err)
		s.route("GET /api/stream2", abort)
		s.patchSnapshot(func(_ map[string]any, conversation func(string) map[string]any) error {
			c := conversation(c.ID)
			if c == nil {
				return errors.New("the conversation is not in the snapshot")
			}
			c["queued_messages"] = queued
			return nil
		})
		s.route("GET /api/conversation/{id}", func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") != c.ID {
				s.pass.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(rewritten)
		})
	})
}
