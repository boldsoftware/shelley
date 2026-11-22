package test

import (
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/conversation-activity.spec.ts. The spec's test ran six
// activity states as steps of one test per theme; each state starts from a
// fresh page, so each is a test of its own here.

// activityState is one of the spec's steps: whether the parent works or
// runs a background job, and whether its subagent does.
type activityState struct {
	name                string
	working             bool
	jobs                int
	subWorking          bool
	subJobs             int
	subRunning, running bool
}

func newActivityState(name string, working bool, jobs int, subWorking bool, subJobs int) activityState {
	st := activityState{name: name, working: working, jobs: jobs, subWorking: subWorking, subJobs: subJobs}
	st.subRunning = subWorking || subJobs > 0
	st.running = working || jobs > 0 || st.subRunning
	return st
}

// activityCentered is an eval that says whether the circle's vertical center
// is within 1px of the line's.
func activityCentered(circle, line string) string {
	return fmt.Sprintf(`(() => { const dot = document.querySelector(%q).getBoundingClientRect(); const box = document.querySelector(%q).getBoundingClientRect(); return Math.abs(dot.y + dot.height / 2 - box.y - box.height / 2) <= 1; })()`, circle, line)
}

// conversationActivity tests the parent conversation's drawer card in one
// activity state and theme.
func conversationActivity(t *testing.T, useful lazycue.Useful, theme string, st activityState) {
	const card = `[data-conversation-id="{{ID}}"]`
	const badge = card + ` .subagent-count-badge`
	var b strings.Builder
	fmt.Fprintf(&b, `Conversation card activity and selection (%s theme, %s). The test has already set everything up before the first step, so steps must not set up, stub or patch anything themselves (no localStorage, fetch or EventSource evals): an init script sets localStorage "shelley-theme" to %q and "shelley-group-by" to "none", and the test answers the page's conversation list requests (/api/conversations/snapshot and /api/conversations/search) and cuts off its live stream (/api/stream2, so the page may show "Reconnecting"). The list holds conversation {{ID}} (/c/{{SLUG}}, the parent) and its one subagent, "activity-child". `, theme, st.name, theme)
	fmt.Fprintf(&b, `The parent is %s; the subagent is %s. `, activityWords(st.working, st.jobs), activityWords(st.subWorking, st.subJobs))
	b.WriteString(`"The card" is ` + card + `, "the badge" is ` + badge + `. To "open the drawer": wait for ".header" to be visible; if the button named exactly "Open conversations" is visible, click it; then ".drawer" should be visible. To "move the mouse away": a hover step with "point": "({x: 0, y: 0})" (the viewport's top-left corner). Perform the steps in order. `)
	steps := []string{
		`Navigate to /new.`,
		`Open the drawer.`,
		`Move the mouse away.`,
		`The card must not have the class "active" (count '` + card + `.active' must be 0).`,
		`Rows stay clear; activity is shown by dots and badges only: the card's computed CSS background-color must be exactly "rgba(0, 0, 0, 0)".`,
		`Click the card's ".conversation-title".`,
		`The URL should become exactly /c/{{SLUG}} (an exact path check).`,
		`Open the drawer.`,
		`Move the mouse away.`,
		`The card should have the class "active" (count '` + card + `.active' must be 1).`,
		`Selection is solid primary regardless of activity: the card's computed background-color must equal the color var(--primary) computes to beside it, so this eval must give true: (() => { const row = document.querySelector('` + card + `'); const probe = document.createElement('div'); probe.style.backgroundColor = 'var(--primary)'; row.parentElement.appendChild(probe); const color = getComputedStyle(probe).backgroundColor; probe.remove(); return getComputedStyle(row).backgroundColor === color; })().`,
	}
	if st.running {
		steps = append(steps,
			`There must be exactly 1 ".drawer-working-indicator" inside the card.`,
			`That indicator should be visible.`,
			`It must be vertically centered on the card's ".drawer-conversation-header-row" (allow 5s for this to hold): this eval must give true: `+activityCentered(card+` .drawer-working-indicator`, card+` .drawer-conversation-header-row`)+`.`)
	} else {
		steps = append(steps, `There must be exactly 0 ".drawer-working-indicator" elements inside the card.`)
	}
	if st.subRunning {
		steps = append(steps,
			`The badge must have exactly the text "1/1".`,
			`There must be exactly 1 element with data-testid "subagent-badge-running" inside the badge.`,
			`It should be visible.`,
			`It must be vertically centered on the badge (allow 5s for this to hold): this eval must give true: `+activityCentered(badge+` [data-testid="subagent-badge-running"]`, badge)+`.`)
	} else {
		steps = append(steps,
			`The badge must have exactly the text "1".`,
			`There must be exactly 0 elements with data-testid "subagent-badge-running" inside the badge.`)
	}
	steps = append(steps,
		`Click the badge.`,
		`".drawer-subagent-list .conversation-item" (the subagent's card) should be visible.`,
		`Its computed CSS background-color must be exactly "rgba(0, 0, 0, 0)".`,
		`It must not have the class "active" (count ".drawer-subagent-list .conversation-item.active" must be 0).`,
		`It must sit below the parent's card: this eval must give true: (() => { const parent = document.querySelector('`+card+`').getBoundingClientRect(); const child = document.querySelector('.drawer-subagent-list .conversation-item').getBoundingClientRect(); return child.y > parent.y + parent.height; })().`,
		`Click the badge again.`,
		`There must then be exactly 0 ".drawer-subagent-list" elements.`)
	for i, step := range steps {
		fmt.Fprintf(&b, "%d. %s ", i+1, step)
	}
	e2e(t, useful, strings.TrimSpace(b.String()), func(s *shelley) {
		s.initScript(fmt.Sprintf(`localStorage.setItem("shelley-theme", %q);
localStorage.setItem("shelley-group-by", "none");`, theme))
		c := s.conversation("echo: activity", "")
		s.v("ID", c.ID)
		s.v("SLUG", c.Slug)
		var snapshot struct {
			Conversations []map[string]any `json:"conversations"`
		}
		s.api("GET", "/api/conversations/snapshot", nil, &snapshot)
		var parent map[string]any
		for _, row := range snapshot.Conversations {
			if row["conversation_id"] == c.ID {
				parent = row
			}
		}
		if parent == nil {
			s.fail("conversation %s is not in the snapshot", c.ID)
		}
		child := maps.Clone(parent)
		maps.Copy(child, map[string]any{
			"conversation_id":        "activity-child",
			"slug":                   "activity-child",
			"parent_conversation_id": c.ID,
			"user_initiated":         false,
			"subagent_count":         0,
		})
		parent["working"], parent["agent_working"] = st.working, st.working
		parent["running_background_jobs"] = st.jobs
		child["working"], child["agent_working"] = st.subWorking, st.subWorking
		child["running_background_jobs"] = st.subJobs
		conversations := []map[string]any{parent, child}
		// Stub list state only; the real parent provides chat content and
		// navigation.
		s.route("/api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"conversations": conversations, "hash": "activity-cards"})
		})
		s.route("/api/conversations/search", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, conversations)
		})
		s.route("/api/conversations/search/", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, conversations)
		})
		s.route("/api/stream2", abort)
		s.route("/api/stream2/", abort)
	})
}

