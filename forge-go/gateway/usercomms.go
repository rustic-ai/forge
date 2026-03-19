package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// parseThread extracts a uint64 thread slice from a JSON-decoded []interface{}.
func parseThread(raw interface{}) []uint64 {
	arr, ok := raw.([]interface{})
	if !ok {
		return []uint64{}
	}
	threads := make([]uint64, 0, len(arr))
	for _, tid := range arr {
		if tidNum, ok := parseUint(tid); ok {
			threads = append(threads, tidNum)
		}
	}
	return threads
}

type WireShapeMode int

const (
	WireShapeCanonical WireShapeMode = iota
	WireShapeProxyCompat
)

// UserCommsHandler upgrades an HTTP connection and routes bidirectional user pub/sub traffic
func UserCommsHandler(msgClient messaging.Backend, guildStore store.Store, gemGen *protocol.GemstoneGenerator) http.HandlerFunc {
	return userCommsHandler(msgClient, guildStore, gemGen, WireShapeCanonical)
}

func UserCommsProxyCompatHandler(msgClient messaging.Backend, guildStore store.Store, gemGen *protocol.GemstoneGenerator) http.HandlerFunc {
	return userCommsHandler(msgClient, guildStore, gemGen, WireShapeProxyCompat)
}

func userCommsHandler(msgClient messaging.Backend, guildStore store.Store, gemGen *protocol.GemstoneGenerator, wireShape WireShapeMode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := logging.FromContext(ctx, slog.Default())
		guildID := r.PathValue("id")
		userID := r.PathValue("user_id")
		userName := r.PathValue("user_name")
		if guildID == "" || userID == "" || userName == "" {
			http.Error(w, "missing path parameters", http.StatusBadRequest)
			return
		}

		if guildStore != nil {
			if _, err := guildStore.GetGuild(guildID); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					http.Error(w, "guild not found", http.StatusNotFound)
					return
				}
				http.Error(w, "failed to resolve guild", http.StatusInternalServerError)
				return
			}
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("failed to upgrade connection", "err", err)
			return
		}
		defer func() { _ = conn.Close() }()

		logger.Info("User socket connected", "guild", guildID, "user", userID, "name", userName)

		sub, err := msgClient.Subscribe(ctx, guildID, userNotificationsTopic(userID))
		if err != nil {
			logger.Error("Failed to subscribe", "err", err)
			return
		}
		defer func() { _ = sub.Close() }()

		creationGemID, _ := gemGen.Generate(protocol.PriorityNormal)

		payloadBytes, _ := json.Marshal(map[string]interface{}{
			"user_id":   userID,
			"user_name": userName,
		})

		senderID := fmt.Sprintf("user_socket:%s", userID)
		creationMsg := &protocol.Message{
			ID:      creationGemID.ToInt(),
			Topics:  protocol.TopicsFromString(systemTopic),
			Sender:  protocol.AgentTag{ID: &senderID, Name: &userName},
			Format:  userProxyCreateFmt,
			Payload: json.RawMessage(payloadBytes),
		}
		initMessageDefaults(creationMsg)
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

				_, sendSpan := wsTracer.Start(spanCtx, "websocket:send_message",
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
					logger.Error("usercomms: failed to marshal outgoing message", "err", err, "guild_id", guildID, "user_id", userID)
					sendSpan.End()
					continue
				}
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					logger.Warn("usercomms: write failed, stopping outbound delivery", "err", err, "guild_id", guildID, "user_id", userID)
					sendSpan.End()
					return
				}
				sendSpan.End()
			}
		}()

		_ = msgClient.PublishMessage(ctx, guildID, systemTopic, creationMsg)

		for {
			_, jsonBytes, err := conn.ReadMessage()
			if err != nil {
				logger.Info("User WebSocket disconnected cleanly", "user", userID)
				break
			}

			var userMsg map[string]interface{}
			if err := json.Unmarshal(jsonBytes, &userMsg); err != nil {
				logger.Warn("Dropped malformed user JSON")
				continue
			}
			if wireShape == WireShapeProxyCompat {
				userMsg = proxyNormalizeIncomingEnvelope(userMsg)
			}
			priority := parsePriority(userMsg["priority"])
			id, ok := parseIncomingGemstoneID(userMsg["id"])
			if !ok || id.Timestamp-(time.Now().UnixMilli()) > 1000 {
				id, _ = gemGen.Generate(priority)
			}
			senderID := fmt.Sprintf("user_socket:%s", userID)
			sender := protocol.AgentTag{ID: &senderID, Name: &userName}
			userMsg["id"] = id.ToInt()
			userMsg["sender"] = map[string]interface{}{
				"id":   senderID,
				"name": userName,
			}

			thread := parseThread(coalesceMapValue(userMsg, "thread", "threads"))
			thread = append(thread, id.ToInt())

			history, valid := parseMessageHistory(coalesceMapValue(userMsg, "message_history", "messageHistory"))
			if !valid {
				logger.Warn("usercomms: dropped message with invalid message_history", "guild_id", guildID, "user_id", userID)
				continue
			}

			format, _ := userMsg["format"].(string)
			spanCtx, receiveSpan := wsTracer.Start(ctx, "websocket:receive_message",
				trace.WithAttributes(
					attribute.String("user_id", userID),
					attribute.String("guild_id", guildID),
					attribute.String("message_id", fmt.Sprintf("id:%d", id.ToInt())),
					attribute.String("message_format", format),
				))

			carrier := propagation.MapCarrier{}
			otel.GetTextMapPropagator().Inject(spanCtx, carrier)
			traceparent := carrier.Get("traceparent")
			if traceparent == "" {
				traceparent = noTracing
			}

			normalizedPayload := normalizeUserEnvelope(userMsg, senderID, userName, id.ToInt(), thread, history, traceparent)
			pBytes, _ := json.Marshal(normalizedPayload)

			wrapped := &protocol.Message{
				ID:             id.ToInt(),
				Topics:         protocol.TopicsFromString(userTopic(userID)),
				Sender:         sender,
				Format:         messageWrapperFmt,
				Payload:        json.RawMessage(pBytes),
				Thread:         thread,
				Traceparent:    &traceparent,
				MessageHistory: history,
			}
			initMessageDefaults(wrapped)

			_ = msgClient.PublishMessage(ctx, guildID, userTopic(userID), wrapped)
			receiveSpan.End()
		}
	}
}

