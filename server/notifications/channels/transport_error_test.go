package channels

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"shelley.exe.dev/server/notifications"
)

func TestTransportErrorCategory(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"timeout", &url.Error{Op: "Get", URL: "https://user:secret@example.test", Err: &net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}}}, "timeout"},
		{"cancellation", context.Canceled, "request canceled"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"DNS", &url.Error{Op: "Get", URL: "https://secret.example", Err: &net.DNSError{Err: "lookup failed", Name: "secret.example"}}, "DNS lookup failed"},
		{"DNS timeout", &net.DNSError{Err: "timeout", Name: "secret.example", IsTimeout: true}, "timeout"},
		{"unknown authority value", &url.Error{Op: "Get", URL: "https://secret.example", Err: fmt.Errorf("wrapped: %w", x509.UnknownAuthorityError{Cert: &x509.Certificate{}})}, "TLS certificate verification failed"},
		{"unknown authority pointer", &url.Error{Op: "Get", URL: "https://secret.example", Err: wrappedError(&x509.UnknownAuthorityError{Cert: &x509.Certificate{}})}, "TLS certificate verification failed"},
		{"hostname error", &url.Error{Op: "Get", URL: "https://secret.example", Err: fmt.Errorf("wrapped: %w", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "secret.example"})}, "TLS certificate verification failed"},
		{"hostname error pointer", &url.Error{Op: "Get", URL: "https://secret.example", Err: wrappedError(&x509.HostnameError{Certificate: &x509.Certificate{}, Host: "secret.example"})}, "TLS certificate verification failed"},
		{"invalid certificate", &url.Error{Op: "Get", URL: "https://secret.example", Err: fmt.Errorf("wrapped: %w", x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired})}, "TLS certificate verification failed"},
		{"invalid certificate pointer", &url.Error{Op: "Get", URL: "https://secret.example", Err: wrappedError(&x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired})}, "TLS certificate verification failed"},
		{"TLS record", tls.RecordHeaderError{Msg: "secret"}, "TLS protocol error"},
		{"TLS alert", &url.Error{Op: "Get", URL: "https://secret.example", Err: tls.AlertError(40)}, "TLS protocol error"},
		{"connection", &url.Error{Op: "Get", URL: "https://secret.example", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}}, "connection failed"},
		{"unknown", errors.New("sensitive details"), "transport error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := transportErrorCategory(tt.err); got != tt.want {
				t.Fatalf("transportErrorCategory() = %q, want %q", got, tt.want)
			}
		})
	}
}

func wrappedError(err error) error {
	return fmt.Errorf("wrapped: %w", err)
}

func TestNotificationTransportErrorsDoNotExposeCredentials(t *testing.T) {
	const secret = "credential-sentinel"
	transport := failingTransport{err: fmt.Errorf("transport failed for %s", secret)}

	ntfyChannel := &ntfy{
		server: "https://user:" + secret + "@ntfy.example/" + secret,
		topic:  "topic",
		client: &http.Client{Transport: transport},
	}
	emailChannel := newEmail(secret)
	emailChannel.client.Transport = transport

	for _, channel := range []notifications.Channel{ntfyChannel, emailChannel} {
		t.Run(channel.Name(), func(t *testing.T) {
			err := channel.Send(context.Background(), notifications.Event{Type: notifications.EventAgentDone})
			if err == nil {
				t.Fatal("expected transport error")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "ntfy.example") {
				t.Fatalf("credential or URL leaked in loggable error: %v", err)
			}
			if !strings.HasSuffix(err.Error(), ": transport error") {
				t.Fatalf("expected safe transport category, got: %v", err)
			}
		})
	}
}

type failingTransport struct {
	err error
}

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout with sensitive details" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
