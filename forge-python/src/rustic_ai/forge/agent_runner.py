import json
import logging
import os
import sys

from pydantic import ConfigDict, ValidationError
from rustic_ai.core.guild.agent import AgentSpec
from rustic_ai.core.guild.dsl import BaseAgentProps, GuildSpec
from rustic_ai.core.guild.metaprog.constants import MetaclassConstants
import rustic_ai.core.guild.dsl as guild_dsl
from rustic_ai.core.guild import Agent
from rustic_ai.core.guild.metastore import models as metastore_models
from rustic_ai.core.messaging.core.message import RoutingSlip
from rustic_ai.core.messaging import MessagingConfig

from rustic_ai.forge.agent_wrapper import ForgeAgentWrapper

log_level = os.getenv("LOG_LEVEL", "INFO").upper()

logging.basicConfig(
    level=getattr(logging, log_level),
    format="[%(asctime)s] %(levelname)s [%(name)s.%(funcName)s:%(lineno)d] %(message)s",
    handlers=[logging.StreamHandler(sys.stdout)],
    force=True,
)

logger = logging.getLogger("forge.runner")

SUPERVISOR_ZMQ_BACKEND_CLASS = "SupervisorZmqMessagingBackend"
SUPERVISOR_ZMQ_BACKEND_MODULE = "rustic_ai.forge.messaging.supervisor_backend"
SUPERVISOR_ZMQ_ENDPOINT_ENV = "FORGE_SUPERVISOR_ZMQ_ENDPOINT"
SUPERVISOR_ZMQ_CONFIG_ENV = "FORGE_SUPERVISOR_ZMQ_CONFIG_JSON"


class _UnresolvedAgent(Agent):
    """
    Placeholder used only for validation fallback when an agent class cannot be imported yet.
    """

    __annotations__ = {MetaclassConstants.AGENT_PROPS_TYPE: BaseAgentProps}

    async def run(self):
        return None


class _UnresolvedAgentProps(BaseAgentProps):
    model_config = ConfigDict(extra="allow")


_UnresolvedAgent.__annotations__ = {
    MetaclassConstants.AGENT_PROPS_TYPE: _UnresolvedAgentProps
}


_ORIGINAL_GET_CLASS_FROM_NAME = guild_dsl.get_class_from_name
_LENIENT_CLASS_RESOLUTION_ENABLED = False
_LENIENT_METASTORE_ENABLED = False

_ORIGINAL_TO_AGENT_SPEC = metastore_models.AgentModel.to_agent_spec


def _enable_lenient_class_resolution() -> None:
    global _LENIENT_CLASS_RESOLUTION_ENABLED
    if _LENIENT_CLASS_RESOLUTION_ENABLED:
        return

    def _lenient_get_class_from_name(class_name: str):
        try:
            return _ORIGINAL_GET_CLASS_FROM_NAME(class_name)
        except Exception:
            logger.warning(
                "Using unresolved placeholder for class during validation: %s",
                class_name,
            )
            return _UnresolvedAgent

    guild_dsl.get_class_from_name = _lenient_get_class_from_name
    _LENIENT_CLASS_RESOLUTION_ENABLED = True


def _enable_lenient_metastore_conversion() -> None:
    global _LENIENT_METASTORE_ENABLED
    if _LENIENT_METASTORE_ENABLED:
        return

    def _to_agent_spec_lenient(self):
        try:
            return _ORIGINAL_TO_AGENT_SPEC(self)
        except ValidationError as e:
            logger.warning(
                "AgentModel.to_agent_spec validation failed for %s; using lenient construct fallback: %s",
                self.class_name,
                e,
            )
            return AgentSpec.model_construct(
                id=self.id,
                name=self.name,
                description=self.description,
                class_name=self.class_name,
                properties=self.properties or {},
                additional_topics=self.additional_topics or [],
                listen_to_default_topic=self.listen_to_default_topic,
                act_only_when_tagged=self.act_only_when_tagged,
                dependency_map=self.dependency_map or {},
                additional_dependencies=self.additional_dependencies or [],
                predicates=self.predicates or {},
                resources={},
                qos={},
            )

    metastore_models.AgentModel.to_agent_spec = _to_agent_spec_lenient
    _LENIENT_METASTORE_ENABLED = True


def _load_guild_spec(guild_spec_json: str) -> GuildSpec:
    """
    Load GuildSpec with a tolerant fallback.

    Strict validation can fail when guild JSON contains agent classes that are not yet
    importable in this process (for example dynamically downloaded uvx deps). In that
    case we preserve enough structure to start the current agent process.
    """
    try:
        return GuildSpec.model_validate_json(guild_spec_json)
    except ValidationError as e:
        logger.warning(
            "GuildSpec strict validation failed; falling back to lenient parsing for startup: %s",
            e,
        )
        _enable_lenient_class_resolution()
        _enable_lenient_metastore_conversion()
        try:
            return GuildSpec.model_validate_json(guild_spec_json)
        except Exception:
            logger.warning(
                "Lenient GuildSpec re-validation failed; using structural fallback."
            )

    raw = json.loads(guild_spec_json)
    if not isinstance(raw, dict):
        raise ValueError("FORGE_GUILD_JSON must decode to a JSON object.")

    raw_agents = raw.get("agents") or []
    fallback_agents: list[AgentSpec] = []
    for i, raw_agent in enumerate(raw_agents):
        if not isinstance(raw_agent, dict):
            logger.warning("Skipping malformed guild agent entry at index %s", i)
            continue
        fallback_agents.append(
            AgentSpec.model_construct(
                id=str(raw_agent.get("id") or f"agent-{i}"),
                name=str(raw_agent.get("name") or f"agent-{i}"),
                description=str(raw_agent.get("description") or ""),
                class_name=str(raw_agent.get("class_name") or ""),
                additional_topics=list(raw_agent.get("additional_topics") or []),
                properties=raw_agent.get("properties", {}),
                listen_to_default_topic=bool(
                    raw_agent.get("listen_to_default_topic", True)
                ),
                act_only_when_tagged=bool(raw_agent.get("act_only_when_tagged", False)),
                predicates=raw_agent.get("predicates") or {},
                dependency_map=raw_agent.get("dependency_map") or {},
                additional_dependencies=list(
                    raw_agent.get("additional_dependencies") or []
                ),
                resources=raw_agent.get("resources") or {},
                qos=raw_agent.get("qos") or {},
            )
        )

    routes_raw = raw.get("routes") or {}
    try:
        routes = RoutingSlip.model_validate(routes_raw)
    except Exception:
        routes = RoutingSlip()

    return GuildSpec.model_construct(
        id=str(raw.get("id") or ""),
        name=str(raw.get("name") or ""),
        description=str(raw.get("description") or ""),
        properties=raw.get("properties") or {},
        agents=fallback_agents,
        dependency_map=raw.get("dependency_map") or {},
        routes=routes,
        gateway=raw.get("gateway"),
    )


