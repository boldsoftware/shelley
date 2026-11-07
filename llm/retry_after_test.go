package llm

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		// when in is an HTTP-date we can't predict exactly; minWant is a lower bound and 0 means "no bound".
	}{
		{"", 0},
		{"0", 0},
		{"7", 7 * time.Second},
		{"60", 60 * time.Second},
		{"-1", 0},
		{"garbage", 0},
	}
	for _, tc := range tests {
		if got := ParseRetryAfter(tc.in); got != tc.want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	// HTTP-date: ~30s in the future.
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	got := ParseRetryAfter(future)
	if got < 25*time.Second || got > 35*time.Second {
		t.Errorf("ParseRetryAfter(future date) = %v, want ~30s", got)
	}

	// HTTP-date in the past should return 0.
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if got := ParseRetryAfter(past); got != 0 {
		t.Errorf("ParseRetryAfter(past date) = %v, want 0", got)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"hello", 5, "hello"},
		{"hello world", 5, "hello\u2026"},
		{"  hello   world  ", 11, "hello world"},
		{"foo\nbar\tbaz", 32, "foo bar baz"},
		// Multi-byte runes: é is 2 bytes; ensure n counts runes and we never split mid-rune.
		{"caf\u00e9 society", 4, "caf\u00e9\u2026"},
		{"caf\u00e9 society", 3, "caf\u2026"},
		// 4-byte rune (emoji).
		{"abc\U0001F600def", 4, "abc\U0001F600\u2026"},
		{"abc\U0001F600def", 3, "abc\u2026"},
		// All multi-byte, no truncation needed.
		{"\u00e9\u00e9\u00e9", 3, "\u00e9\u00e9\u00e9"},
	}
	for _, tc := range tests {
		if got := Truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestRetryDelay(t *testing.T) {
	backoff := []time.Duration{2 * time.Second, 10 * time.Second}
	for _, tc := range []struct {
		n          int
		retryAfter time.Duration
		lo, hi     time.Duration
	}{
		{1, 0, 2 * time.Second, 3 * time.Second},
		{5, 0, 10 * time.Second, 11 * time.Second},               // the last entry repeats
		{1, time.Minute, time.Minute, time.Minute},               // Retry-After wins when longer
		{2, 5 * time.Second, 10 * time.Second, 11 * time.Second}, // but never shortens the wait
	} {
		if got := RetryDelay(backoff, tc.n, tc.retryAfter); got < tc.lo || got > tc.hi {
			t.Errorf("RetryDelay(n=%d, retryAfter=%v) = %v, want in [%v, %v]", tc.n, tc.retryAfter, got, tc.lo, tc.hi)
		}
	}
	if got := RetryDelay([]time.Duration{0}, 3, 0); got != 0 {
		t.Errorf("RetryDelay with zero backoff = %v, want 0", got)
	}
}
