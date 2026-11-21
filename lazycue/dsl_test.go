package lazycue

import (
	"strings"
	"testing"
)

func TestStepSummaries(t *testing.T) {
	for _, c := range []struct {
		step Step
		want string
	}{
		{Step{Action: ActionClick, Selector: "#send"}, "click #send"},
		{Step{Action: ActionClick, Selector: "#send", Tab: "B"}, "[B] click #send"},
		{Step{Action: ActionCloseTab, Tab: "B"}, "[B] close_tab"},
		{Step{Action: ActionNavigate, Tab: "B"}, "[B] navigate to the first tab's page"},
		{Step{Action: ActionClick, Frame: "#embed", Role: "button", Name: "Go"}, `click button "Go" in frame #embed`},
		{Step{Action: ActionEval, Frame: "iframe", Expression: "document.title", Expect: ptr("Hi")}, "eval document.title in frame iframe expect=Hi"},
		{Step{Action: ActionWaitTextGone, Frame: "iframe", Text: "Loading"}, "wait_text_gone Loading in frame iframe"},
		{Step{Action: ActionRetry, Steps: []Step{{Action: ActionClick, Selector: "#copy"}, {Action: ActionAssertValue, Selector: "#t", Value: "out", Tab: "B"}}}, "retry {click #copy; [B] assert_value #t out}"},
	} {
		if got := StepSummary(c.step); got != c.want {
			t.Errorf("StepSummary(%+v) = %q, want %q", c.step, got, c.want)
		}
	}
}

// A frame's placeholders are filled in.
func TestSubstituteFrame(t *testing.T) {
	got, err := substitute(Step{Action: ActionClick, Frame: "#{{ID}}", Selector: "button"}, map[string]string{"ID": "embed"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Frame != "#embed" {
		t.Errorf("frame = %q", got.Frame)
	}
}

// Placeholders in within are filled in, on a copy.
func TestSubstituteWithin(t *testing.T) {
	step := Step{Action: ActionClick, Selector: "button", Within: &Within{Selector: "#{{ID}}", Role: "{{ROLE}}", Name: "{{NAME}}", HasText: "{{TEXT}}"}}
	got, err := substitute(step, map[string]string{"ID": "card", "ROLE": "listitem", "NAME": "Build", "TEXT": "Failed"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Within{Selector: "#card", Role: "listitem", Name: "Build", HasText: "Failed"}); *got.Within != want {
		t.Errorf("within = %+v, want %+v", *got.Within, want)
	}
	if step.Within.Selector != "#{{ID}}" {
		t.Errorf("substitute changed the step's within: %+v", *step.Within)
	}
	if _, err := substitute(step, nil); err == nil || !strings.Contains(err.Error(), "no value for {{ID}}") {
		t.Errorf("missing placeholder in within: %v", err)
	}
}
