package subpub

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

func TestSubPubBasic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := New[string]()
		ctx := context.Background()

		// Subscribe waiting for messages after index 0
		next := sp.Subscribe(ctx, 0)

		// Publish a message at index 1
		go func() {
			sp.Publish(1, "hello")
		}()

		// Should receive the message
		msg, ok := next()
		if !ok {
			t.Fatal("Expected to receive message, got closed channel")
		}
		if msg != "hello" {
			t.Errorf("Expected 'hello', got %q", msg)
		}
	})
}

func TestSubPubMultipleSubscribers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := New[string]()
		ctx := context.Background()

		// Create multiple subscribers
		next1 := sp.Subscribe(ctx, 0)
		next2 := sp.Subscribe(ctx, 0)
		next3 := sp.Subscribe(ctx, 0)

		// Publish a message
		go func() {
			sp.Publish(1, "broadcast")
		}()

		// All subscribers should receive it
		for i, next := range []func() (string, bool){next1, next2, next3} {
			msg, ok := next()
			if !ok {
				t.Fatalf("Subscriber %d: expected to receive message, got closed channel", i+1)
			}
			if msg != "broadcast" {
				t.Errorf("Subscriber %d: expected 'broadcast', got %q", i+1, msg)
			}
		}
	})
}

func TestSubPubSubscriberAlreadyHasMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := New[int]()
		ctx := context.Background()

		// Subscriber already has index 5, waiting for index > 5
		next := sp.Subscribe(ctx, 5)

		// Publish at index 3 (subscriber already has this)
		sp.Publish(3, 100)

		// Publish at index 6 (subscriber should get this)
		go func() {
			sp.Publish(6, 200)
		}()

		msg, ok := next()
		if !ok {
			t.Fatal("Expected to receive message, got closed channel")
		}
		if msg != 200 {
			t.Errorf("Expected 200, got %d", msg)
		}
	})
}

// TestSubPubOutOfOrderPublish: a subscriber gets every index above the one it
// subscribed at, no matter what order the indexes are published in.
//
// Publishers allocate indexes independently of when they publish: shelley
// assigns a message's sequence_id in its own write transaction and publishes
// from a separate goroutine, so a message written second routinely reaches
// Publish first. Filtering against the last index delivered instead of the
// subscription index would swallow whichever message lost that race, and a
// sequence_id is never published twice, so the client would never see it.
func TestSubPubOutOfOrderPublish(t *testing.T) {
	sp := New[int]()
	// A deadline so a regression reports the messages it did get instead of
	// blocking in next() forever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Joining at 2: the subscriber has already replayed everything up to and
	// including index 2.
	next := sp.Subscribe(ctx, 2)

	// 4 lands first, then the message that was written before it.
	sp.Publish(4, 400)
	sp.Publish(3, 300)
	// 1 predates the subscription and stays filtered out, late or not.
	sp.Publish(1, 100)
	sp.Publish(5, 500)

	var got []int
	for range 3 {
		msg, ok := next()
		if !ok {
			t.Fatalf("subscription closed after %d messages: %v", len(got), got)
		}
		got = append(got, msg)
	}
	want := []int{400, 300, 500}
	if !slices.Equal(got, want) {
		t.Errorf("received %v, want %v", got, want)
	}
}

func TestSubPubContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := New[string]()
		ctx, cancel := context.WithCancel(context.Background())

		next, status := sp.SubscribeWithStatus(ctx, 0)

		// Cancel the context
		cancel()

		// Should return false when context is cancelled
		_, ok := next()
		if ok {
			t.Error("Expected closed channel after context cancellation")
		}
		if status.FellBehind() {
			t.Error("context cancellation was reported as falling behind")
		}
	})
}

