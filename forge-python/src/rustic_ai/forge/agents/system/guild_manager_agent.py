from __future__ import annotations

from datetime import datetime
import logging
from typing import List, Optional

from rustic_ai.core.agents.commons.message_formats import ErrorMessage
from rustic_ai.core.agents.system.models import (
    AddRoutingRuleRequest,
    AgentGetRequest,
    AgentInfoResponse,
    AgentLaunchRequest,
    AgentLaunchResponse,
    AgentListRequest,
    AgentListResponse,
    AgentRemovalResponse,
    BadInputResponse,
    ConflictResponse,
    GuildUpdatedAnnouncement,
    RemoveAgentRequest,
    RemoveRoutingRuleRequest,
    RoutingRuleUpdateResponse,
    RunningAgentListRequest,
    StopGuildRequest,
    StopGuildResponse,
    UserAgentCreationRequest,
    UserAgentCreationResponse,
    UserAgentGetRequest,
)
from rustic_ai.core.agents.utils.user_proxy_agent import (
    UserProxyAgent,
    UserProxyAgentProps,
)
from rustic_ai.core.guild import Agent, AgentSpec, GuildTopics, agent
from rustic_ai.core.guild.agent import ProcessContext, SelfReadyNotification, processor
from rustic_ai.core.guild.agent_ext.mixins.health import (
    AgentsHealthReport,
    HealthCheckRequest,
    HealthConstants,
    Heartbeat,
    HeartbeatStatus,
)
from rustic_ai.core.guild.builders import AgentBuilder, GuildBuilder, GuildHelper
from rustic_ai.core.guild.dsl import GuildSpec
from rustic_ai.core.guild.guild import Guild
from rustic_ai.core.guild.metastore.models import AgentStatus, GuildStatus
from rustic_ai.core.state.manager.state_manager import StateManager
from rustic_ai.core.state.models import (
    StateFetchError,
    StateFetchRequest,
    StateFetchResponse,
    StateOwner,
    StateUpdateError,
    StateUpdateRequest,
    StateUpdateResponse,
)
from rustic_ai.core.utils.basic_class_utils import get_qualified_class_name
from rustic_ai.core.utils.class_utils import get_state_manager
from rustic_ai.core.utils.priority import Priority

from rustic_ai.forge.agents.system.guild_manager_agent_props import (
    GuildManagerAgentProps,
)
from rustic_ai.forge.metastore.manager_client import (
    ManagerAPIError,
    ManagerMetastoreClient,
)