func parsePriority(raw interface{}) protocol.Priority {
	switch p := raw.(type) {
	case float64:
		v := int(p)
		if v < int(protocol.PriorityUrgent) || v > int(protocol.PriorityLowest) {
			return protocol.PriorityNormal
		}
		return protocol.Priority(v)
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			if i < int(protocol.PriorityUrgent) || i > int(protocol.PriorityLowest) {
				return protocol.PriorityNormal
			}
			return protocol.Priority(i)
		}
		switch strings.ToUpper(strings.TrimSpace(p)) {
		case "URGENT":
			return protocol.PriorityUrgent
		case "IMPORTANT":
			return protocol.PriorityImportant
		case "HIGH":
			return protocol.PriorityHigh
		case "ABOVE_NORMAL", "ABOVENORMAL":
			return protocol.PriorityAboveNormal
		case "LOW":
			return protocol.PriorityLow
		case "VERY_LOW", "VERYLOW":
			return protocol.PriorityVeryLow
		case "LOWEST":
			return protocol.PriorityLowest
		default:
			return protocol.PriorityNormal
		}
	default:
		return protocol.PriorityNormal
	}
}

func parseIncomingGemstoneID(raw interface{}) (protocol.GemstoneID, bool) {
	switch v := raw.(type) {
	case string:
		id, err := protocol.ParseGemstoneIDString(v)
		if err != nil {
			return protocol.GemstoneID{}, false
		}
		return id, true
	case float64:
		id, err := protocol.ParseGemstoneID(uint64(v))
		if err != nil {
			return protocol.GemstoneID{}, false
		}
		return id, true
	default:
		return protocol.GemstoneID{}, false
	}
}

