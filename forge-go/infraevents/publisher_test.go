package infraevents

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/stretchr/testify/require"
)

type recordingBackend struct {
	mu       sync.Mutex
	messages map[string][]protocol.Message
}

func newRecordingBackend() *recordingBackend {
	return &recordingBackend{messages: make(map[string][]protocol.Message)}
}

func (r *recordingBackend) PublishMessage(_ context.Context, namespace, topic string, msg *protocol.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages[namespace+":"+topic] = append(r.messages[namespace+":"+topic], *msg)
	return nil
}

func (r *recordingBackend) GetMessagesForTopic(_ context.Context, namespace, topic string) ([]protocol.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.Message(nil), r.messages[namespace+":"+topic]...), nil
}

func (r *recordingBackend) GetMessagesSince(_ context.Context, _, _ string, _ uint64) ([]protocol.Message, error) {
	return nil, nil
}

func (r *recordingBackend) GetMessagesByID(_ context.Context, _ string, _ []uint64) ([]protocol.Message, error) {
	return nil, nil
}

func (r *recordingBackend) Subscribe(_ context.Context, _ string, _ ...string) (messaging.Subscription, error) {
	return nil, nil
}

func (r *recordingBackend) Close() error { return nil }

func TestPublisherEmitPublishesCanonicalInfraEvent(t *testing.T) {
	backend := newRecordingBackend()
	pub, err := NewPublisher(backend)
	require.NoError(t, err)

	err = pub.Emit(context.Background(), EmitParams{
		Kind:            "agent.process.started",
		Severity:        SeverityInfo,
		GuildID:         "g1",
		AgentID:         "g1#a1",
		RequestID:       "req-1",
		SourceComponent: "forge-go.test",
		Message:         "agent process started",
		Detail: map[string]any{
			"pid": 1234,
		},
	})
	require.NoError(t, err)

	msgs, err := backend.GetMessagesForTopic(context.Background(), "g1", Topic)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	msg := msgs[0]
	require.Equal(t, Format, msg.Format)
	require.Equal(t, Topic, msg.Topics.String())

	var event Event
	require.NoError(t, json.Unmarshal(msg.Payload, &event))
	require.Equal(t, SchemaVersion, event.SchemaVersion)
	require.NotEmpty(t, event.EventID)
	require.Equal(t, "agent.process.started", event.Kind)
	require.Equal(t, SeverityInfo, event.Severity)
	require.Equal(t, "g1", event.GuildID)
	require.Equal(t, "g1#a1", event.AgentID)
	require.Equal(t, "req-1", event.RequestID)
	require.Equal(t, "forge-go.test", event.Source.Component)
	require.Equal(t, "agent process started", event.Message)
	require.Equal(t, float64(1234), event.Detail["pid"])
}
