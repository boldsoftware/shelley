package lazycue

import (
	"encoding/json"
	"fmt"
)

// Step represents a single action in a DSL test script.
type Step struct {
	Action     string `json:"action"`
	URL        string `json:"url,omitempty"`
	Selector   string `json:"selector,omitempty"`
	Value      string `json:"value,omitempty"`
	Text       string `json:"text,omitempty"`
	Timeout    string `json:"timeout,omitempty"`
	Expression string `json:"expression,omitempty"`
	Expect     string `json:"expect,omitempty"`
	Attribute  string `json:"attribute,omitempty"`
	Key        string `json:"key,omitempty"`
	Modifiers  string `json:"modifiers,omitempty"`
	Count      int    `json:"count,omitempty"`
}

// Supported action names.
const (
	ActionNavigate           = "navigate"
	ActionWaitVisible        = "wait_visible"
	ActionWaitHidden         = "wait_hidden"
	ActionWaitText           = "wait_text"
	ActionWaitTextGone       = "wait_text_gone"
	ActionFill               = "fill"
	ActionClick              = "click"
	ActionPressKey           = "press_key"
	ActionScreenshot         = "screenshot"
	ActionEval               = "eval"
	ActionAssertVisible      = "assert_visible"
	ActionAssertNotVisible   = "assert_not_visible"
	ActionAssertText         = "assert_text"
	ActionAssertTextContains = "assert_text_contains"
	ActionAssertAttribute    = "assert_attribute"
	ActionWaitURL            = "wait_url"
	ActionAssertURL          = "assert_url"
	ActionAssertTitle        = "assert_title"
	ActionAssertCount        = "assert_count"
	ActionSleep              = "sleep"
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
	switch s.Action {
	case ActionNavigate:
		return "navigate " + s.URL
	case ActionWaitVisible, ActionWaitHidden, ActionAssertVisible, ActionAssertNotVisible:
		return s.Action + " " + s.Selector
	case ActionWaitText, ActionWaitTextGone:
		return s.Action + " " + truncateArg(s.Text, 40)
	case ActionFill:
		return "fill " + s.Selector + " " + truncateArg(s.Value, 30)
	case ActionClick:
		return "click " + s.Selector
	case ActionPressKey:
		if s.Modifiers != "" {
			return "press_key " + s.Modifiers + "+" + s.Key
		}
		return "press_key " + s.Key
	case ActionScreenshot:
		return "screenshot"
	case ActionEval:
		expr := truncateArg(s.Expression, 40)
		if s.Expect != "" {
			return "eval " + expr + " expect=" + truncateArg(s.Expect, 20)
		}
		return "eval " + expr
	case ActionAssertText:
		return "assert_text " + s.Selector + " " + truncateArg(s.Text, 30)
	case ActionAssertTextContains:
		return "assert_text_contains " + s.Selector + " " + truncateArg(s.Text, 30)
	case ActionAssertAttribute:
		return "assert_attribute " + s.Selector + " " + s.Attribute + "=" + truncateArg(s.Value, 20)
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
		return fmt.Sprintf("assert_count %s %d", s.Selector, s.Count)
	case ActionSleep:
		return "sleep " + s.Timeout
	default:
		return s.Action
	}
}

func truncateArg(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
