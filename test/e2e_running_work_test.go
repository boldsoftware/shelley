package test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ported from ui/e2e/running-work.spec.ts.

// subagentIDs lists the conversation IDs of a conversation's subagents.
func (s *shelley) subagentIDs(conversation string) []string {
	var subagents []struct {
		ID string `json:"conversation_id"`
	}
	s.api("GET", "/api/conversation/"+conversation+"/subagents", nil, &subagents)
	var ids []string
	for _, a := range subagents {
		ids = append(ids, a.ID)
	}
	return ids
}

// postStatus posts to the server and returns the response's status code,
// whatever it is.
func (s *shelley) postStatus(path string, body any) int {
	s.start()
	var r *strings.Reader
	if body != nil {
		r = strings.NewReader(mustJSON(body))
	} else {
		r = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(s.ctx, "POST", s.backend+path, r)
	s.check(err)
	req.Header.Set("Content-Type", "application/json")
	s.addHeaders(req.Header)
	resp, err := (&http.Client{Transport: s.transport}).Do(req)
	s.check(err)
	resp.Body.Close()
	return resp.StatusCode
}

// okStatus is "ok" for a 2xx status code, as Playwright's response.ok().
func okStatus(code int) string {
	if code/100 == 2 {
		return "ok"
	}
	return fmt.Sprintf("status %d", code)
}

// stopWork cancels the subagents and kills the background jobs of
// conversations, as the spec's stopWork did. These jobs and subagents sleep
// for minutes.
func (s *shelley) stopWork(conversations ...string) {
	for _, id := range conversations {
		for _, a := range s.subagentIDs(id) {
			s.postStatus("/api/conversation/"+a+"/cancel", nil)
		}
		for _, j := range s.jobIDs(id) {
			s.postStatus("/api/conversation/"+id+"/background-jobs/"+j+"/kill", nil)
		}
	}
}

// fakeAgent is a subagent of parent, made from parent's snapshot entry.
func fakeAgent(parent, fields map[string]any) map[string]any {
	agent := map[string]any{}
	for k, v := range parent {
		agent[k] = v
	}
	agent["parent_conversation_id"] = parent["conversation_id"]
	agent["user_initiated"] = false
	for k, v := range fields {
		agent[k] = v
	}
	return agent
}

// runningWorkNames is the eval that lists the toggle's preview names.
const runningWorkNames = `JSON.stringify([...document.querySelectorAll('[data-testid="running-work-toggle"] .running-work-name')].map(e => e.textContent.replace(/\s+/g, ' ').trim()))`

func TestE2ERunningWorkLoneAgentName(t *testing.T) {
	e2e(t, useful(0), `Running work uses the full preview width for a lone agent name. The test seeded a conversation at /c/rw-lone; the browser is told (through a rewritten conversation list, with live updates cut off) that it has one working subagent, "background-worker-alpha". "The preview" is the ".running-work-name" inside [data-testid="running-work-toggle"].
1. Navigate to /c/rw-lone.
2. The preview should have exactly the text "background-worker-alpha".
3. The preview's scrollWidth - clientWidth should become at most 1 (the name is not cut short).
4. The preview's scrollWidth must be greater than half the clientWidth of its closest ".running-work-summary" (it uses the width it has).`, func(s *shelley) {
		c := s.conversation("echo: ready", "rw-lone")
		s.route("GET /api/stream2", abort)
		s.patchSnapshot(func(snapshot map[string]any, conversation func(string) map[string]any) error {
			parent := conversation(c.ID)
			if parent == nil {
				return errors.New("the conversation is not in the snapshot")
			}
			snapshot["conversations"] = append(snapshot["conversations"].([]any), fakeAgent(parent, map[string]any{
				"conversation_id":         "label-preview-agent",
				"slug":                    "background-worker-alpha",
				"working":                 true,
				"agent_working":           true,
				"running_background_jobs": 0,
			}))
			return nil
		})
	})
}

func TestE2ERunningWorkAgentFirstPreviews(t *testing.T) {
	e2e(t, useful(0), `Running work fits agent-first work previews to the available width. The test seeded a conversation at /c/rw-previews; the browser is told (through rewritten responses, with live updates cut off) that it has three idle subagents, "demo-index", "demo-assets" and "demo-build", each running one background job "printf preview-ready; tail -f /dev/null". "The toggle" is [data-testid="running-work-toggle"]. "The names" are the texts of the toggle's ".running-work-name" elements, in order, whitespace collapsed: eval `+"`"+runningWorkNames+"`"+`. "The overflow" is scrollWidth - clientWidth of the toggle's ".running-work-names".
1. Navigate to /c/rw-previews.
2. The names should be exactly ["demo-index","demo-assets"]. The toggle's ".running-work-more" should have exactly the text "and 4 more", and the toggle should contain "3 tasks · 3 subagents".
3. Set the viewport to 1600x900. The names should become exactly ["demo-index","demo-assets","demo-build","printf preview-ready; tail -f /dev/null","printf preview-ready; tail -f /dev/null","printf preview-ready; tail -f /dev/null"], and there should be no ".running-work-more" in the toggle (count 0). The overflow should become at most 1.
4. Set the viewport to 390x700. The names should become exactly ["demo-index","demo-assets"] again, the toggle's ".running-work-more" exactly "and 4 more", and the overflow should become at most 1.`, func(s *shelley) {
		c := s.conversation("echo: ready", "rw-previews")
		s.route("GET /api/stream2", abort)
		s.route("GET /api/conversation/{id}/background-jobs", func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			if !strings.HasPrefix(id, "preview-agent-") {
				s.pass.ServeHTTP(w, r)
				return
			}
			writeJSON(w, []any{map[string]any{
				"job_id":     "job-" + id,
				"command":    "printf preview-ready; tail -f /dev/null",
				"tail":       "preview-ready",
				"started_at": "2026-10-17T22:00:00Z",
			}})
		})
		s.patchSnapshot(func(snapshot map[string]any, conversation func(string) map[string]any) error {
			parent := conversation(c.ID)
			if parent == nil {
				return errors.New("the conversation is not in the snapshot")
			}
			for i, slug := range []string{"demo-index", "demo-assets", "demo-build"} {
				snapshot["conversations"] = append(snapshot["conversations"].([]any), fakeAgent(parent, map[string]any{
					"conversation_id":         fmt.Sprintf("preview-agent-%d", i+1),
					"slug":                    slug,
					"working":                 false,
					"agent_working":           false,
					"running_background_jobs": 1,
				}))
			}
			return nil
		})
	})
}

