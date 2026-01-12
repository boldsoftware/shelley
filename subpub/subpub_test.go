package subpub

import (
	"context"
	"fmt"
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

func TestSubPubContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := New[string]()
		ctx, cancel := context.WithCancel(context.Background())

		next := sp.Subscribe(ctx, 0)

		// Cancel the context
		cancel()

		// Should return false when context is cancelled
		_, ok := next()
		if ok {
			t.Error("Expected closed channel after context cancellation")
		}
	})
}

func TestSubPubSubscriberBehind(t *testing.T) {
	// Don't use synctest for this test as it involves checking buffer overflow behavior
	sp := New[string]()
	ctx := context.Background()

	// Subscriber waiting for messages after index 0
	next := sp.Subscribe(ctx, 0)

	// Fill up the channel buffer (10 messages) quickly before subscriber reads
	for i := 1; i <= 10; i++ {
		sp.Publish(int64(i), fmt.Sprintf("message%d", i))
	}

	// Try to send one more - subscriber should be disconnected because buffer is full
	sp.Publish(11, "overflow")

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
		if received > 11 {
			t.Fatal("Received more messages than expected")
		}
	}

	// Should have received exactly 10 messages before being disconnected
	if received != 10 {
		t.Errorf("Expected to receive 10 buffered messages, got %d: %v", received, messages)
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

	// Fill up the channel buffer (10 messages) + 1 more to trigger disconnection
	for i := 1; i <= 11; i++ {
		sp.Publish(int64(i), fmt.Sprintf("message%d", i))
	}

	// Try to receive all messages - should get exactly 10, then be disconnected
	received := 0
	for {
		_, ok := next()
		if !ok {
			break
		}
		received++
		if received > 11 {
			t.Fatal("Received more messages than expected")
		}
	}

	// Should have received exactly 10 messages before being disconnected
	if received != 10 {
		t.Errorf("Expected to receive 10 buffered messages, got %d", received)
	}
}

// TestSubPubSubscriberNotInterested tests that subscribers don't receive messages they're not interested in
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
