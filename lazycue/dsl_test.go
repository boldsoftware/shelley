package lazycue

import "testing"

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