func TestE2ERunningWorkSubagentWithBackgroundTask(t *testing.T) {
	e2e(t, useful(0), `Running work keeps a subagent visible while its background task runs. The test seeded a conversation at /c/rw-subagent (ID {{PARENT_ID}}) whose subagent "backgroundhelper" started the background job "printf agent-ready; tail -f /dev/null"; neither agent is working any more. "The strip" is '[aria-label="Running work"]', "the toggle" is its [data-testid="running-work-toggle"], "the names" are the texts of the toggle's ".running-work-name" elements, in order, whitespace collapsed (eval `+"`"+runningWorkNames+"`"+`), "the group" is the strip's '[role="group"][aria-label="Work by backgroundhelper"]', "the row" is the group's ".running-work-agent", and "the job" is the group's [data-testid="background-job"]. "The working indicator" is [data-testid="agent-thinking"], "the parent's dot" is '[data-conversation-id="{{PARENT_ID}}"] .drawer-working-indicator', and "the favicon" is 'link[rel="icon"]'; "dimmed" means the favicon's href contains "opacity%3D%220.35%22". The viewport starts at 1280x800.
1. Navigate to /c/rw-subagent. The toggle should come to contain "1 subagent" (wait up to 30s), and contain "1 task".
2. The names should be exactly ["backgroundhelper","printf agent-ready; tail -f /dev/null"]. The first ".running-work-name"'s scrollWidth - clientWidth should become at most 1. There should be no ".running-work-more" in the toggle (count 0).
3. The working indicator and the parent's dot should be visible, and the favicon dimmed. There should be no ".status-ready" and no ".status-stop-button" (both count 0), and ".status-readout-model" should be enabled.
4. Set the viewport to 390x800. ".status-bar-active > .working-indicator" should be hidden. Click the visible button with aria-label exactly "More options". ".model-picker-menu" should have the class "p-disabled" and the computed opacity "0.5". Press Escape. Set the viewport back to 1280x800.
5. Click the toggle. The row should contain "backgroundhelper" and "Waiting on 1 background task", and the row's ".running-work-agent-time" should contain "Running". The job should contain "printf agent-ready; tail -f /dev/null". The strip should contain no details or summary elements (count 0). The job's [data-testid="background-job-tail"] should contain "agent-ready".
6. Click the toggle (it collapses). The toggle should contain "1 subagent", the names should be exactly ["backgroundhelper","printf agent-ready; tail -f /dev/null"] again, and there should be no ".running-work-more".
7. Click the toggle again. The row should contain "Waiting on 1 background task", and the job's tail should be visible.
8. Call "parentJob" (the parent starts the background job "printf parent-ready; tail -f /dev/null"); it should return "ok".
9. The toggle should come to contain "2 tasks". The strip should have exactly 2 [data-testid="background-job"], and the group exactly 1.
10. Click the job's [data-testid="background-job-kill"]. Call "subagentKill" should return "ok" (the browser's kill request for the subagent's job succeeded).
11. The job's ".running-command-time" should have exactly the text "Finished", the job should have no kill button (count 0), and its tail should still contain "agent-ready". The row's ".running-work-agent-time" should come to have exactly the text "Finished" (wait up to 15s). The toggle should contain "1 task". The working indicator and the parent's dot should still be visible, and the favicon still dimmed. The strip's [data-testid="background-job"] that contains "parent-ready" should have a ".running-command-time" containing "Running".
12. Call "stopWork" (stops all of the parent's work); it returns "done". The toggle should come to contain "No running work". The working indicator and the parent's dot should be gone (count 0), ".status-ready" should be visible, and the favicon no longer dimmed.
13. Click the toggle. The strip should disappear (count 0).`, func(s *shelley) {
		s.viewport(1280, 800)
		c := s.conversation("subagent: backgroundhelper bash-bg: printf agent-ready; tail -f /dev/null", "rw-subagent")
		s.v("PARENT_ID", c.ID)
		var subagent string
		s.poll(30*time.Second, func() error {
			ids := s.subagentIDs(c.ID)
			if len(ids) == 0 {
				return errors.New("no subagent yet")
			}
			subagent = ids[0]
			var sub, parent struct {
				Conversation struct {
					AgentWorking bool `json:"agent_working"`
				} `json:"conversation"`
			}
			s.api("GET", "/api/conversation/"+subagent, nil, &sub)
			s.api("GET", "/api/conversation/"+c.ID, nil, &parent)
			if parent.Conversation.AgentWorking || sub.Conversation.AgentWorking || len(s.jobIDs(subagent)) != 1 {
				return errors.New("the agents are still working, or the subagent's job has not started")
			}
			return nil
		})
		s.call("parentJob", func() string {
			return okStatus(s.postStatus("/api/conversation/"+c.ID+"/chat", map[string]any{
				"message": "bash-bg: printf parent-ready; tail -f /dev/null",
				"model":   "predictable",
			}))
		})
		var mu sync.Mutex
		var kills []int
		s.route("POST /api/conversation/{id}/background-jobs/{job}/kill", func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			s.pass.ServeHTTP(rec, r)
			if r.PathValue("id") == subagent {
				mu.Lock()
				kills = append(kills, rec.status)
				mu.Unlock()
			}
		})
		s.call("subagentKill", func() string {
			mu.Lock()
			defer mu.Unlock()
			if len(kills) == 0 {
				return "no kill request yet"
			}
			return okStatus(kills[0])
		})
		s.call("stopWork", func() string {
			s.stopWork(c.ID)
			return "done"
		})
	})
}

