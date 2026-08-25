package subpub

import (
	"context"
	"sync"
	"sync/atomic"
)

// SubscriberQueueCapacity bounds pending messages per subscriber.
const SubscriberQueueCapacity = 200

// SubscriptionStatus reports why a subscription ended.
type SubscriptionStatus struct {
	done       <-chan struct{}
	fellBehind atomic.Bool
}

// Done closes when the subscription is canceled or disconnected.
func (s *SubscriptionStatus) Done() <-chan struct{} { return s.done }

// FellBehind reports whether the subscriber was disconnected because its queue filled.
func (s *SubscriptionStatus) FellBehind() bool { return s.fellBehind.Load() }

type SubPub[K any] struct {
	mu          sync.Mutex
	subscribers []*subscriber[K]
}

type subscriber[K any] struct {
	// after is the index this subscriber joined at: it wants every message
	// published with a strictly greater index. It is fixed for the life of the
	// subscription and must stay that way.
	//
	// Advancing it to the last index delivered would be a filter on "newer than
	// what I last got", which only works if publishers hand out indexes in
	// increasing order. They don't: shelley allocates a message's sequence_id in
	// its own write transaction and publishes from a separate goroutine, so a
	// message written second can reach Publish first. An advancing cursor would
	// then skip past the earlier message and drop it permanently — and since a
	// sequence_id is delivered exactly once, nothing downstream would ever
	// resend it.
	after  int64
	ch     chan K
	ctx    context.Context
	cancel context.CancelFunc
	status *SubscriptionStatus
}

func New[K any]() *SubPub[K] {
	return &SubPub[K]{
		subscribers: make([]*subscriber[K], 0),
	}
}

// Subscribe registers an interest in messages after the given index, subject to the
// expiration/cancellation of the provided context. The returned function blocks
// until a new message, and can return false as the second argument if the subscription
// is done.
func (sp *SubPub[K]) Subscribe(ctx context.Context, idx int64) func() (K, bool) {
	next, _ := sp.SubscribeWithStatus(ctx, idx)
	return next
}

// SubscribeWithStatus is Subscribe plus status that reports immediately when
// the subscription ends and distinguishes falling behind from cancellation.
func (sp *SubPub[K]) SubscribeWithStatus(ctx context.Context, idx int64) (func() (K, bool), *SubscriptionStatus) {
	// Create a child context so we can cancel the subscription independently
	subCtx, cancel := context.WithCancel(ctx)
	status := &SubscriptionStatus{done: subCtx.Done()}

	// Buffered channel to avoid blocking publishers
	ch := make(chan K, SubscriberQueueCapacity)
	sub := &subscriber[K]{
		after:  idx,
		ch:     ch,
		ctx:    subCtx,
		cancel: cancel,
		status: status,
	}

	sp.mu.Lock()
	sp.subscribers = append(sp.subscribers, sub)
	sp.mu.Unlock()

	// Return a function that blocks until the next message.
	next := func() (K, bool) {
		select {
		case msg, ok := <-ch:
			if !ok {
				var zero K
				return zero, false
			}
			return msg, true
		case <-subCtx.Done():
			// Context cancelled, but drain any buffered messages first
			select {
			case msg, ok := <-ch:
				if ok {
					return msg, true
				}
			default:
			}
			var zero K
			return zero, false
		}
	}
	return next, status
}

// Publish sends a message to all subscribers whose subscription index is below
// the given index. Subscribers that are "behind" should get a disconnection
// message.
//
// Indexes need not arrive in increasing order; each is judged against the
// subscription index alone, so an out-of-order publish is still delivered.
// Callers must publish each index at most once per subscriber, since nothing
// here suppresses a repeat.
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

		// Only send to subscribers that subscribed at an index below idx.
		if sub.after < idx {
			// Try to send the message
			select {
			case sub.ch <- message:
				remaining = append(remaining, sub)
			default:
				// Channel full, subscriber is behind - disconnect them
				sub.status.fellBehind.Store(true)
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
			sub.status.fellBehind.Store(true)
			close(sub.ch)
			sub.cancel()
		}
	}
	sp.subscribers = remaining
}