func parseMessageHistory(raw interface{}) ([]protocol.ProcessEntry, bool) {
	if raw == nil {
		return []protocol.ProcessEntry{}, true
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]protocol.ProcessEntry, 0, len(arr))
	for _, entry := range arr {
		m, ok := entry.(map[string]interface{})
		if !ok {
			return nil, false
		}
		normalized, ok := normalizeProcessEntry(m)
		if !ok {
			return nil, false
		}
		out = append(out, normalized)
	}
	return out, true
}

func normalizeProcessEntry(entry map[string]interface{}) (protocol.ProcessEntry, bool) {
	agent, ok := entry["agent"].(map[string]interface{})
	if !ok {
		return protocol.ProcessEntry{}, false
	}
	var agentTag protocol.AgentTag
	if id, ok := agent["id"]; ok && id != nil {
		idStr, ok := id.(string)
		if !ok {
			return protocol.ProcessEntry{}, false
		}
		agentTag.ID = &idStr
	}
	if name, ok := agent["name"]; ok && name != nil {
		nameStr, ok := name.(string)
		if !ok {
			return protocol.ProcessEntry{}, false
		}
		agentTag.Name = &nameStr
	}
	origin, ok := parseUint(entry["origin"])
	if !ok {
		return protocol.ProcessEntry{}, false
	}
	result, ok := parseUint(entry["result"])
	if !ok {
		return protocol.ProcessEntry{}, false
	}
	processor, ok := entry["processor"].(string)
	if !ok {
		return protocol.ProcessEntry{}, false
	}
	pe := protocol.ProcessEntry{
		Agent:     agentTag,
		Origin:    origin,
		Result:    result,
		Processor: processor,
	}
	if fromTopic, ok := entry["from_topic"]; ok && fromTopic != nil {
		topic, ok := fromTopic.(string)
		if !ok {
			return protocol.ProcessEntry{}, false
		}
		pe.FromTopic = &topic
	}
	if toTopics, ok := entry["to_topics"]; ok && toTopics != nil {
		arr, ok := toTopics.([]interface{})
		if !ok {
			return protocol.ProcessEntry{}, false
		}
		normalizedTopics := make([]string, 0, len(arr))
		for _, t := range arr {
			topic, ok := t.(string)
			if !ok {
				return protocol.ProcessEntry{}, false
			}
			normalizedTopics = append(normalizedTopics, topic)
		}
		pe.ToTopics = normalizedTopics
	}
	if reason, ok := entry["reason"]; ok && reason != nil {
		arr, ok := reason.([]interface{})
		if !ok {
			return protocol.ProcessEntry{}, false
		}
		normalizedReasons := make([]string, 0, len(arr))
		for _, r := range arr {
			reasonText, ok := r.(string)
			if !ok {
				return protocol.ProcessEntry{}, false
			}
			normalizedReasons = append(normalizedReasons, reasonText)
		}
		pe.Reason = normalizedReasons
	}
	pe.Normalize()
	return pe, true
}

