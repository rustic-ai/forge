package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// fakeSubscription is a controllable messaging.Subscription for exercising the
// forward()/Close() concurrency of GuildSubscription in isolation. Close() is a
// no-op on the channels: the real subscriptions signal shutdown by closing their
// channels, but GuildSubscription.forward() also exits on context cancellation,
// and leaving the channels open here avoids a send-on-closed-channel race with a
// concurrent test producer. Use closeSource() to exercise the source-closed path.
type fakeSubscription struct {
	ch        chan messaging.SubMessage
	errCh     chan error
	closeOnce sync.Once
}

func newFakeSubscription() *fakeSubscription {
	return &fakeSubscription{
		ch:    make(chan messaging.SubMessage),
		errCh: make(chan error),
	}
}

func (f *fakeSubscription) Channel() <-chan messaging.SubMessage { return f.ch }
func (f *fakeSubscription) ErrChannel() <-chan error             { return f.errCh }
func (f *fakeSubscription) Close() error                         { return nil }

// closeSource simulates the underlying transport closing its delivery channels.
func (f *fakeSubscription) closeSource() {
	f.closeOnce.Do(func() {
		close(f.ch)
		close(f.errCh)
	})
}

func newTestSubscription(sub messaging.Subscription, msgBuf, errBuf int) *GuildSubscription {
	ctx, cancel := context.WithCancel(context.Background())
	return &GuildSubscription{
		sub:     sub,
		msgChan: make(chan *protocol.Message, msgBuf),
		errChan: make(chan error, errBuf),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// TestClose_DuringActiveForwarding asserts that closing the subscription while
// messages are actively being forwarded never panics with "send on closed
// channel". This is the regression: previously Close() closed msgChan/errChan
// while forward() could still be mid-send. Run with -race for full coverage.
func TestClose_DuringActiveForwarding(t *testing.T) {
	fs := newFakeSubscription()
	s := newTestSubscription(fs, 100, 10)
	go s.forward()

	// Drain the consumer side so the buffer is not the thing preventing sends.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range s.Messages() {
		}
	}()

	// Producer: push messages until the context is cancelled by Close().
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for {
			select {
			case fs.ch <- messaging.SubMessage{Message: &protocol.Message{}}:
			case <-s.ctx.Done():
				return
			}
		}
	}()

	// Let a few messages flow, then close concurrently with active forwarding.
	time.Sleep(5 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	// Close is idempotent.
	if err := s.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}

	waitClosed(t, producerDone, "producer did not observe context cancellation")
	waitClosed(t, drained, "msgChan was never closed after Close")
}

// TestClose_UnblocksStalledForward asserts that a forward() goroutine blocked on
// a send (because the consumer stopped reading and the buffer filled) is released
// by Close() rather than leaking forever.
func TestClose_UnblocksStalledForward(t *testing.T) {
	fs := newFakeSubscription()
	s := newTestSubscription(fs, 1, 1) // tiny buffers so the second send blocks

	go s.forward()

	// First message lands in the size-1 buffer; the second leaves forward blocked
	// on the guarded send with no consumer reading.
	fs.ch <- messaging.SubMessage{Message: &protocol.Message{}}
	fs.ch <- messaging.SubMessage{Message: &protocol.Message{}}

	// Close must cancel the context, unblock the stalled send, and let forward
	// exit — which closes msgChan.
	if err := s.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range s.Messages() {
		}
	}()
	waitClosed(t, drained, "forward goroutine leaked: msgChan never closed after Close")
}

// TestForward_ExitsWhenSourceCloses asserts that forward() returns (and closes
// its output channels) when the underlying subscription closes its channels,
// even without an explicit Close() call.
func TestForward_ExitsWhenSourceCloses(t *testing.T) {
	fs := newFakeSubscription()
	s := newTestSubscription(fs, 1, 1)
	go s.forward()

	fs.closeSource()

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range s.Messages() {
		}
	}()
	waitClosed(t, drained, "forward did not exit when the source channel closed")
}

func waitClosed(t *testing.T, done <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
}
