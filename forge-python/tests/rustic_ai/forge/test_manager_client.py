import httpx
import pytest

from rustic_ai.core.guild.agent import AgentSpec
from rustic_ai.core.guild.dsl import GuildSpec, QOSSpec, ResourceSpec
from rustic_ai.core.guild.metastore.models import AgentStatus, GuildStatus
from rustic_ai.core.messaging.core.message import RoutingRule
from rustic_ai.forge.metastore.manager_client import (
    ManagerAPIError,
    ManagerMetastoreClient,
)


def test_manager_client_ensure_and_heartbeat_roundtrip():
    calls = []

    def handler(request: httpx.Request) -> httpx.Response:
        calls.append(
            (request.method, request.url.path, dict(request.headers), request.content)
        )

        if request.url.path == "/manager/guilds/ensure":
            body = {
                "guild_spec": {
                    "id": "g-1",
                    "name": "Guild",
                    "description": "desc",
                    "properties": {},
                    "agents": [],
                    "dependency_map": {},
                    "routes": {"steps": []},
                    "gateway": None,
                },
                "status": "not_launched",
                "was_created": True,
            }
            return httpx.Response(200, json=body)

        if request.url.path == "/manager/guilds/g-1/lifecycle/heartbeat":
            return httpx.Response(
                200,
                json={
                    "agent_id": "a-1",
                    "agent_status": "running",
                    "guild_status": "running",
                    "agent_found": True,
                },
            )

        return httpx.Response(404, json={"error": "not found"})

    client = httpx.Client(
        transport=httpx.MockTransport(handler), base_url="http://forge.test"
    )
    metastore = ManagerMetastoreClient("http://forge.test", token="tkn", client=client)

    spec = GuildSpec(id="g-1", name="Guild", description="desc")
    ensure_resp = metastore.ensure_guild(spec, "org-1")
    assert ensure_resp["status"] == "not_launched"

    heartbeat_resp = metastore.process_heartbeat(
        "g-1",
        "a-1",
        AgentStatus.RUNNING,
        GuildStatus.RUNNING,
    )
    assert heartbeat_resp["agent_status"] == "running"

    assert calls[0][0] == "POST"
    assert calls[0][1] == "/manager/guilds/ensure"
    assert calls[0][2]["x-forge-manager-token"] == "tkn"


def test_manager_client_request_payload_shapes():
    captured = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured[request.url.path] = request.content.decode("utf-8")
        return httpx.Response(
            200, json={"ok": True, "rule_hashid": "h", "deleted": True}
        )

    client = httpx.Client(
        transport=httpx.MockTransport(handler), base_url="http://forge.test"
    )
    metastore = ManagerMetastoreClient("http://forge.test", client=client)

    agent_spec = AgentSpec.model_construct(
        id="a-1",
        name="A1",
        description="d",
        class_name="test.Agent",
        properties={},
        additional_topics=[],
        dependency_map={},
        additional_dependencies=[],
        predicates={},
        resources=ResourceSpec(),
        qos=QOSSpec(),
    )
    rule = RoutingRule(agent_type="test.Agent")

    metastore.ensure_agent("g-1", agent_spec)
    metastore.add_routing_rule("g-1", rule)
    metastore.remove_routing_rule("g-1", "hash-1")

    assert "/manager/guilds/g-1/agents/ensure" in captured
    assert '"id":"a-1"' in captured["/manager/guilds/g-1/agents/ensure"]
    assert "/manager/guilds/g-1/routes" in captured


def test_manager_client_raises_on_error_status():
    def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(401, json={"error": "unauthorized"})

    client = httpx.Client(
        transport=httpx.MockTransport(handler), base_url="http://forge.test"
    )
    metastore = ManagerMetastoreClient("http://forge.test", client=client)

    with pytest.raises(ManagerAPIError):
        metastore.get_guild_spec("g-1")