// activityWords says how a conversation is busy.
func activityWords(working bool, jobs int) string {
	switch {
	case working:
		return "working"
	case jobs > 0:
		return "idle but running a background job"
	}
	return "idle"
}

var activityStates = []activityState{
	newActivityState("idle", false, 0, false, 0),
	newActivityState("parent working", true, 0, false, 0),
	newActivityState("subagent working", false, 0, true, 0),
	newActivityState("both working", true, 0, true, 0),
	newActivityState("parent background job", false, 1, false, 0),
	newActivityState("subagent background job", false, 0, false, 1),
}

func TestE2EConversationActivityLightIdle(t *testing.T) {
	conversationActivity(t, useful(0), "light", activityStates[0])
}

func TestE2EConversationActivityLightParentWorking(t *testing.T) {
	conversationActivity(t, useful(0), "light", activityStates[1])
}

func TestE2EConversationActivityLightSubagentWorking(t *testing.T) {
	conversationActivity(t, useful(0), "light", activityStates[2])
}

func TestE2EConversationActivityLightBothWorking(t *testing.T) {
	conversationActivity(t, useful(0), "light", activityStates[3])
}

func TestE2EConversationActivityLightParentBackgroundJob(t *testing.T) {
	conversationActivity(t, useful(0), "light", activityStates[4])
}

func TestE2EConversationActivityLightSubagentBackgroundJob(t *testing.T) {
	conversationActivity(t, useful(0), "light", activityStates[5])
}

func TestE2EConversationActivityDarkIdle(t *testing.T) {
	conversationActivity(t, useful(0), "dark", activityStates[0])
}

func TestE2EConversationActivityDarkParentWorking(t *testing.T) {
	conversationActivity(t, useful(0), "dark", activityStates[1])
}

func TestE2EConversationActivityDarkSubagentWorking(t *testing.T) {
	conversationActivity(t, useful(0), "dark", activityStates[2])
}

func TestE2EConversationActivityDarkBothWorking(t *testing.T) {
	conversationActivity(t, useful(0), "dark", activityStates[3])
}

func TestE2EConversationActivityDarkParentBackgroundJob(t *testing.T) {
	conversationActivity(t, useful(0), "dark", activityStates[4])
}

func TestE2EConversationActivityDarkSubagentBackgroundJob(t *testing.T) {
	conversationActivity(t, useful(0), "dark", activityStates[5])
}
