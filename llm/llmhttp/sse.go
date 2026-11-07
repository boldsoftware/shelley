package llmhttp

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// SSEEvent is a dispatched Server-Sent Event.
type SSEEvent struct {
	EventType string // the "event:" field; empty if unset
	Data      string // the "data:" fields joined with "\n"
}

// ReadSSE reads an SSE stream and yields its events, following
// https://html.spec.whatwg.org/multipage/server-sent-events.html#event-stream-interpretation:
// blank lines dispatch, ':' lines are comments, events without data are
// dropped, and "id"/"retry" are ignored. A trailing event without a final
// blank line is still dispatched.
func ReadSSE(r io.Reader, yield func(SSEEvent) error) error {
	scanner := bufio.NewScanner(r)
	// Events can be large (e.g. tool input JSON).
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var (
		eventType string
		dataLines []string
		hasData   bool
	)
	dispatch := func() error {
		if !hasData {
			eventType = ""
			return nil
		}
		ev := SSEEvent{EventType: eventType, Data: strings.Join(dataLines, "\n")}
		eventType = ""
		dataLines = dataLines[:0]
		hasData = false
		return yield(ev)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventType = value
		case "data":
			dataLines = append(dataLines, value)
			hasData = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading SSE stream: %w", err)
	}
	return dispatch()
}