def _load_agent_spec(agent_spec_json: str) -> AgentSpec:
    try:
        return AgentSpec.model_validate_json(agent_spec_json)
    except ValidationError as e:
        logger.warning(
            "AgentSpec strict validation failed; retrying with lenient class resolution: %s",
            e,
        )
        _enable_lenient_class_resolution()
        _enable_lenient_metastore_conversion()
        return AgentSpec.model_validate_json(agent_spec_json)


def _load_backend_config(client_props: dict, client_type_str: str) -> dict:
    if "backend_config" in client_props:
        raw_backend_config = client_props["backend_config"]
    else:
        raw_backend_config = {
            key: value for key, value in client_props.items() if key != "organization_id"
        }
    if not isinstance(raw_backend_config, dict):
        raise ValueError("FORGE_CLIENT_PROPERTIES_JSON backend_config must be a JSON object.")

    backend_config = dict(raw_backend_config)

    if client_type_str == SUPERVISOR_ZMQ_BACKEND_CLASS:
        raw_supervisor_config = os.getenv(SUPERVISOR_ZMQ_CONFIG_ENV, "")
        if raw_supervisor_config:
            supervisor_config = json.loads(raw_supervisor_config)
            if not isinstance(supervisor_config, dict):
                raise ValueError(f"{SUPERVISOR_ZMQ_CONFIG_ENV} must decode to a JSON object.")
            backend_config = supervisor_config | backend_config

        endpoint = os.getenv(SUPERVISOR_ZMQ_ENDPOINT_ENV, "")
        if endpoint and "endpoint" not in backend_config:
            backend_config["endpoint"] = endpoint

    return backend_config


def main():
    try:
        logger.info("Starting Forge Agent Runner...")

        guild_spec_json = os.getenv("FORGE_GUILD_JSON")
        if not guild_spec_json:
            raise ValueError("FORGE_GUILD_JSON environment variable is missing.")

        guild_spec = _load_guild_spec(guild_spec_json)
        logger.debug(f"Loaded GuildSpec: {guild_spec.id} ({guild_spec.name})")

        agent_spec_json = os.getenv("FORGE_AGENT_CONFIG_JSON")
        if not agent_spec_json:
            raise ValueError("FORGE_AGENT_CONFIG_JSON environment variable is missing.")

        agent_spec = _load_agent_spec(agent_spec_json)
        logger.info(f"Loaded AgentSpec: {agent_spec.id} ({agent_spec.name})")

        client_type_str = os.getenv("FORGE_CLIENT_TYPE", "InMemoryMessagingBackend")
        client_module_str = os.getenv("FORGE_CLIENT_MODULE", "")
        client_props = json.loads(os.getenv("FORGE_CLIENT_PROPERTIES_JSON", "{}"))
        if not isinstance(client_props, dict):
            raise ValueError("FORGE_CLIENT_PROPERTIES_JSON must decode to a JSON object.")

        backend_config = _load_backend_config(client_props, client_type_str)

        if client_type_str == SUPERVISOR_ZMQ_BACKEND_CLASS and not client_module_str:
            client_module_str = SUPERVISOR_ZMQ_BACKEND_MODULE

        if (
            client_type_str == "RedisMessagingBackend"
            and "redis_client" not in backend_config
        ):
            backend_config["redis_client"] = {
                "host": os.getenv("REDIS_HOST", "localhost"),
                "port": int(os.getenv("REDIS_PORT", "6379")),
                "db": int(os.getenv("REDIS_DB", "0")),
            }
        elif (
            client_type_str == "NATSMessagingBackend"
            and "nats_client" not in backend_config
        ):
            nats_url = os.getenv("NATS_URL", "nats://localhost:4222")
            backend_config["nats_client"] = {"servers": [nats_url]}

        organization_id = client_props.pop("organization_id", None)

        messaging_config = MessagingConfig(
            backend_module=client_module_str,
            backend_class=client_type_str,
            backend_config=backend_config,
        )

        logger.info(f"Using Messaging Backend: {client_type_str}")

        machine_id = hash(f"{guild_spec.id}-{agent_spec.id}") % 256

        wrapper = ForgeAgentWrapper(
            guild_spec=guild_spec,
            agent_spec=agent_spec,
            messaging_config=messaging_config,
            machine_id=machine_id,
            organization_id=organization_id,
        )

        wrapper.run()

        logger.info("Forge Agent Runner exited gracefully.")
        sys.exit(0)

    except Exception as e:
        logger.critical(f"Forge Agent Runner crashed: {e}", exc_info=True)
        sys.exit(1)


if __name__ == "__main__":
    main()
