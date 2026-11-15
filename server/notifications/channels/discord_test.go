package channels

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
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

type discordTransportFunc func(*http.Request) (*http.Response, error)

func (f discordTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestDiscordTransportFailureDiagnostics(t *testing.T) {
	const secret = "fake-webhook-secret"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"canceled", fmt.Errorf("%s: %w", secret, context.Canceled), "request canceled"},
		{"deadline", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), "timeout"},
		{"network timeout", &net.DNSError{Name: secret, Err: secret, IsTimeout: true}, "timeout"},
		{"DNS", &net.DNSError{Name: secret, Err: secret}, "DNS lookup failed"},
		{"TLS certificate", &tls.CertificateVerificationError{Err: errors.New(secret)}, "TLS certificate verification failed"},
		{"TLS record", tls.RecordHeaderError{Msg: secret}, "TLS protocol error"},
		{"TLS alert", tls.AlertError(40), "TLS protocol error"},
		{"connection", &net.OpError{Op: secret, Net: secret, Err: errors.New(secret)}, "connection failed"},
		{"unknown", errors.New(secret), "transport error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDiscord("https://discord.invalid/api/webhooks/" + secret + "?token=" + secret)
			d.client.Transport = discordTransportFunc(func(r *http.Request) (*http.Response, error) {
				return nil, tt.err
			})
			err := d.Send(context.Background(), notifications.Event{Type: notifications.EventAgentDone})
			want := "send discord webhook failed: " + tt.want
			if err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
			if strings.Contains(fmt.Sprintf("%+v", err), secret) || errors.Unwrap(err) != nil {
				t.Fatalf("transport details exposed in error: %+v", err)
			}
		})
	}
}

func TestDiscordHTTPStatusDoesNotExposeResponse(t *testing.T) {
	d := newDiscord("https://discord.invalid/api/webhooks/fake-webhook-secret")
	d.client.Transport = discordTransportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader("fake-webhook-secret")),
			Header:     make(http.Header),
		}, nil
	})
	err := d.Send(context.Background(), notifications.Event{Type: notifications.EventAgentDone})
	if err == nil || err.Error() != "discord webhook returned 401" {
		t.Fatalf("got %v, want only HTTP status", err)
	}
}