func TestSubPubSubscriberBehind(t *testing.T) {
	// Don't use synctest for this test as it involves checking buffer overflow behavior
	if SubscriberQueueCapacity != 200 {
		t.Fatalf("SubscriberQueueCapacity = %d, want 200", SubscriberQueueCapacity)
	}
	sp := New[string]()
	ctx := context.Background()

	// Subscriber waiting for messages after index 0
	next := sp.Subscribe(ctx, 0)

	// Fill the channel quickly before the subscriber reads.
	for i := 1; i <= SubscriberQueueCapacity; i++ {
		sp.Publish(int64(i), fmt.Sprintf("message%d", i))
	}

	// One more message disconnects the subscriber because its buffer is full.
	sp.Publish(SubscriberQueueCapacity+1, "overflow")

	// Try to receive - should work for buffered messages
	received := 0
	var messages []string
	for {
		msg, ok := next()
		if !ok {
			break
		}
		messages = append(messages, msg)
		received++
		if received > SubscriberQueueCapacity {
			t.Fatal("Received more messages than expected")
		}
	}

	if received != SubscriberQueueCapacity {
		t.Errorf("Expected to receive %d buffered messages, got %d: %v", SubscriberQueueCapacity, received, messages)
	}
}

func TestSubPubSequentialMessages(t *testing.T) {
	// Don't use synctest for this test as mutex blocking doesn't work well with it
	sp := New[int]()
	ctx := context.Background()

	next := sp.Subscribe(ctx, 0)

	// Publish multiple messages in order
	for i := 1; i <= 5; i++ {
		sp.Publish(int64(i), i*10)
	}

	// Receive all messages
	received := []int{}
	for i := 1; i <= 5; i++ {
		msg, ok := next()
		if !ok {
			t.Fatalf("Expected to receive 5 messages, got closed channel after %d messages", i-1)
		}
		received = append(received, msg)
	}

	// Check we got all expected values in order
	expected := []int{10, 20, 30, 40, 50}
	for i, val := range received {
		if val != expected[i] {
			t.Errorf("Message %d: expected %d, got %d", i, expected[i], val)
		}
	}
}

func TestSubPubLateSubscriber(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := New[string]()
		ctx := context.Background()

		// Publish some messages before anyone subscribes
		sp.Publish(1, "early1")
		sp.Publish(2, "early2")

		// Late subscriber joins, interested in messages after index 2
		next := sp.Subscribe(ctx, 2)

		// Publish a new message
		go func() {
			sp.Publish(3, "late")
		}()

		// Should only receive the new message
		msg, ok := next()
		if !ok {
			t.Fatal("Expected to receive message, got closed channel")
		}
		if msg != "late" {
			t.Errorf("Expected 'late', got %q", msg)
		}
	})
}

func TestSubPubWithTimeout(t *testing.T) {
	sp := New[string]()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	next := sp.Subscribe(ctx, 0)

	// Don't publish anything, just wait for timeout
	_, ok := next()
	if ok {
		t.Error("Expected timeout to close the subscription")
	}
}

func TestSubPubMultiplePublishes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := New[string]()
		ctx := context.Background()

		// Start two subscribers at different positions
		next1 := sp.Subscribe(ctx, 0)
		next2 := sp.Subscribe(ctx, 1)

		// Publish at index 2 - only next1 should receive (next2 already has idx 1)
		go func() {
			sp.Publish(2, "msg2")
		}()

		msg, ok := next1()
		if !ok {
			t.Fatal("Subscriber 1: expected to receive message, got closed channel")
		}
		if msg != "msg2" {
			t.Errorf("Subscriber 1: expected 'msg2', got %q", msg)
		}

		msg, ok = next2()
		if !ok {
			t.Fatal("Subscriber 2: expected to receive message, got closed channel")
		}
		if msg != "msg2" {
			t.Errorf("Subscriber 2: expected 'msg2', got %q", msg)
		}

		// Now both are at index 2, publish at index 3
		go func() {
			sp.Publish(3, "msg3")
		}()

		for i, next := range []func() (string, bool){next1, next2} {
			msg, ok := next()
			if !ok {
				t.Fatalf("Subscriber %d: expected to receive msg3, got closed channel", i+1)
			}
			if msg != "msg3" {
				t.Errorf("Subscriber %d: expected 'msg3', got %q", i+1, msg)
			}
		}
	})
}

