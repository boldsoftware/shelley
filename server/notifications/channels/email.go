package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"shelley.exe.dev/server/notifications"
)

const emailGatewayURL = "http://169.254.169.254/gateway/email/send"

func init() {
	notifications.Register("email", func(config map[string]any, logger *slog.Logger) (notifications.Channel, error) {
		to, ok := config["to"].(string)
		if !ok || to == "" {
			return nil, fmt.Errorf("email channel requires \"to\"")
		}
		return newEmail(to), nil
	})
}

type email struct {
	to     string
	client *http.Client
}

func newEmail(to string) *email {
	return &email{
		to: to,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (e *email) Name() string { return "email" }

func (e *email) Send(ctx context.Context, event notifications.Event) error {
	subject, body := formatEmailMessage(event)
	if subject == "" {
		return nil
	}

	payload, err := json.Marshal(map[string]string{
		"to":      e.to,
		"subject": subject,
		"body":    body,
	})
	if err != nil {
		return fmt.Errorf("marshal email payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, emailGatewayURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create email request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("send email failed: %s", transportErrorCategory(err))
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read email response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("email gateway returned %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(respBody, &result) == nil && result.Error != "" {
		return fmt.Errorf("email gateway error: %s", result.Error)
	}

	return nil
}

func formatEmailMessage(event notifications.Event) (subject, body string) {
	switch event.Type {
	case notifications.EventAgentDone:
		if p, ok := event.Payload.(notifications.AgentDonePayload); ok {
			subject = notifications.Title(p.Hostname, p.ConversationTitle)
			var parts []string
			if p.ConversationURL != "" {
				parts = append(parts, p.ConversationURL)
			}
			if p.FinalResponse != "" {
				parts = append(parts, "", p.FinalResponse)
			}
			body = strings.Join(parts, "\n")
		} else {
			subject = "Agent finished"
		}
		return subject, body

	case notifications.EventAgentError:
		if p, ok := event.Payload.(notifications.AgentErrorPayload); ok {
			subject = notifications.Title(p.Hostname, "error")
			var parts []string
			if p.ConversationURL != "" {
				parts = append(parts, p.ConversationURL)
			}
			if p.ErrorMessage != "" {
				parts = append(parts, p.ErrorMessage)
			}
			body = strings.Join(parts, "\n")
		} else {
			subject = "Agent error"
		}
		return subject, body

	default:
		return "", ""
	}
}
