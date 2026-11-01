package loop

import (
	"context"
	"fmt"
	"strings"

	"shelley.exe.dev/llm"
)

type modelRouting struct {
	served map[string]string
}

type RoutingNotice struct {
	Title     string `json:"title"`
	From      string `json:"from"`
	To        string `json:"to"`
	Detail    string `json:"detail"`
	Recovered bool   `json:"recovered"`
}

func (n RoutingNotice) Text() string {
	return fmt.Sprintf("%s\n%s → %s\n%s", n.Title, n.From, n.To, n.Detail)
}

type RoutingNoticeRecordFunc func(context.Context, RoutingNotice) error

func claudeModelLabel(id string) string {
	family, major, minor, ok := llm.ParseClaudeModel(id)
	if !ok {
		return ""
	}
	version := fmt.Sprint(major)
	if minor != 0 {
		version += fmt.Sprintf(".%d", minor)
	}
	return fmt.Sprintf("Claude %s %s", strings.ToUpper(family[:1])+family[1:], version)
}

func (r *modelRouting) observe(ctx context.Context, response *llm.Response, warn RoutingNoticeRecordFunc) error {
	if response.Origin == nil || warn == nil {
		return nil
	}
	requested := claudeModelLabel(response.Origin.Model)
	served := claudeModelLabel(response.Model)
	if requested == "" || served == "" {
		return nil
	}
	previous, seen := r.served[requested]
	if !seen {
		previous = requested
	}
	if previous != served {
		notice := RoutingNotice{
			Title:     "Provider routed this response",
			From:      previous,
			To:        served,
			Detail:    fmt.Sprintf("You requested %s. The provider did not supply a routing reason.", requested),
			Recovered: served == requested,
		}
		if notice.Recovered {
			notice.Title = "Back on your requested model"
			notice.Detail = "The provider is serving " + requested + " again."
		}
		if err := warn(ctx, notice); err != nil {
			return err
		}
	}
	if r.served == nil {
		r.served = make(map[string]string)
	}
	r.served[requested] = served
	return nil
}
