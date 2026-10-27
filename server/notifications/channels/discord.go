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

func init() {
	notifications.Register("discord", func(config map[string]any, logger *slog.Logger) (notifications.Channel, error) {
		url, ok := config["webhook_url"].(string)
		if !ok || url == "" {
			return nil, fmt.Errorf("discord channel requires \"webhook_url\"")
		}
		return newDiscord(url), nil
	})
}

type discord struct {
	webhookURL string
	client     *http.Client
}

func newDiscord(webhookURL string) *discord {
	return &discord{
		webhookURL: webhookURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (d *discord) Name() string { return "discord" }

func (d *discord) Send(ctx context.Context, event notifications.Event) error {
	msg := formatDiscordMessage(event)
	if msg == nil {
		return nil
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create discord request failed")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("send discord webhook failed")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("discord webhook returned %d", resp.StatusCode)
	}
	return nil
}

type discordMessage struct {
	Embeds []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string `json:"title"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	Color       int    `json:"color"`
	Timestamp   string `json:"timestamp,omitempty"`
}

// discordMaxDescription is Discord's embed description limit.
const discordMaxDescription = 4096

func formatDiscordMessage(event notifications.Event) *discordMessage {
	switch event.Type {
	case notifications.EventAgentDone:
		embed := discordEmbed{
			Color:     0x22c55e, // green
			Timestamp: event.Timestamp.Format(time.RFC3339),
		}
		if p, ok := event.Payload.(notifications.AgentDonePayload); ok {
			embed.Title = notifications.Title(p.Hostname, p.ConversationTitle)
			embed.URL = p.ConversationURL
			if p.FinalResponse != "" {
				embed.Description = p.FinalResponse
				if len(embed.Description) > discordMaxDescription {
					embed.Description = embed.Description[:discordMaxDescription-3] + "..."
				}
			}
		} else {
			embed.Title = "Agent finished"
		}
		return &discordMessage{Embeds: []discordEmbed{embed}}

	case notifications.EventAgentError:
		embed := discordEmbed{
			Color:     0xef4444, // red
			Timestamp: event.Timestamp.Format(time.RFC3339),
		}
		if p, ok := event.Payload.(notifications.AgentErrorPayload); ok {
			embed.Title = notifications.Title(p.Hostname, "error")
			embed.URL = p.ConversationURL
			if p.ErrorMessage != "" {
				embed.Description = p.ErrorMessage
			}
		} else {
			embed.Title = "Agent error"
		}
		return &discordMessage{Embeds: []discordEmbed{embed}}

	default:
		return nil
	}
}
