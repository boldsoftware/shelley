package lazycue

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Step represents a single action in a DSL test script.
type Step struct {
	Action   string `json:"action"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	// HasText narrows a selector to the elements whose text contains it,
	// ignoring case, whitespace collapsed.
	HasText string `json:"has_text,omitempty"`
	// Role and Name pick elements by ARIA role and accessible name, as
	// Playwright's getByRole, inside what Selector matches when set.
	Role string `json:"role,omitempty"`
	Name string `json:"name,omitempty"`
	// Exact makes Name and HasText match the whole text, case-sensitively.
	Exact bool `json:"exact,omitempty"`
	// Visible keeps only the visible matches.
	Visible bool `json:"visible,omitempty"`
	// Nth, when set, keeps only the match at that index once every other
	// field has narrowed them, as Playwright's nth: 0 is the first, -1 the
	// last.
	Nth *int `json:"nth,omitempty"`
	// Within keeps only the matches inside an element it picks out, as
	// Playwright's chained locators do.
	Within *Within `json:"within,omitempty"`
	// Frame is a CSS selector for an iframe in the page, whose document the
	// step acts in, as Playwright's frameLocator.
	Frame      string  `json:"frame,omitempty"`
	Value      string  `json:"value,omitempty"`
	Text       string  `json:"text,omitempty"`
	Timeout    string  `json:"timeout,omitempty"`
	Expression string  `json:"expression,omitempty"`
	Expect     *string `json:"expect,omitempty"` // set, even to "", makes eval and call assertions
	Attribute  string  `json:"attribute,omitempty"`
	Key        string  `json:"key,omitempty"`
	Modifiers  string  `json:"modifiers,omitempty"`
	Count      int     `json:"count,omitempty"`
	// Texts are what an assert_texts step expects of its matches, in order.
	Texts []string `json:"texts,omitempty"`
	// Ratio is the share of an element an assert_in_viewport step needs in
	// the viewport, or an assert_not_in_viewport step forbids; unset is any.
	Ratio float64 `json:"ratio,omitempty"`
	// Point is a JS expression for where a pointer step aims, computed as it
	// runs: an {x, y} in viewport CSS pixels, in place of a selector, role, x
	// and y. Nothing there is waited for, as with Playwright's page.mouse.
	Point string `json:"point,omitempty"`
	// X and Y are a pointer step's offset in CSS pixels from the element's
	// top-left corner (unset means its center), a point in the viewport
	// without a selector, or a wheel step's scroll distances.
	X *float64 `json:"x,omitempty"`
	Y *float64 `json:"y,omitempty"`
	// Tab names the browser tab the step acts on; empty is the tab the browser
	// started with. A navigate step to a name not yet open opens that tab.
	Tab string `json:"tab,omitempty"`
	// Button is the mouse button of a click, mouse_down or mouse_up step:
	// left (the default), right or middle.
	Button string `json:"button,omitempty"`
	// Points lists a touch step's fingers as "x,y" offsets in CSS pixels from
	// the element's top-left corner (or from Point), separated by spaces;
	// unset is one finger at x,y (or the element's center).
	Points string `json:"points,omitempty"`
	// Width, Height, Scale, Mobile and Touch describe a set_viewport step's
	// device; unset fields keep their current value.
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	Scale  float64 `json:"scale,omitempty"`
	Mobile *bool   `json:"mobile,omitempty"`
	Touch  *bool   `json:"touch,omitempty"`
	// Steps are a retry step's steps.
	Steps []Step `json:"steps,omitempty"`
}

// Within picks out elements as a step does, for the step to look inside.
type Within struct {
	Selector string `json:"selector,omitempty"`
	Role     string `json:"role,omitempty"`
	Name     string `json:"name,omitempty"`
	HasText  string `json:"has_text,omitempty"`
	Exact    bool   `json:"exact,omitempty"`
	Visible  bool   `json:"visible,omitempty"`
	Nth      *int   `json:"nth,omitempty"`
}

// step is a step that picks out what w does.
func (w Within) step() Step {
	return Step{Selector: w.Selector, Role: w.Role, Name: w.Name, HasText: w.HasText, Exact: w.Exact, Visible: w.Visible, Nth: w.Nth}
}

// Supported action names.
const (
	ActionNavigate              = "navigate"
	ActionReload                = "reload"
	ActionGoBack                = "go_back"
	ActionGoForward             = "go_forward"
	ActionWaitVisible           = "wait_visible"
	ActionWaitHidden            = "wait_hidden"
	ActionWaitText              = "wait_text"
	ActionWaitTextGone          = "wait_text_gone"
	ActionFill                  = "fill"
	ActionClick                 = "click"
	ActionHover                 = "hover"
	ActionPressKey              = "press_key"
	ActionKeyDown               = "key_down"
	ActionKeyUp                 = "key_up"
	ActionScreenshot            = "screenshot"
	ActionEval                  = "eval"
	ActionAssertVisible         = "assert_visible"
	ActionAssertNotVisible      = "assert_not_visible"
	ActionAssertText            = "assert_text"
	ActionAssertTextContains    = "assert_text_contains"
	ActionAssertTextNotContains = "assert_text_not_contains"
	ActionAssertTexts           = "assert_texts"
	ActionAssertInViewport      = "assert_in_viewport"
	ActionAssertNotInViewport   = "assert_not_in_viewport"
	ActionAssertAttribute       = "assert_attribute"
	ActionAssertValue           = "assert_value"
	ActionAssertEnabled         = "assert_enabled"
	ActionAssertDisabled        = "assert_disabled"
	ActionAssertFocused         = "assert_focused"
	ActionWaitURL               = "wait_url"
	ActionAssertURL             = "assert_url"
	ActionAssertTitle           = "assert_title"
	ActionAssertCount           = "assert_count"
	ActionSleep                 = "sleep"
	ActionCloseTab              = "close_tab"
	ActionSetViewport           = "set_viewport"
	ActionEmulateMedia          = "emulate_media"
	ActionCall                  = "call"
	ActionType                  = "type"
	ActionInsertText            = "insert_text"
	ActionMouseDown             = "mouse_down"
	ActionMouseUp               = "mouse_up"
	ActionWheel                 = "wheel"
	ActionTouch                 = "touch"
	ActionCollectGarbage        = "collect_garbage"
	ActionRetry                 = "retry"
)

// ParseSteps parses a JSON array of steps.
func ParseSteps(data []byte) ([]Step, error) {
	var steps []Step
	if err := json.Unmarshal(data, &steps); err != nil {
		return nil, err
	}
	return steps, nil
}

// FormatSteps serializes steps to indented JSON.
func FormatSteps(steps []Step) ([]byte, error) {
	return json.MarshalIndent(steps, "", "  ")
}

// StepSummary returns a short human-readable summary of a step, e.g.
// "navigate /new" or "click #login-button" or "assert_text .title \"Hello\"".
func StepSummary(s Step) string {
	if s.Tab != "" {
		return "[" + s.Tab + "] " + stepSummary(s)
	}
	return stepSummary(s)
}

func stepSummary(s Step) string {
	switch s.Action {
	case ActionNavigate:
		if s.URL == "" && s.Tab != "" {
			return "navigate to the first tab's page"
		}
		return "navigate " + s.URL
	case ActionWaitVisible, ActionWaitHidden, ActionAssertVisible, ActionAssertNotVisible:
		return s.Action + " " + describe(s)
	case ActionWaitText, ActionWaitTextGone:
		return s.Action + " " + truncateArg(s.Text, 40) + inFrame(s)
	case ActionFill:
		return "fill " + describe(s) + " " + truncateArg(s.Value, 30)
	case ActionClick:
		return "click " + describe(s) + pointSuffix(s)
	case ActionHover:
		return "hover " + describe(s) + pointSuffix(s)
	case ActionMouseDown, ActionMouseUp:
		out := s.Action
		if s.Point != "" {
			out += " " + describe(s)
		}
		if s.Button != "" {
			out += " " + s.Button
		}
		return out
	case ActionWheel:
		out := "wheel"
		if s.X != nil && s.Y != nil {
			out += fmt.Sprintf(" %g,%g", *s.X, *s.Y)
		}
		if s.Point != "" {
			out += " at " + describe(s)
		}
		return out
	case ActionTouch:
		if s.Points != "" {
			return "touch " + s.Value + " " + describe(s) + " at " + s.Points + countSuffix(s)
		}
		return "touch " + s.Value + " " + describe(s) + pointSuffix(s)
	case ActionType, ActionInsertText:
		return s.Action + " " + truncateArg(s.Value, 30)
	case ActionCall:
		if s.Expect != nil {
			return "call " + s.Value + " expect=" + truncateArg(*s.Expect, 30)
		}
		return "call " + s.Value
	case ActionSetViewport:
		summary := fmt.Sprintf("set_viewport %dx%d", s.Width, s.Height)
		if s.Scale != 0 {
			summary += fmt.Sprintf(" scale=%g", s.Scale)
		}
		if s.Mobile != nil {
			summary += fmt.Sprintf(" mobile=%t", *s.Mobile)
		}
		if s.Touch != nil {
			summary += fmt.Sprintf(" touch=%t", *s.Touch)
		}
		return summary
	case ActionEmulateMedia:
		return s.Action + " " + s.Value
	case ActionKeyDown, ActionKeyUp:
		return s.Action + " " + s.Key
	case ActionPressKey:
		if s.Modifiers != "" {
			return "press_key " + s.Modifiers + "+" + s.Key
		}
		return "press_key " + s.Key
	case ActionScreenshot:
		return "screenshot"
	case ActionEval:
		expr := truncateArg(s.Expression, 40) + inFrame(s)
		if s.Expect != nil {
			return "eval " + expr + " expect=" + truncateArg(*s.Expect, 20)
		}
		return "eval " + expr
	case ActionAssertText:
		return "assert_text " + describe(s) + " " + truncateArg(s.Text, 30)
	case ActionAssertTextContains, ActionAssertTextNotContains:
		return s.Action + " " + describe(s) + " " + truncateArg(s.Text, 30)
	case ActionAssertTexts:
		return "assert_texts " + describe(s) + " " + truncateArg(quoteList(s.Texts), 40)
	case ActionAssertInViewport, ActionAssertNotInViewport:
		if s.Ratio != 0 {
			return fmt.Sprintf("%s %s ratio %g", s.Action, describe(s), s.Ratio)
		}
		return s.Action + " " + describe(s)
	case ActionAssertAttribute:
		return "assert_attribute " + describe(s) + " " + s.Attribute + "=" + truncateArg(s.Value, 20)
	case ActionAssertValue:
		return "assert_value " + describe(s) + " " + truncateArg(s.Value, 30)
	case ActionAssertEnabled, ActionAssertDisabled, ActionAssertFocused:
		return s.Action + " " + describe(s)
	case ActionWaitURL:
		if s.Value != "" {
			return s.Action + " " + s.Value
		}
		return s.Action + " ~" + s.Text
	case ActionAssertURL:
		if s.Value != "" {
			return "assert_url " + s.Value
		}
		return "assert_url contains " + truncateArg(s.Text, 30)
	case ActionAssertTitle:
		return "assert_title " + truncateArg(s.Text, 30)
	case ActionAssertCount:
		return fmt.Sprintf("assert_count %s %d", describe(s), s.Count)
	case ActionSleep:
		return "sleep " + s.Timeout
	case ActionRetry:
		var inner []string
		for _, n := range s.Steps {
			inner = append(inner, StepSummary(n))
		}
		return "retry {" + strings.Join(inner, "; ") + "}"
	default:
		return s.Action
	}
}

// describe names what a step's selector, role, name, has_text, visible, nth,
// within and frame pick out, e.g. `button "Archive" in nav`, or its point.
func describe(s Step) string {
	if s.Point != "" {
		return "point " + truncateArg(s.Point, 40)
	}
	d := s.Selector
	if s.Role != "" {
		d = s.Role
		if s.Name != "" {
			d += fmt.Sprintf(" %q", s.Name)
		}
		if s.Selector != "" {
			d += " in " + s.Selector
		}
	}
	if s.HasText != "" {
		d += fmt.Sprintf(" with text %q", s.HasText)
	}
	if s.Visible {
		d = "visible " + d
	}
	if s.Nth != nil {
		d += fmt.Sprintf(" nth %d", *s.Nth)
	}
	if s.Within != nil {
		d += " within " + describe(s.Within.step())
	}
	return d + inFrame(s)
}

// inFrame names a step's frame, if it has one.
func inFrame(s Step) string {
	if s.Frame == "" {
		return ""
	}
	return " in frame " + s.Frame
}

// pointSuffix describes a pointer step's offset, button, click count and
// modifiers.
func pointSuffix(s Step) string {
	var out string
	if s.Modifiers != "" {
		out += " with " + s.Modifiers
	}
	if s.X != nil && s.Y != nil {
		out += fmt.Sprintf(" at %g,%g", *s.X, *s.Y)
	}
	if s.Button != "" {
		out += " " + s.Button
	}
	return out + countSuffix(s)
}

func countSuffix(s Step) string {
	if s.Count > 1 {
		return fmt.Sprintf(" x%d", s.Count)
	}
	return ""
}

// quoteList quotes ss in a list, e.g. ["a","b"].
func quoteList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ",") + "]"
}

// collapse collapses runs of whitespace in s to one space and trims it.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateArg(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