func TestE2ERunningWorkMobileRetainsFinishedJobs(t *testing.T) {
	e2e(t, useful(0), `Running work keeps mobile work visible and retains finished jobs while others run. The test seeded a conversation at /c/rw-mobile that started two background jobs, "echo ready; sleep 600" and "sleep 601". "The strip" is '[aria-label="Running work"]', "the toggle" is its [data-testid="running-work-toggle"], "the items" is the strip's [data-testid="running-work-items"], "the jobs" are the items' [data-testid="background-job"] elements, and "the scroller" is ".messages-container". The viewport is 390x500.
1. Navigate to /c/rw-mobile and wait up to 30s for [data-testid="message-input"] to be visible.
2. The toggle should come to contain "2 tasks" (wait up to 15s). The toggle's ".running-work-name" containing "echo ready; sleep 600" and the one containing "sleep 601" should both be visible. The toggle must not contain "Running:".
3. The strip's nextElementSibling must have the class "messages-bottom-sentinel".
4. Eval: set the scroller's scrollTop to its scrollHeight. Its scrollHeight - clientHeight - scrollTop should become at most 2.
5. In one eval, as a phone tap does: dispatch a bubbling Event "touchstart" on the toggle, then call the toggle's click().
6. There should be exactly 2 jobs, and the job containing "echo ready" and the job containing "sleep 601" should be visible. This should become true: the strip's bottom (getBoundingClientRect) is greater than the top of its closest ".messages-container" and at most that container's bottom (the opened panel is in view).
7. Click the toggle (a real click). It should have aria-expanded "false".
8. In one eval on the scroller: dispatch a bubbling WheelEvent "wheel" with deltaY -20, then subtract 20 from its scrollTop (the reader scrolls up a little). Then record the scroller's scrollTop in a window variable as the reading position.
9. Eval: call the toggle's click(). The items should be visible, and the scroller's scrollTop should stay within 2 of the reading position (poll until |scrollTop - reading position| <= 2).
10. The job containing "echo ready" should have a [data-testid="background-job-tail"] containing "ready". The items should contain no details or summary elements (count 0).
11. Call "killEcho" (kills the "echo ready" job through the API); it should return "ok".
12. The toggle should come to contain "1 task" (wait up to 15s). The "echo ready" job's ".running-command-time" should have exactly the text "Finished", it should have no [data-testid="background-job-kill"] (count 0), and its tail should still contain "ready". The "sleep 601" job's ".running-command-time" should contain "Running".`, func(s *shelley) {
		s.viewport(390, 500)
		c := s.conversation("bash-bg: echo ready; sleep 600", "rw-mobile")
		if code := s.postStatus("/api/conversation/"+c.ID+"/chat", map[string]any{"message": "bash-bg: sleep 601", "model": "predictable"}); code/100 != 2 {
			s.fail("chat: status %d", code)
		}
		s.call("killEcho", func() string {
			var jobs []struct {
				ID      string `json:"job_id"`
				Command string `json:"command"`
			}
			s.api("GET", "/api/conversation/"+c.ID+"/background-jobs", nil, &jobs)
			for _, j := range jobs {
				if strings.Contains(j.Command, "echo ready") {
					return okStatus(s.postStatus("/api/conversation/"+c.ID+"/background-jobs/"+j.ID+"/kill", nil))
				}
			}
			return "no echo ready job"
		})
	})
}

