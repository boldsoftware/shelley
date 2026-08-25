package server

import (
	"sync"
	"time"

	"shelley.exe.dev/llm"
)

// streamFlusher batches LLM stream deltas and flushes them periodically.
// Providers emit hundreds of tiny events per second. Broadcasting each one
// individually overwhelms the bounded subpub queue, causing subscriber
// disconnections. Instead, we coalesce adjacent deltas of the same type and
// index every interval (e.g., 50ms), yielding ~20 updates/second.
type streamFlusher struct {
	cm       *ConversationManager
	interval time.Duration
	// shouldPublish rejects buffered deltas from a loop generation that has
	// since been cancelled or replaced.
	shouldPublish func() bool

	mu      sync.Mutex
	buf     []llm.StreamDelta
	timer   *time.Timer
	running bool
}

// nextSeq returns the next monotonically increasing sequence number. The
// counter lives on the ConversationManager so it survives loop resets and is
// truly per-conversation. Safe to call without holding sf.mu.
func (sf *streamFlusher) nextSeq() int64 {
	return sf.cm.streamDeltaSeq.Add(1)
}

func newStreamFlusher(cm *ConversationManager, interval time.Duration, shouldPublish func() bool) *streamFlusher {
	return &streamFlusher{
		cm:            cm,
		interval:      interval,
		shouldPublish: shouldPublish,
	}
}

// Push adds a stream delta to the buffer and schedules a flush.
func (sf *streamFlusher) Push(delta llm.StreamDelta) {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	if delta.Text == "" {
		return
	}
	if n := len(sf.buf); n > 0 && sf.buf[n-1].Type == delta.Type && sf.buf[n-1].Index == delta.Index {
		sf.buf[n-1].Text += delta.Text
	} else {
		sf.buf = append(sf.buf, delta)
	}

	if !sf.running {
		sf.running = true
		sf.timer = time.AfterFunc(sf.interval, sf.flush)
	}
}

func (sf *streamFlusher) flush() {
	sf.mu.Lock()
	deltas := sf.buf
	sf.buf = nil
	sf.running = false
	if sf.timer != nil {
		sf.timer.Stop()
		sf.timer = nil
	}
	for i := range deltas {
		deltas[i].Seq = sf.nextSeq()
	}
	sf.mu.Unlock()

	if !sf.shouldPublish() {
		return
	}
	for i := range deltas {
		sf.cm.broadcastStream(StreamResponse{
			StreamDelta: &deltas[i],
		})
	}
}

// Flush forces any buffered deltas to be broadcast immediately.
// Call this before recording the final assistant message to ensure
// deltas reach the UI before the full message replaces them.
func (sf *streamFlusher) Flush() {
	sf.flush()
}
