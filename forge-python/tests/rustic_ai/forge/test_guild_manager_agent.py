from rustic_ai.core.guild.agent_ext.mixins.health import HeartbeatStatus
from rustic_ai.core.guild.metastore.models import AgentStatus, GuildStatus

from rustic_ai.forge.agents.system.guild_manager_agent import GuildManagerAgent


def test_guild_status_from_health_mapping():
    assert (
        GuildManagerAgent._guild_status_from_health(HeartbeatStatus.OK)
        == GuildStatus.RUNNING
    )
    assert (
        GuildManagerAgent._guild_status_from_health(HeartbeatStatus.WARNING)
        == GuildStatus.WARNING
    )
    assert (
        GuildManagerAgent._guild_status_from_health(HeartbeatStatus.BACKLOGGED)
        == GuildStatus.BACKLOGGED
    )
    assert (
        GuildManagerAgent._guild_status_from_health(HeartbeatStatus.ERROR)
        == GuildStatus.ERROR
    )
    assert (
        GuildManagerAgent._guild_status_from_health(HeartbeatStatus.UNKNOWN)
        == GuildStatus.UNKNOWN
    )


def test_agent_status_from_heartbeat_mapping():
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.OK)
        == AgentStatus.RUNNING
    )
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.WARNING)
        == AgentStatus.RUNNING
    )
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.BACKLOGGED)
        == AgentStatus.RUNNING
    )
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.STARTING)
        == AgentStatus.STARTING
    )
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.PENDING_LAUNCH)
        == AgentStatus.PENDING_LAUNCH
    )
    assert (
        GuildManagerAgent._heartbeat_to_agent_status(HeartbeatStatus.ERROR)
        == AgentStatus.ERROR
    )
