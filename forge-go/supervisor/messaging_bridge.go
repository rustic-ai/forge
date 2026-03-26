package supervisor

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/go-zeromq/zmq4"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// BridgeTransportMode controls whether the ZMQ bridge uses IPC (unix domain
// socket) or TCP (loopback) for communication with the agent process.
type BridgeTransportMode string

const (
	BridgeTransportIPC BridgeTransportMode = "ipc"
	BridgeTransportTCP BridgeTransportMode = "tcp"
)

// NormalizeBridgeTransportMode returns the canonical BridgeTransportMode for
// the given string. Unknown values default to BridgeTransportIPC.
func NormalizeBridgeTransportMode(s string) BridgeTransportMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "tcp":
		return BridgeTransportTCP
	default:
		return BridgeTransportIPC
	}
}

type bridgeEnvelope struct {
	Kind             string            `json:"kind"`
	RequestID        string            `json:"request_id,omitempty"`
	Op               string            `json:"op,omitempty"`
	Namespace        string            `json:"namespace,omitempty"`
	Topic            string            `json:"topic,omitempty"`
	SinceID          uint64            `json:"since_id,omitempty"`
	Message          json.RawMessage   `json:"message,omitempty"`
	Messages         []json.RawMessage `json:"messages,omitempty"`
	MessageIDs       []uint64          `json:"msg_ids,omitempty"`
	LegacyMessageIDs []uint64          `json:"message_ids,omitempty"`
	OK               bool              `json:"ok,omitempty"`
	Error            string            `json:"error,omitempty"`
}

type AgentMessagingBridge struct {
	ctx      context.Context
	cancel   context.CancelFunc
	endpoint string
	sock     zmq4.Socket

	msgBackend messaging.Backend

	sendMu sync.Mutex
	subsMu sync.Mutex
	subs   map[string]messaging.Subscription

	closeOnce  sync.Once
	wg         sync.WaitGroup
	socketPath string
	mode       BridgeTransportMode
}

// NewAgentMessagingBridge creates a bridge using IPC mode (backward-compatible).
func NewAgentMessagingBridge(
	parent context.Context,
	guildID string,
	agentID string,
	workDir string,
	msgBackend messaging.Backend,
) (*AgentMessagingBridge, error) {
	return NewAgentMessagingBridgeWithMode(parent, guildID, agentID, workDir, msgBackend, BridgeTransportIPC)
}

// NewAgentMessagingBridgeWithMode creates a bridge using the specified transport mode.
func NewAgentMessagingBridgeWithMode(
	parent context.Context,
	guildID string,
	agentID string,
	workDir string,
	msgBackend messaging.Backend,
	mode BridgeTransportMode,
) (*AgentMessagingBridge, error) {
	if msgBackend == nil {
		return nil, fmt.Errorf("messaging backend is required")
	}

	ctx, cancel := context.WithCancel(parent)

	var endpoint, socketPath string
	switch mode {
	case BridgeTransportTCP:
		// Grab an ephemeral port, then release it so ZMQ can bind.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			cancel()
			return nil, fmt.Errorf("allocate ephemeral port for bridge: %w", err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		endpoint = "tcp://" + addr

	default: // IPC
		mode = BridgeTransportIPC
		socketDir := resolveBridgeSocketDir()
		if err := os.MkdirAll(socketDir, 0o700); err != nil {
			cancel()
			return nil, fmt.Errorf("create bridge socket dir: %w", err)
		}
		socketPath = bridgeSocketPath(socketDir, guildID, agentID, workDir)
		_ = os.Remove(socketPath)
		endpoint = "ipc://" + socketPath
	}

	sock := zmq4.NewPair(ctx, zmq4.WithAutomaticReconnect(true))
	if err := sock.Listen(endpoint); err != nil {
		cancel()
		if socketPath != "" {
			_ = os.Remove(socketPath)
		}
		return nil, fmt.Errorf("listen on %s: %w", endpoint, err)
	}

	bridge := &AgentMessagingBridge{
		ctx:        ctx,
		cancel:     cancel,
		endpoint:   endpoint,
		sock:       sock,
		msgBackend: msgBackend,
		subs:       make(map[string]messaging.Subscription),
		socketPath: socketPath,
		mode:       mode,
	}

	bridge.wg.Add(1)
	go bridge.serve()

	return bridge, nil
}

func bridgeSocketPath(socketDir, guildID, agentID, workDir string) string {
	digest := sha1.Sum([]byte(workDir + "|" + guildID + "|" + agentID))
	prefix := sanitizePathComponent(agentID)
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	return filepath.Join(socketDir, fmt.Sprintf("%s-%x.sock", prefix, digest[:8]))
}

func resolveBridgeSocketDir() string {
	if override := strings.TrimSpace(os.Getenv("FORGE_ZMQ_DIR")); override != "" {
		return override
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "forge-zmq")
	}
	// Keep IPC socket paths short; long TMPDIR-derived paths can exceed unix socket limits.
	return "/tmp/forge-zmq"
}

func (b *AgentMessagingBridge) Endpoint() string {
	return b.endpoint
}

