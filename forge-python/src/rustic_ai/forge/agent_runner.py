import json
import logging
import os
import sys

from rustic_ai.core.guild.agent import AgentSpec
from rustic_ai.core.guild.dsl import GuildSpec
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


def _load_guild_spec(guild_spec_json: str) -> GuildSpec:
    """
    Load the guild spec this agent belongs to.

    Validation is tolerant of agent classes that are not importable in *this* process:
    an agent only installs the packages it needs, so a sibling's plugin classes are
    routinely absent here. rusticai-core (>=1.3.0) keeps such specs as opaque properties
    rather than failing, and re-validates strictly when an agent is actually built.
    """
    return GuildSpec.model_validate_json(guild_spec_json)


def _load_agent_spec(agent_spec_json: str) -> AgentSpec:
    """
    Load this process's own agent spec.

    Unlike the guild spec, this one is expected to be fully resolvable here — the agent's
    own classes must be importable in its own environment. Declare any plugin packages the
    spec's properties reference via `forge_extra_deps` on the agent spec.
    """
    return AgentSpec.model_validate_json(agent_spec_json)


def _load_backend_config(client_props: dict, client_type_str: str) -> dict:
    if "backend_config" in client_props:
        raw_backend_config = client_props["backend_config"]
    else:
        raw_backend_config = {
            key: value
            for key, value in client_props.items()
            if key != "organization_id"
        }
    if not isinstance(raw_backend_config, dict):
        raise ValueError(
            "FORGE_CLIENT_PROPERTIES_JSON backend_config must be a JSON object."
        )

    backend_config = dict(raw_backend_config)

    if client_type_str == SUPERVISOR_ZMQ_BACKEND_CLASS:
        raw_supervisor_config = os.getenv(SUPERVISOR_ZMQ_CONFIG_ENV, "")
        if raw_supervisor_config:
            supervisor_config = json.loads(raw_supervisor_config)
            if not isinstance(supervisor_config, dict):
                raise ValueError(
                    f"{SUPERVISOR_ZMQ_CONFIG_ENV} must decode to a JSON object."
                )
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
            raise ValueError(
                "FORGE_CLIENT_PROPERTIES_JSON must decode to a JSON object."
            )

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