// finishedSubagent is the description of a subagent the test starts from the
// composer and cancels through the API, up to its row reading "Finished".
func finishedSubagent(name string) string {
	return `"The strip" is '[aria-label="Running work"]', "the toggle" is its [data-testid="running-work-toggle"], and "the row" is the strip's ".running-work-agent".
1. Navigate to /c/rw-` + name + ` and wait up to 30s for [data-testid="message-input"] to be visible.
2. Fill the message input with "subagent: ` + name + ` bash: sleep 120" and click [data-testid="send-button"].
3. The toggle should come to contain "` + name + `" (wait up to 30s). Click the toggle.
4. The row should contain "` + name + `"`
}

func TestE2ERunningWorkFinishedSubagentOpensConversation(t *testing.T) {
	e2e(t, useful(0), `Running work keeps a finished subagent row and opens its conversation. The test seeded a conversation at /c/rw-statushelper. `+finishedSubagent("statushelper")+`, and come to contain "sleep" (wait up to 30s).
5. Call "cancelSubagent" (cancels the subagent through the API); it should return "ok".
6. The row's ".running-work-agent-time" should come to have exactly the text "Finished" (wait up to 15s). The toggle should still have aria-expanded "true".
7. Click the row. The URL should come to contain "/c/statushelper" (wait up to 10s).`, func(s *shelley) {
		c := s.conversation("hello there", "rw-statushelper")
		s.call("cancelSubagent", func() string { return s.cancelFirstSubagent(c.ID) })
	})
}

func TestE2ERunningWorkDiscardsFinishedSubagentRows(t *testing.T) {
	e2e(t, useful(0), `Running work discards finished subagent rows when the panel closes. The test seeded a conversation at /c/rw-finishhelper. `+finishedSubagent("finishhelper")+`.
5. Call "cancelSubagent" (cancels the subagent through the API); it should return "ok".
6. The row's ".running-work-agent-time" should come to have exactly the text "Finished" (wait up to 15s).
7. Click the toggle. The strip should disappear (count 0).`, func(s *shelley) {
		c := s.conversation("hello there", "rw-finishhelper")
		s.call("cancelSubagent", func() string { return s.cancelFirstSubagent(c.ID) })
	})
}

// cancelFirstSubagent cancels a conversation's first subagent.
func (s *shelley) cancelFirstSubagent(conversation string) string {
	ids := s.subagentIDs(conversation)
	if len(ids) == 0 {
		return "no subagent"
	}
	return okStatus(s.postStatus("/api/conversation/"+ids[0]+"/cancel", nil))
}
