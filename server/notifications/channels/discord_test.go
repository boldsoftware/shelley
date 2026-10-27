package channels

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/server/notifications"
)

type failingDiscordTransport struct{}

func (failingDiscordTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, errors.New("failure for " + r.URL.String())
}

func TestDiscordRequestErrorsDoNotExposeWebhook(t *testing.T) {
	for _, url := range []string{"http://[fake-webhook", "http://localhost/fake-webhook"} {
		d := newDiscord(url)
		d.client.Transport = failingDiscordTransport{}
		err := d.Send(context.Background(), notifications.Event{Type: notifications.EventAgentDone})
		if err == nil {
			t.Fatal("expected request error")
		}
		if strings.Contains(err.Error(), "fake-webhook") || strings.Contains(err.Error(), url) {
			t.Fatalf("webhook exposed in loggable error: %v", err)
		}
	}
}