// SocketPath returns the IPC socket file path, or empty string for TCP mode.
func (b *AgentMessagingBridge) SocketPath() string {
	return b.socketPath
}

// Mode returns the transport mode used by this bridge.
func (b *AgentMessagingBridge) Mode() BridgeTransportMode {
	return b.mode
}

func (b *AgentMessagingBridge) Close() {
	b.closeOnce.Do(func() {
		b.cancel()
		b.closeSubscriptions()
		_ = b.sock.Close()
		b.wg.Wait()
		if b.socketPath != "" {
			_ = os.Remove(b.socketPath)
		}
	})
}

func (b *AgentMessagingBridge) serve() {
	defer b.wg.Done()

	for {
		msg, err := b.sock.Recv()
		if err != nil {
			if b.ctx.Err() != nil {
				return
			}
			return
		}

		if len(msg.Frames) == 0 {
			continue
		}

		var req bridgeEnvelope
		if err := json.Unmarshal(msg.Frames[0], &req); err != nil {
			_ = b.sendEnvelope(bridgeEnvelope{
				Kind:  "response",
				Error: fmt.Sprintf("invalid bridge request: %v", err),
			})
			continue
		}

		if req.Kind != "request" {
			continue
		}

		if err := b.handleRequest(req); err != nil {
			_ = b.sendEnvelope(bridgeEnvelope{
				Kind:      "response",
				RequestID: req.RequestID,
				Op:        req.Op,
				Error:     err.Error(),
			})
		}
	}
}

func (b *AgentMessagingBridge) handleRequest(req bridgeEnvelope) error {
	switch req.Op {
	case "ping":
		return b.sendEnvelope(bridgeEnvelope{Kind: "response", RequestID: req.RequestID, Op: req.Op, OK: true})
	case "publish":
		return b.handlePublish(req)
	case "subscribe":
		return b.handleSubscribe(req)
	case "unsubscribe":
		return b.handleUnsubscribe(req)
	case "get_messages":
		return b.handleGetMessages(req)
	case "get_since":
		return b.handleGetSince(req)
	case "get_next":
		return b.handleGetNext(req)
	case "get_by_id":
		return b.handleGetByID(req)
	case "cleanup":
		b.closeSubscriptions()
		return b.sendEnvelope(bridgeEnvelope{Kind: "response", RequestID: req.RequestID, Op: req.Op, OK: true})
	default:
		return fmt.Errorf("unsupported bridge op %q", req.Op)
	}
}

func (b *AgentMessagingBridge) handlePublish(req bridgeEnvelope) error {
	namespace, bareTopic, _, err := splitBridgeTopic(req.Namespace, req.Topic)
	if err != nil {
		return err
	}

	message, err := decodeBridgeMessage(req.Message)
	if err != nil {
		return err
	}

	if err := b.msgBackend.PublishMessage(b.ctx, namespace, bareTopic, &message); err != nil {
		return err
	}

	return b.sendEnvelope(bridgeEnvelope{Kind: "response", RequestID: req.RequestID, Op: req.Op, OK: true})
}

func (b *AgentMessagingBridge) handleSubscribe(req bridgeEnvelope) error {
	namespace, bareTopic, namespacedTopic, err := splitBridgeTopic(req.Namespace, req.Topic)
	if err != nil {
		return err
	}

	b.subsMu.Lock()
	if _, exists := b.subs[namespacedTopic]; exists {
		b.subsMu.Unlock()
		return b.sendEnvelope(bridgeEnvelope{Kind: "response", RequestID: req.RequestID, Op: req.Op, OK: true})
	}
	b.subsMu.Unlock()

	sub, err := b.msgBackend.Subscribe(b.ctx, namespace, bareTopic)
	if err != nil {
		return err
	}

	b.subsMu.Lock()
	b.subs[namespacedTopic] = sub
	b.subsMu.Unlock()

	b.wg.Add(1)
	go b.forwardSubscription(namespacedTopic, sub)

	return b.sendEnvelope(bridgeEnvelope{Kind: "response", RequestID: req.RequestID, Op: req.Op, OK: true})
}

func (b *AgentMessagingBridge) handleUnsubscribe(req bridgeEnvelope) error {
	_, _, namespacedTopic, err := splitBridgeTopic(req.Namespace, req.Topic)
	if err != nil {
		return err
	}

	var sub messaging.Subscription

	b.subsMu.Lock()
	sub = b.subs[namespacedTopic]
	delete(b.subs, namespacedTopic)
	b.subsMu.Unlock()

	if sub != nil {
		_ = sub.Close()
	}

	return b.sendEnvelope(bridgeEnvelope{Kind: "response", RequestID: req.RequestID, Op: req.Op, OK: true})
}

func (b *AgentMessagingBridge) handleGetMessages(req bridgeEnvelope) error {
	namespace, bareTopic, _, err := splitBridgeTopic(req.Namespace, req.Topic)
	if err != nil {
		return err
	}

	messagesForTopic, err := b.msgBackend.GetMessagesForTopic(b.ctx, namespace, bareTopic)
	if err != nil {
		return err
	}

	payload, err := marshalMessages(messagesForTopic)
	if err != nil {
		return err
	}

	return b.sendEnvelope(bridgeEnvelope{
		Kind:      "response",
		RequestID: req.RequestID,
		Op:        req.Op,
		OK:        true,
		Messages:  payload,
	})
}

