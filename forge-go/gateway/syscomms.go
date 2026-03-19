package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/helper/logging"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func SysCommsHandler(msgClient messaging.Backend, guildStore store.Store, gemGen *protocol.GemstoneGenerator) http.HandlerFunc {
	return sysCommsHandler(msgClient, guildStore, gemGen, WireShapeCanonical)
}

func SysCommsProxyCompatHandler(msgClient messaging.Backend, guildStore store.Store, gemGen *protocol.GemstoneGenerator) http.HandlerFunc {
	return sysCommsHandler(msgClient, guildStore, gemGen, WireShapeProxyCompat)
}

func sysCommsHandler(msgClient messaging.Backend, guildStore store.Store, gemGen *protocol.GemstoneGenerator, wireShape WireShapeMode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := logging.FromContext(ctx, slog.Default())
		guildID := r.PathValue("id")
		userID := r.PathValue("user_id")
		if guildID == "" || userID == "" {
			http.Error(w, "missing syscomms path parameters", http.StatusBadRequest)
			return
		}

		if guildStore != nil {
			if _, err := guildStore.GetGuild(guildID); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					logger.Error("Guild not found for syscomms socket", "guild", guildID)
					http.Error(w, "guild not found", http.StatusNotFound)
					return
				}
				logger.Error("Failed to resolve guild for syscomms socket", "guild", guildID, "err", err)
				http.Error(w, "failed to resolve guild", http.StatusInternalServerError)
				return
			}
		}

		logger.Info("System socket connected", "guild", guildID, "user", userID)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("syscomms failed websocket upgrade", "err", err)
			return
		}
		defer func() { _ = conn.Close() }()

		socketSenderID := fmt.Sprintf("sys_comms_socket:%s", userID)

		sub, err := msgClient.Subscribe(ctx, guildID, userSystemNotificationsTopic(userID), guildStatusTopic)
		if err != nil {
			logger.Error("Failed to subscribe syscomms", "err", err)
			return
		}
		defer func() { _ = sub.Close() }()

		healthCheckGemID, _ := gemGen.Generate(protocol.PriorityHigh)
		// Python emits naive datetime strings for HealthCheckRequest (no timezone suffix).
		// Keep the same format to avoid offset-naive/offset-aware math failures in manager agents.
		payloadBytes, _ := json.Marshal(map[string]interface{}{
			"checktime": time.Now().Format("2006-01-02T15:04:05.999999"),
		})
		healthCheck := &protocol.Message{
			ID:      healthCheckGemID.ToInt(),
			Topics:  protocol.TopicsFromSlice([]string{guildStatusTopic}),
			Sender:  protocol.AgentTag{ID: &socketSenderID},
			Format:  healthCheckFmt,
			Payload: json.RawMessage(payloadBytes),
		}
		initMessageDefaults(healthCheck)

		go func() {
			for subMsg := range sub.Channel() {
				m := subMsg.Message

				spanCtx := ctx
				if m.Traceparent != nil && *m.Traceparent != "" && *m.Traceparent != noTracing {
					carrier := propagation.MapCarrier{"traceparent": *m.Traceparent}
					spanCtx = otel.GetTextMapPropagator().Extract(context.Background(), carrier)
				}

				sID, sName := "UNKNOWN", "UNKNOWN"
				if m.Sender.ID != nil {
					sID = *m.Sender.ID
				}
				if m.Sender.Name != nil {
					sName = *m.Sender.Name
				}
				topicPub := "UNKNOWN"
				if m.TopicPublishedTo != nil {
					topicPub = *m.TopicPublishedTo
				}
				rootThread := uint64(0)
				if len(m.Thread) > 0 {
					rootThread = m.Thread[0]
				}

				_, sendSpan := wsTracer.Start(spanCtx, "websocket:send_sys_message",
					trace.WithAttributes(
						attribute.String("user_id", userID),
						attribute.String("guild_id", guildID),
						attribute.String("message_id", fmt.Sprintf("id:%d", m.ID)),
						attribute.String("message_format", m.Format),
						attribute.String("message_topic", topicPub),
						attribute.String("agent_id", sID),
						attribute.String("agent_name", sName),
						attribute.String("root_thread_id", fmt.Sprintf("id:%d", rootThread)),
					))

				var (
					b   []byte
					err error
				)
				if wireShape == WireShapeProxyCompat {
					b, err = ProxyMarshalOutgoingMessage(*m, guildID, r)
				} else {
					b, err = json.Marshal(m)
				}
				if err != nil {
					logger.Error("syscomms: failed to marshal outgoing message", "err", err, "guild_id", guildID, "user_id", userID)
					sendSpan.End()
					continue
				}
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					logger.Warn("syscomms: write failed, stopping outbound delivery", "err", err, "guild_id", guildID, "user_id", userID)
					sendSpan.End()
					return
				}
				sendSpan.End()
			}
		}()

		_ = msgClient.PublishMessage(ctx, guildID, guildStatusTopic, healthCheck)

		for {
			_, jsonBytes, err := conn.ReadMessage()
			if err != nil {
				logger.Info("Systems disconnect", "user", userID)
				break
			}

			var userMsg map[string]interface{}
			if err := json.Unmarshal(jsonBytes, &userMsg); err != nil {
				continue
			}
			if wireShape == WireShapeProxyCompat {
				userMsg = proxyNormalizeIncomingEnvelope(userMsg)
			}
			fStr, _ := userMsg["format"].(string)
			if fStr == "" || userMsg["payload"] == nil {
				// Python parity: ignore inbound syscomms without both format and payload.
				continue
			}

			priority := parsePriority(userMsg["priority"])
			gemID, ok := parseIncomingGemstoneID(userMsg["id"])
			if !ok || gemID.Timestamp-(time.Now().UnixMilli()) > 1000 {
				gemID, _ = gemGen.Generate(priority)
			}

			pBytes, _ := json.Marshal(userMsg["payload"])

			wrapped := &protocol.Message{
				ID:      gemID.ToInt(),
				Topics:  protocol.TopicsFromString(userSystemRequestsTopic(userID)),
				Sender:  protocol.AgentTag{ID: &socketSenderID},
				Format:  fStr,
				Payload: json.RawMessage(pBytes),
			}
			initMessageDefaults(wrapped)

			// Python syscomms resets thread to only the current message id.
			wrapped.Thread = []uint64{wrapped.ID}

			spanCtx, receiveSpan := wsTracer.Start(ctx, "websocket:receive_sys_message",
				trace.WithAttributes(
					attribute.String("user_id", userID),
					attribute.String("guild_id", guildID),
					attribute.String("message_id", fmt.Sprintf("id:%d", gemID.ToInt())),
					attribute.String("message_format", fStr),
				))

			carrier := propagation.MapCarrier{}
			otel.GetTextMapPropagator().Inject(spanCtx, carrier)
			tp := carrier.Get("traceparent")
			if tp == "" {
				tp = noTracing
			}
			wrapped.Traceparent = &tp

			_ = msgClient.PublishMessage(ctx, guildID, userSystemRequestsTopic(userID), wrapped)
			receiveSpan.End()
		}
	}
}