// TestSubPubSubscriberContextCancelled tests that subscribers properly handle context cancellation
func TestSubPubSubscriberContextCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := New[string]()
		ctx, cancel := context.WithCancel(context.Background())

		next := sp.Subscribe(ctx, 0)

		// Cancel context before publishing
		cancel()

		// Publish a message
		sp.Publish(1, "test")

		// Should return false when context is cancelled
		_, ok := next()
		if ok {
			t.Error("Expected closed channel after context cancellation")
		}
	})
}

// TestSubPubSubscriberDisconnected tests that subscribers get disconnected when channel is full
func TestSubPubSubscriberDisconnected(t *testing.T) {
	sp := New[string]()
	ctx := context.Background()

	// Create subscriber
	next := sp.Subscribe(ctx, 0)

	// Fill the channel and publish one more message to trigger disconnection.
	for i := 1; i <= SubscriberQueueCapacity+1; i++ {
		sp.Publish(int64(i), fmt.Sprintf("message%d", i))
	}

	// Buffered messages remain available before the disconnection is observed.
	received := 0
	for {
		_, ok := next()
		if !ok {
			break
		}
		received++
		if received > SubscriberQueueCapacity {
			t.Fatal("Received more messages than expected")
		}
	}

	if received != SubscriberQueueCapacity {
		t.Errorf("Expected to receive %d buffered messages, got %d", SubscriberQueueCapacity, received)
	}
}

// TestSubPubDoneClosesImmediatelyWhenSubscriberFallsBehind verifies that the
// cancellation signal does not wait for buffered messages to be drained.
func TestSubPubStatusReportsSubscriberFellBehind(t *testing.T) {
	sp := New[string]()
	ctx, cancel := context.WithCancel(context.Background())
	next, status := sp.SubscribeWithStatus(ctx, 0)

	for i := 1; i <= SubscriberQueueCapacity+1; i++ {
		sp.Publish(int64(i), fmt.Sprintf("message%d", i))
	}
	// Request cancellation can race with overflow handling in the stream
	// handler; it must not erase the reason the subscription already ended.
	cancel()

	select {
	case <-status.Done():
	default:
		t.Fatal("done channel remained open after subscriber was disconnected")
	}
	if !status.FellBehind() {
		t.Fatal("status did not report that the subscriber fell behind")
	}

	// The immediate done signal does not change Subscribe's buffered-drain
	// contract: callers using next still receive the queued messages.
	for i := 0; i < SubscriberQueueCapacity; i++ {
		if _, ok := next(); !ok {
			t.Fatalf("next returned false after %d buffered messages", i)
		}
	}
	if _, ok := next(); ok {
		t.Fatal("next remained open after buffered messages were drained")
	}
}

// TestSubPubSubscriberNotInterested tests that subscribers don't receive messages they're not interested in.
func TestSubPubSubscriberNotInterested(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := New[int]()
		ctx := context.Background()

		// Subscriber already has index 5, waiting for messages after index 5
		next := sp.Subscribe(ctx, 5)

		// Publish at index 5 (subscriber already has this)
		sp.Publish(5, 100)

		// Publish at index 4 (subscriber is ahead of this)
		sp.Publish(4, 200)

		// Publish at index 6 (subscriber should get this)
		go func() {
			sp.Publish(6, 300)
		}()

		msg, ok := next()
		if !ok {
			t.Fatal("Expected to receive message, got closed channel")
		}
		if msg != 300 {
			t.Errorf("Expected 300, got %d", msg)
		}
	})
}

// TestSubPubSubscriberContextDoneDuringPublish tests subscriber context cancellation during publish
func TestSubPubSubscriberContextDoneDuringPublish(t *testing.T) {
	sp := New[string]()
	ctx, cancel := context.WithCancel(context.Background())

	// Create subscriber
	next := sp.Subscribe(ctx, 0)

	// Cancel context
	cancel()

	// Publish a message - subscriber should be removed
	sp.Publish(1, "test")

	// Try to receive - should be closed
	_, ok := next()
	if ok {
		t.Error("Expected closed channel after context cancellation")
	}
}