func (b *AgentMessagingBridge) handleGetSince(req bridgeEnvelope) error {
	namespace, bareTopic, _, err := splitBridgeTopic(req.Namespace, req.Topic)
	if err != nil {
		return err
	}

	messagesSince, err := b.msgBackend.GetMessagesSince(b.ctx, namespace, bareTopic, req.SinceID)
	if err != nil {
		return err
	}

	payload, err := marshalMessages(messagesSince)
	if err != nil {
		return err
	}

	return b.sendEnvelope(bridgeEnvelope{
		Kind:      "response",
		RequestID: req.RequestID,
		Op:        req.Op,
		OK:        true,
		Messages:  payload,
	})
}

func (b *AgentMessagingBridge) handleGetNext(req bridgeEnvelope) error {
	namespace, bareTopic, _, err := splitBridgeTopic(req.Namespace, req.Topic)
	if err != nil {
		return err
	}

	messagesSince, err := b.msgBackend.GetMessagesSince(b.ctx, namespace, bareTopic, req.SinceID)
	if err != nil {
		return err
	}

	var payload json.RawMessage
	if len(messagesSince) > 0 {
		raw, err := json.Marshal(messagesSince[0])
		if err != nil {
			return err
		}
		payload = raw
	}

	return b.sendEnvelope(bridgeEnvelope{
		Kind:      "response",
		RequestID: req.RequestID,
		Op:        req.Op,
		OK:        true,
		Message:   payload,
	})
}

func (b *AgentMessagingBridge) handleGetByID(req bridgeEnvelope) error {
	namespace := strings.TrimSpace(req.Namespace)
	if namespace == "" {
		return fmt.Errorf("namespace is required for get_by_id")
	}

	messagesByID, err := b.msgBackend.GetMessagesByID(b.ctx, namespace, req.requestedMessageIDs())
	if err != nil {
		return err
	}

	payload, err := marshalMessages(messagesByID)
	if err != nil {
		return err
	}

	return b.sendEnvelope(bridgeEnvelope{
		Kind:      "response",
		RequestID: req.RequestID,
		Op:        req.Op,
		OK:        true,
		Messages:  payload,
	})
}

func (b *AgentMessagingBridge) forwardSubscription(topic string, sub messaging.Subscription) {
	defer b.wg.Done()

	for {
		select {
		case <-b.ctx.Done():
			return
		case msg, ok := <-sub.Channel():
			if !ok {
				return
			}
			raw, err := json.Marshal(msg.Message)
			if err != nil {
				continue
			}
			_ = b.sendEnvelope(bridgeEnvelope{
				Kind:    "event",
				Op:      "deliver",
				Topic:   topic,
				Message: raw,
			})
		case err, ok := <-sub.ErrChannel():
			if !ok {
				return
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return
			}
		}
	}
}

func (b *AgentMessagingBridge) closeSubscriptions() {
	b.subsMu.Lock()
	subs := b.subs
	b.subs = make(map[string]messaging.Subscription)
	b.subsMu.Unlock()

	for _, sub := range subs {
		if sub != nil {
			_ = sub.Close()
		}
	}
}

func (b *AgentMessagingBridge) sendEnvelope(env bridgeEnvelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}

	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	return b.sock.Send(zmq4.NewMsg(raw))
}

func splitBridgeTopic(namespace string, topic string) (string, string, string, error) {
	namespace = strings.TrimSpace(namespace)
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return "", "", "", fmt.Errorf("topic is required")
	}

	if namespace == "" {
		idx := strings.Index(topic, ":")
		if idx <= 0 {
			return "", "", "", fmt.Errorf("topic %q is not namespaced", topic)
		}
		namespace = topic[:idx]
		return namespace, topic[idx+1:], topic, nil
	}

	prefix := namespace + ":"
	if strings.HasPrefix(topic, prefix) {
		return namespace, topic[len(prefix):], topic, nil
	}

	return namespace, topic, prefix + topic, nil
}

func marshalMessages(messages []protocol.Message) ([]json.RawMessage, error) {
	if len(messages) == 0 {
		return []json.RawMessage{}, nil
	}

	out := make([]json.RawMessage, 0, len(messages))
	for _, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

func (e bridgeEnvelope) requestedMessageIDs() []uint64 {
	if len(e.MessageIDs) > 0 {
		return e.MessageIDs
	}
	return e.LegacyMessageIDs
}

func decodeBridgeMessage(raw json.RawMessage) (protocol.Message, error) {
	var message protocol.Message
	if err := json.Unmarshal(raw, &message); err == nil {
		return message, nil
	}

	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return protocol.Message{}, fmt.Errorf("decode publish message: %w", err)
	}
	if err := json.Unmarshal([]byte(encoded), &message); err != nil {
		return protocol.Message{}, fmt.Errorf("decode publish message: %w", err)
	}
	return message, nil
}
