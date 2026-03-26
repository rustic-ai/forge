package gateway

import (
	"fmt"

	"github.com/rustic-ai/forge/forge-go/infraevents"
	"go.opentelemetry.io/otel"
)

const (
	noTracing             = "no_tracing"
	systemTopic           = "system_topic"
	guildStatusTopic      = "guild_status_topic"
	infraEventsTopic      = infraevents.Topic
	userMessageBroadcast  = "user_message_broadcast"
	userProxyCreateFmt    = "rustic_ai.core.agents.system.models.UserAgentCreationRequest"
	messageWrapperFmt     = "rustic_ai.core.messaging.core.message.Message"
	healthCheckFmt        = "rustic_ai.core.guild.agent_ext.mixins.health.HealthCheckRequest"
	agentsHealthReportFmt = "rustic_ai.core.guild.agent_ext.mixins.health.AgentsHealthReport"
	participantListFmt    = "rustic_ai.core.agents.utils.user_proxy_agent.ParticipantList"
)

var wsTracer = otel.Tracer("rustic_ai")

func userTopic(userID string) string {
	return fmt.Sprintf("user:%s", userID)
}

func userNotificationsTopic(userID string) string {
	return fmt.Sprintf("user_notifications:%s", userID)
}

func userSystemRequestsTopic(userID string) string {
	return fmt.Sprintf("user_system:%s", userID)
}

func userSystemNotificationsTopic(userID string) string {
	return fmt.Sprintf("user_system_notification:%s", userID)
}
