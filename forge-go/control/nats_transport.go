package control

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// ctrlSanitize replaces characters not allowed in NATS subject tokens.
// Must match the Python _sanitize helper and the Go messaging.sanitize helper.
func ctrlSanitize(name string) string {
	r := strings.NewReplacer(":", "_", ".", "_", "$", "_")
	return r.Replace(name)
}

const (
	ctrlResponseStream        = "CTRL_RESPONSES"
	ctrlResponseSubjectPrefix = "ctrl.response."
	ctrlResponseMaxAge        = 60 * time.Second
	ctrlQueueMaxAge           = 5 * time.Minute
)

// NATSControlTransport implements ControlPlane using NATS JetStream.
// Control queues are individual JetStream streams; responses use a shared CTRL_RESPONSES stream.
type NATSControlTransport struct {
	nc      *nats.Conn
	js      nats.JetStreamContext
	mu      sync.Mutex
	streams map[string]bool
	subs    map[string]*nats.Subscription // cached pull subscriptions per queue key
}

var _ ControlPlane = (*NATSControlTransport)(nil)

// NewNATSControlTransport creates a NATSControlTransport and ensures the shared response stream exists.
func NewNATSControlTransport(nc *nats.Conn) (*NATSControlTransport, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("control: failed to get JetStream context: %w", err)
	}
	t := &NATSControlTransport{
		nc:      nc,
		js:      js,
		streams: make(map[string]bool),
		subs:    make(map[string]*nats.Subscription),
	}
	if err := t.ensureResponseStream(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *NATSControlTransport) ctrlStreamName(queueKey string) string {
	return "CTRL_" + ctrlSanitize(queueKey)
}

func (t *NATSControlTransport) ctrlSubject(queueKey string) string {
	return "ctrl." + ctrlSanitize(queueKey)
}

func (t *NATSControlTransport) ensureStream(queueKey string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.streams[queueKey] {
		return nil
	}

	sName := t.ctrlStreamName(queueKey)
	subject := t.ctrlSubject(queueKey)

	cfg := &nats.StreamConfig{
		Name:      sName,
		Subjects:  []string{subject},
		MaxAge:    ctrlQueueMaxAge,
		Retention: nats.WorkQueuePolicy,
	}

	_, err := t.js.AddStream(cfg)
	if err != nil {
		if _, lookupErr := t.js.StreamInfo(sName); lookupErr == nil {
			t.streams[queueKey] = true
			return nil
		}
		return fmt.Errorf("control: failed to ensure stream for %q: %w", queueKey, err)
	}
	t.streams[queueKey] = true
	return nil
}

func (t *NATSControlTransport) ensureResponseStream() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	const key = "__responses__"
	if t.streams[key] {
		return nil
	}

	cfg := &nats.StreamConfig{
		Name:     ctrlResponseStream,
		Subjects: []string{ctrlResponseSubjectPrefix + ">"},
		MaxAge:   ctrlResponseMaxAge,
	}

	_, err := t.js.AddStream(cfg)
	if err != nil {
		if _, lookupErr := t.js.StreamInfo(ctrlResponseStream); lookupErr == nil {
			t.streams[key] = true
			return nil
		}
		return fmt.Errorf("control: failed to ensure response stream: %w", err)
	}
	t.streams[key] = true
	return nil
}

// Push enqueues a control message onto a queue-key stream.
func (t *NATSControlTransport) Push(ctx context.Context, queueKey string, payload []byte) error {
	if err := t.ensureStream(queueKey); err != nil {
		return err
	}
	_, err := t.js.Publish(t.ctrlSubject(queueKey), payload)
	return err
}

// getOrCreateSub returns a cached pull subscription for the given queue key,
// creating one if it doesn't exist or the previous one became invalid.
func (t *NATSControlTransport) getOrCreateSub(queueKey string) (*nats.Subscription, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if sub, ok := t.subs[queueKey]; ok && sub.IsValid() {
		return sub, nil
	}

	sub, err := t.js.PullSubscribe(
		t.ctrlSubject(queueKey),
		"",
		nats.BindStream(t.ctrlStreamName(queueKey)),
	)
	if err != nil {
		return nil, fmt.Errorf("control: failed to create pull subscription for %q: %w", queueKey, err)
	}
	t.subs[queueKey] = sub
	return sub, nil
}

// invalidateSub removes a cached subscription so the next call recreates it.
func (t *NATSControlTransport) invalidateSub(queueKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if sub, ok := t.subs[queueKey]; ok {
		_ = sub.Unsubscribe()
		delete(t.subs, queueKey)
	}
}

// Pop dequeues one message from a queue-key stream, blocking up to timeout.
// Returns (nil, nil) on timeout.
func (t *NATSControlTransport) Pop(ctx context.Context, queueKey string, timeout time.Duration) ([]byte, error) {
	if err := t.ensureStream(queueKey); err != nil {
		return nil, err
	}

	sub, err := t.getOrCreateSub(queueKey)
	if err != nil {
		return nil, err
	}

	msgs, err := sub.Fetch(1, nats.MaxWait(timeout))
	if err == nats.ErrTimeout {
		return nil, nil
	}
	if err != nil {
		// Subscription may have gone bad; invalidate so next call recreates it.
		t.invalidateSub(queueKey)
		return nil, fmt.Errorf("control: pop from %q: %w", queueKey, err)
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	if err := msgs[0].AckSync(); err != nil {
		return nil, fmt.Errorf("control: ack message from %q: %w", queueKey, err)
	}
	return msgs[0].Data, nil
}

// Close cleans up cached pull subscriptions.
func (t *NATSControlTransport) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, sub := range t.subs {
		_ = sub.Unsubscribe()
		delete(t.subs, key)
	}
}

// QueueDepth returns the number of pending messages in a queue-key stream.
func (t *NATSControlTransport) QueueDepth(ctx context.Context, queueKey string) (int64, error) {
	if err := t.ensureStream(queueKey); err != nil {
		return 0, err
	}
	info, err := t.js.StreamInfo(t.ctrlStreamName(queueKey))
	if err != nil {
		return 0, err
	}
	return int64(info.State.Msgs), nil
}

// PushResponse publishes a response payload to the shared response stream.
// The ttl parameter is accepted for interface compatibility but TTL is governed by stream MaxAge.
func (t *NATSControlTransport) PushResponse(ctx context.Context, requestID string, payload []byte, ttl time.Duration) error {
	subject := ctrlResponseSubjectPrefix + ctrlSanitize(requestID)
	_, err := t.js.Publish(subject, payload)
	return err
}

// WaitResponse waits for a response to a given requestID, blocking up to timeout.
// Returns (nil, nil) on timeout.
func (t *NATSControlTransport) WaitResponse(ctx context.Context, requestID string, timeout time.Duration) ([]byte, error) {
	subject := ctrlResponseSubjectPrefix + ctrlSanitize(requestID)
	sub, err := t.js.PullSubscribe(
		subject,
		"",
		nats.BindStream(ctrlResponseStream),
	)
	if err != nil {
		return nil, fmt.Errorf("control: failed to subscribe for response %q: %w", requestID, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	msgs, err := sub.Fetch(1, nats.MaxWait(timeout))
	if err == nats.ErrTimeout {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("control: wait response for %q: %w", requestID, err)
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	_ = msgs[0].Ack()
	return msgs[0].Data, nil
}
