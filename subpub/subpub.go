package subpub

import (
	"context"
	"sync"
)

type SubPub[K any] struct {
	mu          sync.Mutex
	subscribers []*subscriber[K]
}

type subscriber[K any] struct {
	idx    int64
	ch     chan K
	ctx    context.Context
	cancel context.CancelFunc
}

func New[K any]() *SubPub[K] {
	return &SubPub[K]{
		subscribers: make([]*subscriber[K], 0),
	}
}

// Subscription is a handle to a registered subscriber, providing both the
// blocking Next function and the ability to advance the internal index.
type Subscription[K any] struct {
	sub *subscriber[K]
	sp  *SubPub[K]
}

// Next blocks until a new message is available or the subscription is cancelled.
func (s *Subscription[K]) Next() (K, bool) {
	select {
	case msg, ok := <-s.sub.ch:
		if !ok {
			var zero K
			return zero, false
		}
		return msg, true
	case <-s.sub.ctx.Done():
		// Context cancelled, but drain any buffered messages first
		select {
		case msg, ok := <-s.sub.ch:
			if ok {
				return msg, true
			}
		default:
		}
		var zero K
		return zero, false
	}
}

// AdvanceIndex updates this subscriber's index so that subsequent Publish calls
// with idx <= newIdx are skipped. This is useful after a catch-up query fills
// in messages the subscriber would otherwise deliver as duplicates.
func (s *Subscription[K]) AdvanceIndex(newIdx int64) {
	s.sp.mu.Lock()
	defer s.sp.mu.Unlock()
	if s.sub.idx < newIdx {
		s.sub.idx = newIdx
	}
}

// Subscribe registers an interest in messages after the given index, subject to the
// expiration/cancellation of the provided context.
func (sp *SubPub[K]) Subscribe(ctx context.Context, idx int64) *Subscription[K] {
	// Create a child context so we can cancel the subscription independently
	subCtx, cancel := context.WithCancel(ctx)

	// Buffered channel to avoid blocking publishers
	ch := make(chan K, 10)
	sub := &subscriber[K]{
		idx:    idx,
		ch:     ch,
		ctx:    subCtx,
		cancel: cancel,
	}

	sp.mu.Lock()
	sp.subscribers = append(sp.subscribers, sub)
	sp.mu.Unlock()

	return &Subscription[K]{sub: sub, sp: sp}
}

// Publish sends a message to all subscribers waiting for messages after the given index.
// Subscribers that are "behind" should get a disconnection message.
func (sp *SubPub[K]) Publish(idx int64, message K) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	// Notify subscribers and filter out disconnected ones
	remaining := sp.subscribers[:0]
	for _, sub := range sp.subscribers {
		// Check if context is still valid
		select {
		case <-sub.ctx.Done():
			// Context cancelled, close channel and don't keep subscriber
			close(sub.ch)
			continue
		default:
		}

		// Only send to subscribers waiting for messages after an index < idx
		if sub.idx < idx {
			// Try to send the message
			select {
			case sub.ch <- message:
				// Success, update subscriber's index and keep them
				sub.idx = idx
				remaining = append(remaining, sub)
			default:
				// Channel full, subscriber is behind - disconnect them
				close(sub.ch)
				sub.cancel()
			}
		} else {
			// This subscriber is not interested yet (already has this index or beyond)
			remaining = append(remaining, sub)
		}
	}
	sp.subscribers = remaining
}

// Broadcast sends a message to ALL subscribers regardless of their current index.
// This is used for out-of-band notifications like conversation list updates.
func (sp *SubPub[K]) Broadcast(message K) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	remaining := sp.subscribers[:0]
	for _, sub := range sp.subscribers {
		select {
		case <-sub.ctx.Done():
			close(sub.ch)
			continue
		default:
		}

		select {
		case sub.ch <- message:
			remaining = append(remaining, sub)
		default:
			// Channel full, disconnect
			close(sub.ch)
			sub.cancel()
		}
	}
	sp.subscribers = remaining
}