class GuildManagerAgent(Agent[GuildManagerAgentProps]):
    def __init__(self):
        props = self.agent_spec.props
        guild_spec = props.guild_spec
        self.organization_id = props.organization_id

        self.metastore = ManagerMetastoreClient(
            base_url=props.manager_api_base_url,
            token=props.manager_api_token,
        )

        self.original_guild_spec = guild_spec
        guild_id = guild_spec.id

        state_manager_config = GuildHelper.get_state_mgr_config(guild_spec)
        self.state_manager: StateManager = get_state_manager(
            GuildHelper.get_state_manager(guild_spec),
            state_manager_config,
        )

        logging.info("Guild Manager initializing guild %s", guild_id)

        ensure_resp = self.metastore.ensure_guild(guild_spec, self.organization_id)
        persisted_spec = GuildSpec.model_validate(ensure_resp["guild_spec"])
        self.guild_spec = persisted_spec

        self.agent_health: dict = {}
        self.launch_triggered = False

        for agent_spec in self.guild_spec.agents:
            if agent_spec.id == self.id:
                continue
            self.agent_health[agent_spec.id] = Heartbeat(
                checktime=datetime.now(),
                checkstatus=HeartbeatStatus.PENDING_LAUNCH,
                checkmeta={},
            ).model_dump()
            self.agent_health[agent_spec.id]["status"] = AgentStatus.PENDING_LAUNCH  # type: ignore[index]

        self.state_manager.update_state(
            StateUpdateRequest(
                state_owner=StateOwner.GUILD,
                guild_id=guild_id,
                agent_id=self.id,
                update_path="agents.health",
                state_update=self.agent_health,
            )
        )

        self.guild: Optional[Guild] = None

    def _add_agent(self, agent_spec: AgentSpec) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        self.guild.launch_agent(agent_spec)
        self.metastore.ensure_agent(self.guild_id, agent_spec)

        self.agent_health[agent_spec.id] = Heartbeat(
            checktime=datetime.now(),
            checkstatus=HeartbeatStatus.PENDING_LAUNCH,
            checkmeta={},
        ).model_dump()

    def _announce_guild_refresh(self, ctx: ProcessContext) -> None:
        try:
            guild_resp = self.metastore.get_guild_spec(self.guild_id)
        except ManagerAPIError as err:
            logging.error("Failed to fetch guild for refresh: %s", err)
            ctx.send_error(
                ErrorMessage(
                    agent_type=self.get_qualified_class_name(),
                    error_type="GUILD_UPDATE_ANNOUNCEMENT_FAILED",
                    error_message=str(err),
                )
            )
            return

        guild_spec = GuildSpec.model_validate(guild_resp["guild_spec"])
        self.guild_spec = guild_spec

        guild_updated_announcement = GuildUpdatedAnnouncement(
            guild_id=self.guild_id,
            guild_spec=guild_spec,
        )
        ctx._direct_send(
            priority=Priority.NORMAL,
            format=get_qualified_class_name(GuildUpdatedAnnouncement),
            payload=guild_updated_announcement.model_dump(),
            topics=[GuildTopics.GUILD_STATUS_TOPIC],
        )

        guild_state = self.state_manager.get_state(
            StateFetchRequest(state_owner=StateOwner.GUILD, guild_id=self.guild_id)
        )

        ctx._direct_send(
            priority=Priority.NORMAL,
            format=get_qualified_class_name(StateFetchResponse),
            payload=guild_state.model_dump(),
            topics=[GuildTopics.GUILD_STATUS_TOPIC],
        )

    def _launch_guild_agents(self, ctx: ProcessContext) -> None:
        if self.launch_triggered:
            return

        self.launch_triggered = True

        for agent_spec in self.original_guild_spec.agents:
            if agent_spec.id == self.id:
                continue
            self.agent_health[agent_spec.id] = Heartbeat(
                checktime=datetime.now(),
                checkstatus=HeartbeatStatus.STARTING,
                checkmeta={},
            ).model_dump()

        self.state_manager.update_state(
            StateUpdateRequest(
                state_owner=StateOwner.GUILD,
                guild_id=self.guild_id,
                agent_id=self.id,
                update_path="agents.health",
                state_update=self.agent_health,
            )
        )

        ensure_resp = self.metastore.ensure_guild(self.guild_spec, self.organization_id)
        persisted_spec = GuildSpec.model_validate(ensure_resp["guild_spec"])
        guild_status = GuildStatus(ensure_resp["status"])

        if guild_status == GuildStatus.PENDING_LAUNCH:
            logging.info("Guild Manager launching guild %s", self.guild_id)
            self.guild = GuildBuilder.from_spec(persisted_spec).launch(
                self.organization_id
            )
        else:
            logging.info("Guild Manager loading guild %s", self.guild_id)
            self.guild = GuildBuilder.from_spec(persisted_spec).load_or_launch(
                self.organization_id
            )

        self.guild.register_agent(self.get_spec())
        self.metastore.ensure_agent(self.guild_id, self.get_spec())
        self.metastore.update_guild_status(self.guild_id, GuildStatus.STARTING)

        if self.agent_health:
            ctx._direct_send(
                priority=Priority.NORMAL,
                format=get_qualified_class_name(AgentsHealthReport),
                payload=AgentsHealthReport.model_validate(
                    {
                        "agents": {
                            k: Heartbeat.model_validate(v)
                            for k, v in self.agent_health.items()
                        }
                    }
                ).model_dump(),
                topics=[GuildTopics.GUILD_STATUS_TOPIC],
            )

        ctx._direct_send(
            priority=Priority.NORMAL,
            format=get_qualified_class_name(HealthCheckRequest),
            payload=HealthCheckRequest().model_dump(),
            topics=[HealthConstants.HEARTBEAT_TOPIC],
        )

    @processor(
        SelfReadyNotification,
        predicate=lambda self, msg: msg.sender == self.get_agent_tag()
        and msg.topic_published_to == self._self_inbox,
        handle_essential=True,
    )
    def launch_guild_agents(self, ctx: ProcessContext[SelfReadyNotification]) -> None:
        self._launch_guild_agents(ctx)

    @processor(AgentLaunchRequest)
    def launch_agent(self, ctx: ProcessContext[AgentLaunchRequest]) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        aar = ctx.payload
        self.guild.launch_agent(aar.agent_spec)
        self.metastore.ensure_agent(self.guild_id, aar.agent_spec)

        self.state_manager.update_state(
            StateUpdateRequest(
                state_owner=StateOwner.GUILD,
                guild_id=self.guild_id,
                agent_id=self.id,
                update_path=f'agents.health["{aar.agent_spec.id}"]',
                state_update=Heartbeat(
                    checktime=datetime.now(),
                    checkstatus=HeartbeatStatus.STARTING,
                    checkmeta={},
                ).model_dump(),
            )
        )

        ctx.send(
            AgentLaunchResponse(
                agent_id=aar.agent_spec.id,
                status_code=201,
                status="Agent launched successfully",
            )
        )
        self._announce_guild_refresh(ctx)

    @processor(RemoveAgentRequest)
    def remove_agent(self, ctx: ProcessContext[RemoveAgentRequest]) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        rar = ctx.payload
        try:
            self.guild.remove_agent(rar.agent_id)
        except ValueError:
            logging.warning(
                "Agent %s was not running when remove was requested", rar.agent_id
            )

        status_resp = self.metastore.update_agent_status(
            self.guild_id,
            rar.agent_id,
            AgentStatus.DELETED,
        )
        if not status_resp.get("found", False):
            ctx.send_error(
                ErrorMessage(
                    agent_type=self.get_qualified_class_name(),
                    error_type="AGENT_NOT_FOUND",
                    error_message=f"Agent {rar.agent_id} not found in guild {self.guild_id}",
                )
            )
            return

        ctx.send(
            AgentRemovalResponse(
                agent_id=rar.agent_id,
                status_code=200,
                status="Agent removed successfully",
            )
        )
        self._announce_guild_refresh(ctx)

    @processor(AddRoutingRuleRequest)
    def add_routing_rule(self, ctx: ProcessContext[AddRoutingRuleRequest]) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        rule = ctx.payload.routing_rule
        add_resp = self.metastore.add_routing_rule(self.guild_id, rule)
        self.guild.routes.add_step(rule)

        ctx.send(
            RoutingRuleUpdateResponse(
                rule_hashid=add_resp["rule_hashid"],
                status_code=201,
                status="Routing rule added successfully",
            )
        )
        self._announce_guild_refresh(ctx)

    @processor(RemoveRoutingRuleRequest)
    def remove_routing_rule(
        self, ctx: ProcessContext[RemoveRoutingRuleRequest]
    ) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        target_hashid = ctx.payload.rule_hashid
        remove_resp = self.metastore.remove_routing_rule(self.guild_id, target_hashid)
        if not remove_resp.get("deleted", False):
            ctx.send_error(
                ErrorMessage(
                    agent_type=self.get_qualified_class_name(),
                    error_type="RULE_NOT_FOUND",
                    error_message=f"Routing rule with hashid {target_hashid} not found",
                )
            )
            return

        self.guild.routes.steps = [
            step for step in self.guild.routes.steps if step.hashid != target_hashid
        ]

        ctx.send(
            RoutingRuleUpdateResponse(
                rule_hashid=target_hashid,
                status_code=200,
                status="Routing rule removed successfully",
            )
        )
        self._announce_guild_refresh(ctx)

    @processor(AgentListRequest)
    def list_agents(self, ctx: ProcessContext[AgentListRequest]) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        alr = ctx.payload
        if alr.guild_id != self.guild_id:
            ctx.send(
                BadInputResponse(
                    error_field="guild_id", message=f"Invalid guild id: {alr.guild_id}"
                )
            )
            return

        agents: List[AgentInfoResponse] = [
            AgentInfoResponse(
                id=guild_agent.id,
                name=guild_agent.name,
                description=guild_agent.description,
                class_name=guild_agent.class_name,
            )
            for guild_agent in self.guild.list_agents()
        ]
        ctx.send(AgentListResponse(agents=agents))

    @staticmethod
    def _guild_status_from_health(aggregated_health: HeartbeatStatus) -> GuildStatus:
        if aggregated_health == HeartbeatStatus.OK:
            return GuildStatus.RUNNING
        if aggregated_health == HeartbeatStatus.WARNING:
            return GuildStatus.WARNING
        if aggregated_health == HeartbeatStatus.STARTING:
            return GuildStatus.STARTING
        if aggregated_health == HeartbeatStatus.BACKLOGGED:
            return GuildStatus.BACKLOGGED
        if aggregated_health == HeartbeatStatus.ERROR:
            return GuildStatus.ERROR
        return GuildStatus.UNKNOWN

    @staticmethod
    def _heartbeat_to_agent_status(heartbeat_status: HeartbeatStatus) -> AgentStatus:
        if heartbeat_status in [
            HeartbeatStatus.OK,
            HeartbeatStatus.WARNING,
            HeartbeatStatus.BACKLOGGED,
        ]:
            return AgentStatus.RUNNING
        if heartbeat_status == HeartbeatStatus.STARTING:
            return AgentStatus.STARTING
        if heartbeat_status == HeartbeatStatus.PENDING_LAUNCH:
            return AgentStatus.PENDING_LAUNCH
        return AgentStatus.ERROR

    def _process_heartbeat_and_update_status(
        self,
        heartbeat: Heartbeat,
        sender_id: str,
        health_report: AgentsHealthReport,
    ) -> AgentStatus:
        guild_status = self._guild_status_from_health(health_report.guild_health)
        new_agent_status = self._heartbeat_to_agent_status(heartbeat.checkstatus)

        heartbeat_resp = self.metastore.process_heartbeat(
            guild_id=self.guild_id,
            agent_id=sender_id,
            agent_status=new_agent_status,
            guild_status=guild_status,
        )

        if heartbeat_resp.get("agent_status"):
            return AgentStatus(heartbeat_resp["agent_status"])
        return new_agent_status

    def _check_and_refresh_guild(
        self,
        ctx: ProcessContext,
        sender_id: str,
        prev_status: Optional[HeartbeatStatus],
        new_agent_status: AgentStatus,
    ) -> None:
        should_refresh = (
            sender_id != self.id
            and new_agent_status == AgentStatus.RUNNING
            and (
                prev_status is None
                or prev_status == HeartbeatStatus.STARTING
                or prev_status == HeartbeatStatus.PENDING_LAUNCH
            )
        )
        if should_refresh:
            self._announce_guild_refresh(ctx)

    @processor(Heartbeat, handle_essential=True)
    def update_agent_status(self, ctx: ProcessContext[Heartbeat]) -> None:
        heartbeat = ctx.payload
        sender_id = ctx.message.sender.id

        prev_raw = (self.agent_health.get(sender_id) or {}).get("checkstatus")  # type: ignore[union-attr]
        prev_status: Optional[HeartbeatStatus] = None
        if isinstance(prev_raw, HeartbeatStatus):
            prev_status = prev_raw
        elif isinstance(prev_raw, str):
            try:
                prev_status = HeartbeatStatus(prev_raw)
            except ValueError:
                prev_status = None

        self.state_manager.update_state(
            StateUpdateRequest(
                state_owner=StateOwner.GUILD,
                guild_id=self.guild_id,
                agent_id=self.id,
                update_path=f'agents.health["{sender_id}"]',
                state_update=heartbeat.model_dump(),
            )
        )

        agent_health_response = self.state_manager.get_state(
            StateFetchRequest(
                state_owner=StateOwner.GUILD,
                guild_id=self.guild_id,
                agent_id=self.id,
                state_path="agents.health",
            )
        )

        if agent_health_response.state:
            health_report = AgentsHealthReport.model_validate(
                {
                    "agents": {
                        k: Heartbeat.model_validate(v)
                        for k, v in agent_health_response.state.items()
                    }
                }
            )
            new_agent_status = self._process_heartbeat_and_update_status(
                heartbeat,
                sender_id,
                health_report,
            )

            ctx._direct_send(
                priority=Priority.NORMAL,
                format=get_qualified_class_name(AgentsHealthReport),
                payload=health_report.model_dump(),
                topics=[GuildTopics.GUILD_STATUS_TOPIC],
            )
            self._check_and_refresh_guild(ctx, sender_id, prev_status, new_agent_status)

    @processor(RunningAgentListRequest)
    def list_running_agents(self, ctx: ProcessContext[RunningAgentListRequest]) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        alr = ctx.payload
        if alr.guild_id != self.guild_id:
            ctx.send(
                BadInputResponse(
                    error_field="guild_id", message=f"Invalid guild id: {alr.guild_id}"
                )
            )
            return

        agents: List[AgentInfoResponse] = [
            AgentInfoResponse(
                id=guild_agent.id,
                name=guild_agent.name,
                description=guild_agent.description,
                class_name=guild_agent.class_name,
            )
            for guild_agent in self.guild.list_all_running_agents()
        ]
        ctx.send(AgentListResponse(agents=agents))

    @processor(AgentGetRequest)
    def get_agent(self, ctx: ProcessContext[AgentGetRequest]) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        agr = ctx.payload
        if agr.guild_id != self.guild_id:
            ctx.send(
                BadInputResponse(
                    error_field="guild_id", message=f"Invalid guild id: {agr.guild_id}"
                )
            )
            return

        guild_agent = self.guild.get_agent(agr.agent_id)
        if not guild_agent:
            ctx.send(
                BadInputResponse(
                    error_field="agent_id", message=f"Invalid agent id: {agr.agent_id}"
                )
            )
            return

        ctx.send(
            AgentInfoResponse(
                id=guild_agent.id,
                name=guild_agent.name,
                description=guild_agent.description,
                class_name=guild_agent.class_name,
            )
        )

    @processor(UserAgentCreationRequest)
    def create_user_agent(
        self, ctx: agent.ProcessContext[UserAgentCreationRequest]
    ) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        uacr = ctx.payload
        user_agent_id = UserProxyAgent.get_user_agent_id(uacr.user_id)
        if self.guild.get_agent(user_agent_id):
            ctx.send(
                ConflictResponse(
                    error_field="user_id",
                    message=f"Agent for user {uacr.user_id} already exists",
                )
            )
            return

        user_agent_spec = (
            AgentBuilder(UserProxyAgent)
            .set_id(user_agent_id)
            .set_name(uacr.user_name)
            .set_description(f"Agent for user {uacr.user_id}")
            .set_properties(UserProxyAgentProps(user_id=uacr.user_id))
            .add_additional_topic(UserProxyAgent.get_user_inbox_topic(uacr.user_id))
            .add_additional_topic(UserProxyAgent.get_user_outbox_topic(uacr.user_id))
            .add_additional_topic(
                UserProxyAgent.get_user_system_notifications_topic(uacr.user_id)
            )
            .add_additional_topic(
                UserProxyAgent.get_user_system_requests_topic(uacr.user_id)
            )
            .add_additional_topic(GuildTopics.GUILD_STATUS_TOPIC)
            .add_additional_topic(UserProxyAgent.BROADCAST_TOPIC)
            .build_spec()
        )

        user_topic = UserProxyAgent.get_user_inbox_topic(uacr.user_id)
        self._add_agent(user_agent_spec)

        self.state_manager.update_state(
            StateUpdateRequest(
                state_owner=StateOwner.GUILD,
                guild_id=self.guild_id,
                agent_id=self.id,
                update_path="agents.health",
                state_update={
                    user_agent_spec.id: Heartbeat(
                        checktime=datetime.now(),
                        checkstatus=HeartbeatStatus.STARTING,
                        checkmeta={},
                    ).model_dump()
                },
            )
        )

        ctx.send(
            UserAgentCreationResponse(
                user_id=uacr.user_id,
                agent_id=user_agent_spec.id,
                status_code=201,
                status="Agent created successfully",
                topic=user_topic,
            )
        )
        self._announce_guild_refresh(ctx)

    @processor(UserAgentGetRequest)
    def get_user_agent(self, ctx: ProcessContext[UserAgentGetRequest]) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        user_id = ctx.payload.user_id
        user_agent = self.guild.get_agent(UserProxyAgent.get_user_agent_id(user_id))
        if not user_agent:
            ctx.send(
                BadInputResponse(
                    error_field="user_id", message=f"Invalid user id: {user_id}"
                )
            )
            return

        ctx.send(
            AgentInfoResponse(
                id=user_agent.id,
                name=user_agent.name,
                description=user_agent.description,
                class_name=user_agent.class_name,
            )
        )

    @processor(StateFetchRequest, handle_essential=True)
    def get_state_handler(self, ctx: ProcessContext[StateFetchRequest]) -> None:
        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        try:
            state = self.state_manager.get_state(ctx.payload)
            ctx._direct_send(
                priority=Priority.NORMAL,
                format=get_qualified_class_name(StateFetchResponse),
                payload=state.model_dump(),
                topics=[GuildTopics.STATE_TOPIC],
            )
        except Exception as err:
            ctx.send_error(
                StateFetchError(state_fetch_request=ctx.payload, error=str(err))
            )

    @processor(StateUpdateRequest, handle_essential=True)
    def update_state_handler(self, ctx: ProcessContext[StateUpdateRequest]) -> None:
        if getattr(self, "_shutting_down", False) and getattr(
            self, "_is_shutdown", False
        ):
            return

        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        try:
            state_update = self.state_manager.update_state(ctx.payload)
            ctx._direct_send(
                priority=Priority.NORMAL,
                format=get_qualified_class_name(StateUpdateResponse),
                payload=state_update.model_dump(),
                topics=[GuildTopics.STATE_TOPIC],
            )
        except Exception as err:
            ctx.send_error(
                StateUpdateError(state_update_request=ctx.payload, error=str(err))
            )

    @processor(StopGuildRequest, handle_essential=True)
    def stop_guild(self, ctx: ProcessContext[StopGuildRequest]) -> None:
        ctx._direct_send(
            priority=Priority.NORMAL,
            format=get_qualified_class_name(StopGuildResponse),
            payload=StopGuildResponse(user_id=ctx.payload.user_id).model_dump(),
            topics=[GuildTopics.GUILD_STATUS_TOPIC],
        )

        self._shutting_down = True

        if self.guild is None:
            raise RuntimeError("Guild is not initialized")

        if ctx.payload.guild_id == self.guild_id:
            self.metastore.update_guild_status(self.guild_id, GuildStatus.STOPPING)

            for agent_spec in self.guild.list_agents():
                if agent_spec.id != self.id:
                    self.guild.remove_agent(agent_spec.id)

            self.metastore.update_guild_status(self.guild_id, GuildStatus.STOPPED)
            self.guild.remove_agent(self.id)

        self._is_shutdown = True

    @processor(HealthCheckRequest, handle_essential=True)
    def send_heartbeat(self, ctx: ProcessContext[HealthCheckRequest]):
        if getattr(self, "_shutting_down", False) and getattr(
            self, "_is_shutdown", False
        ):
            return

        status = HeartbeatStatus.OK
        checkmeta: dict = {}
        qos_latency = self.agent_spec.qos.latency
        time_now = datetime.now()
        checktime = ctx.payload.checktime
        msg_latency = (time_now - checktime).total_seconds() * 1000
        if qos_latency and msg_latency > qos_latency:
            status = HeartbeatStatus.BACKLOGGED

        checkmeta["qos_latency"] = qos_latency
        checkmeta["observed_latency"] = msg_latency

        ctx.send(
            Heartbeat(checktime=checktime, checkstatus=status, checkmeta=checkmeta)
        )

        if not self.launch_triggered:
            self._launch_guild_agents(ctx)
