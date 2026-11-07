package llm

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ParseRetryAfter parses a Retry-After header value (RFC 7231 §7.1.3).
// It accepts either delta-seconds or an HTTP-date. Returns 0 if the header
// is missing or unparseable.
func ParseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// Truncate clips s to at most n runes, appending an ellipsis when truncated.
// Whitespace runs are collapsed to single spaces so it renders well in a
// single-line log field. n counts runes (not bytes) and the cut always lands
// on a rune boundary, so multi-byte UTF-8 sequences are never split.
func Truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i, count := 0, 0
	for i < len(s) && count < n {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		count++
	}
	return s[:i] + "…"
}

// DefaultBackoff is the retry schedule for provider requests. Its long tail
// is deliberate: providers regularly have multi-hour incidents, and giving
// up after a few minutes is a worse UX than waiting.
var DefaultBackoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	1 * time.Minute,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
	20 * time.Minute,
	30 * time.Minute,
}

// RetryDelay is how long to wait before retry n (1 for the first retry):
// backoff's nth entry (the last one repeats) plus up to a second of jitter,
// raised to retryAfter when the server asked for longer.
func RetryDelay(backoff []time.Duration, n int, retryAfter time.Duration) time.Duration {
	base := backoff[min(n-1, len(backoff)-1)]
	jitter := time.Duration(rand.Int64N(max(min(int64(base), int64(time.Second)), 1)))
	return max(base+jitter, retryAfter)
}

// WaitRetry reports ev to r.OnRetry and sleeps for ev.Sleep. It returns
// ctx.Err() if ctx is done first.
func WaitRetry(ctx context.Context, r *Request, ev RetryEvent) error {
	if r.OnRetry != nil {
		r.OnRetry(ev)
	}
	select {
	case <-time.After(ev.Sleep):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
