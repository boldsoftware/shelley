package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"shelley.exe.dev/server/notifications"
)

var ntfyPriorities = map[string]int{
	"min":     1,
	"low":     2,
	"default": 3,
	"high":    4,
	"max":     5,
}

func init() {
	notifications.Register("ntfy", func(config map[string]any, logger *slog.Logger) (notifications.Channel, error) {
		server, _ := config["server"].(string)
		if server == "" {
			return nil, fmt.Errorf("ntfy channel requires \"server\"")
		}

		topic, _ := config["topic"].(string)
		if topic == "" {
			return nil, fmt.Errorf("ntfy channel requires \"topic\"")
		}

		token, _ := config["token"].(string)
		username, _ := config["username"].(string)
		password, _ := config["password"].(string)

		donePriorityStr, _ := config["done_priority"].(string)
		if donePriorityStr == "" {
			donePriorityStr = "default"
		}
		donePriority, ok := ntfyPriorities[donePriorityStr]
		if !ok {
			return nil, fmt.Errorf("ntfy channel: invalid done_priority %q", donePriorityStr)
		}

		errorPriorityStr, _ := config["error_priority"].(string)
		if errorPriorityStr == "" {
			errorPriorityStr = "high"
		}
		errorPriority, ok := ntfyPriorities[errorPriorityStr]
		if !ok {
			return nil, fmt.Errorf("ntfy channel: invalid error_priority %q", errorPriorityStr)
		}

		return &ntfy{
			server:        server,
			topic:         topic,
			token:         token,
			username:      username,
			password:      password,
			donePriority:  donePriority,
			errorPriority: errorPriority,
			client: &http.Client{
				Timeout: 10 * time.Second,
			},
		}, nil
	})
}

type ntfy struct {
	server        string
	topic         string
	token         string
	username      string
	password      string
	donePriority  int
	errorPriority int
	client        *http.Client
}

func (n *ntfy) Name() string { return "ntfy" }

type ntfyMessage struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
}

func (n *ntfy) Send(ctx context.Context, event notifications.Event) error {
	msg := n.formatMessage(event)
	if msg == nil {
		return nil
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal ntfy payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.server, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create ntfy request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	} else if n.username != "" && n.password != "" {
		req.SetBasicAuth(n.username, n.password)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("send ntfy notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if len(b) > 0 {
			return fmt.Errorf("ntfy server returned %s: %s", resp.Status, bytes.TrimSpace(b))
		}
		return fmt.Errorf("ntfy server returned %s", resp.Status)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (n *ntfy) formatMessage(event notifications.Event) *ntfyMessage {
	switch event.Type {
	case notifications.EventAgentDone:
		msg := &ntfyMessage{
			Topic:    n.topic,
			Priority: n.donePriority,
			Tags:     []string{"white_check_mark"},
		}
		if p, ok := event.Payload.(notifications.AgentDonePayload); ok {
			if p.ConversationTitle != "" {
				msg.Title = fmt.Sprintf("Agent finished: %s", p.ConversationTitle)
			} else {
				msg.Title = "Agent finished"
			}
			var body string
			if p.Model != "" {
				body = fmt.Sprintf("Model: %s", p.Model)
			}
			if p.FinalResponse != "" {
				if body != "" {
					body += "\n"
				}
				body += p.FinalResponse
			}
			msg.Message = body
		} else {
			msg.Title = "Agent finished"
		}
		return msg

	case notifications.EventAgentError:
		msg := &ntfyMessage{
			Topic:    n.topic,
			Title:    "Agent error",
			Priority: n.errorPriority,
			Tags:     []string{"x"},
		}
		if p, ok := event.Payload.(notifications.AgentErrorPayload); ok && p.ErrorMessage != "" {
			msg.Message = p.ErrorMessage
		}
		return msg

	default:
		return nil
	}
}
