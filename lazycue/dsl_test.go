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
		{Step{Action: ActionRetry, Steps: []Step{{Action: ActionClick, Selector: "#copy"}, {Action: ActionAssertValue, Selector: "#t", Value: "out", Tab: "B"}}}, "retry {click #copy; [B] assert_value #t out}"},
	} {
		if got := StepSummary(c.step); got != c.want {
			t.Errorf("StepSummary(%+v) = %q, want %q", c.step, got, c.want)
		}
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
