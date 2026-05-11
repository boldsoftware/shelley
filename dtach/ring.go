package dtach

import "sync"

// ring is a fixed-capacity byte buffer that drops the oldest bytes on overflow.
// It is safe for concurrent use.
type ring struct {
	mu       sync.Mutex
	buf      []byte
	capacity int
}

func newRing(capacity int) *ring {
	return &ring{capacity: capacity}
}

func (r *ring) write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) >= r.capacity {
		r.buf = append(r.buf[:0], p[len(p)-r.capacity:]...)
		return
	}
	needed := len(r.buf) + len(p) - r.capacity
	if needed > 0 {
		r.buf = append(r.buf[:0], r.buf[needed:]...)
	}
	r.buf = append(r.buf, p...)
}

func (r *ring) snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}