func normalizeUserEnvelope(
	userMsg map[string]interface{},
	senderID string,
	senderName string,
	id uint64,
	thread []uint64,
	history []protocol.ProcessEntry,
	traceparent string,
) map[string]interface{} {
	format, _ := userMsg["format"].(string)
	topics := normalizeTopics(coalesceMapValue(userMsg, "topics", "topic"))
	payload := coalesceMapValue(userMsg, "payload", "data")
	if payload == nil {
		payload = map[string]interface{}{}
	}
	recipientList := normalizeRecipientList(coalesceMapValue(userMsg, "recipient_list", "recipientList"))
	normalized := map[string]interface{}{
		"id": id,
		"sender": map[string]interface{}{
			"id":   senderID,
			"name": senderName,
		},
		"topics":          topics,
		"recipient_list":  recipientList,
		"payload":         payload,
		"format":          normalizeIncomingFormat(format),
		"thread":          thread,
		"message_history": history,
		"traceparent":     traceparent,
	}
	if inResponseTo, ok := parseUint(coalesceMapValue(userMsg, "in_response_to", "inReplyTo")); ok {
		normalized["in_response_to"] = inResponseTo
	}
	if conversationID, ok := parseUint(coalesceMapValue(userMsg, "conversation_id", "conversationId")); ok {
		normalized["conversation_id"] = conversationID
	}
	if forwardHeader := coalesceMapValue(userMsg, "forward_header", "forwardHeader"); forwardHeader != nil {
		normalized["forward_header"] = forwardHeader
	}
	if routingSlip := coalesceMapValue(userMsg, "routing_slip", "routingSlip"); routingSlip != nil {
		normalized["routing_slip"] = routingSlip
	}
	if ttl, ok := parseInt(userMsg["ttl"]); ok {
		normalized["ttl"] = ttl
	}
	if isErrorMessage, ok := userMsg["is_error_message"].(bool); ok {
		normalized["is_error_message"] = isErrorMessage
	}
	if sessionState := coalesceMapValue(userMsg, "session_state", "sessionState"); sessionState != nil {
		normalized["session_state"] = sessionState
	}
	if topicPublishedTo, ok := coalesceMapValue(userMsg, "topic_published_to", "topicPublishedTo").(string); ok && topicPublishedTo != "" {
		normalized["topic_published_to"] = topicPublishedTo
	}
	if enrichWithHistory, ok := parseInt(coalesceMapValue(userMsg, "enrich_with_history", "enrichWithHistory")); ok {
		normalized["enrich_with_history"] = enrichWithHistory
	}
	if processStatus, ok := coalesceMapValue(userMsg, "process_status", "processStatus").(string); ok && processStatus != "" {
		normalized["process_status"] = processStatus
	}
	if originGuildStack := coalesceMapValue(userMsg, "origin_guild_stack", "originGuildStack"); originGuildStack != nil {
		normalized["origin_guild_stack"] = originGuildStack
	}
	return normalized
}

func normalizeIncomingFormat(format string) string {
	if format == "" {
		return format
	}
	formatAliases := map[string]string{
		"healthcheck":           healthCheckFmt,
		"questionResponse":      "rustic_ai.core.ui_protocol.types.QuestionResponse",
		"formResponse":          "rustic_ai.core.ui_protocol.types.FormResponse",
		"participantsRequest":   "rustic_ai.core.agents.utils.user_proxy_agent.ParticipantListRequest",
		"chatCompletionRequest": "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionRequest",
		"stopGuildRequest":      "rustic_ai.core.agents.system.models.StopGuildRequest",
		"ChatCompletionRequest": "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionRequest",
	}
	if qualified, ok := formatAliases[format]; ok {
		return qualified
	}
	return format
}

func normalizeTopics(raw interface{}) interface{} {
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return "default_topic"
		}
		return v
	case []interface{}:
		topics := make([]string, 0, len(v))
		for _, entry := range v {
			topic, ok := entry.(string)
			if !ok || strings.TrimSpace(topic) == "" {
				continue
			}
			topics = append(topics, topic)
		}
		if len(topics) == 0 {
			return "default_topic"
		}
		return topics
	default:
		return "default_topic"
	}
}

func normalizeRecipientList(raw interface{}) []interface{} {
	switch v := raw.(type) {
	case []interface{}:
		return v
	default:
		return []interface{}{}
	}
}

func coalesceMapValue(values map[string]interface{}, keys ...string) interface{} {
	for _, key := range keys {
		v, ok := values[key]
		if ok && v != nil {
			return v
		}
	}
	return nil
}

func parseUint(raw interface{}) (uint64, bool) {
	switch v := raw.(type) {
	case float64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case uint64:
		return v, true
	case string:
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func parseInt(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
