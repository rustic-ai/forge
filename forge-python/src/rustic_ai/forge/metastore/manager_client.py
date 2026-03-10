from __future__ import annotations

from enum import Enum
from typing import Any, Optional
from urllib.parse import quote

import httpx
from rustic_ai.core.guild.agent import AgentSpec
from rustic_ai.core.guild.dsl import GuildSpec
from rustic_ai.core.guild.metastore.models import AgentStatus, GuildStatus
from rustic_ai.core.messaging.core.message import RoutingRule


class ManagerAPIError(RuntimeError):
    """Raised when manager metastore API calls fail."""


class ManagerMetastoreClient:
    def __init__(
        self,
        base_url: str,
        token: Optional[str] = None,
        timeout_seconds: float = 10.0,
        client: Optional[httpx.Client] = None,
    ) -> None:
        headers: dict[str, str] = {}
        if token:
            headers["X-Forge-Manager-Token"] = token
            headers["Authorization"] = f"Bearer {token}"

        self._auth_headers = headers
        self._owns_client = client is None
        self._client = client or httpx.Client(
            base_url=base_url.rstrip("/"),
            timeout=timeout_seconds,
            headers=headers,
        )

    def close(self) -> None:
        if self._owns_client:
            self._client.close()

    def __enter__(self) -> "ManagerMetastoreClient":
        return self

    def __exit__(self, *_: Any) -> None:
        self.close()

    def ensure_guild(
        self, guild_spec: GuildSpec, organization_id: str
    ) -> dict[str, Any]:
        payload = {
            "guild_spec": guild_spec.model_dump(mode="json", exclude_none=True),
            "organization_id": organization_id,
        }
        return self._request("POST", "/manager/guilds/ensure", json=payload)

    def get_guild_spec(self, guild_id: str) -> dict[str, Any]:
        gid = quote(guild_id, safe="")
        return self._request("GET", f"/manager/guilds/{gid}/spec")

    def update_guild_status(self, guild_id: str, status: GuildStatus) -> dict[str, Any]:
        gid = quote(guild_id, safe="")
        payload = {"status": _enum_wire_value(status)}
        return self._request("PATCH", f"/manager/guilds/{gid}/status", json=payload)

    def ensure_agent(self, guild_id: str, agent_spec: AgentSpec) -> dict[str, Any]:
        gid = quote(guild_id, safe="")
        payload = agent_spec.model_dump(mode="json", exclude_none=True)
        return self._request(
            "POST", f"/manager/guilds/{gid}/agents/ensure", json=payload
        )

    def update_agent_status(
        self,
        guild_id: str,
        agent_id: str,
        status: AgentStatus,
    ) -> dict[str, Any]:
        gid = quote(guild_id, safe="")
        aid = quote(agent_id, safe="")
        payload = {"status": _enum_wire_value(status)}
        return self._request(
            "PATCH",
            f"/manager/guilds/{gid}/agents/{aid}/status",
            json=payload,
        )

    def add_routing_rule(self, guild_id: str, rule: RoutingRule) -> dict[str, Any]:
        gid = quote(guild_id, safe="")
        payload = {"routing_rule": rule.model_dump(mode="json", exclude_none=True)}
        return self._request("POST", f"/manager/guilds/{gid}/routes", json=payload)

    def remove_routing_rule(self, guild_id: str, rule_hashid: str) -> dict[str, Any]:
        gid = quote(guild_id, safe="")
        rid = quote(rule_hashid, safe="")
        return self._request("DELETE", f"/manager/guilds/{gid}/routes/{rid}")

    def process_heartbeat(
        self,
        guild_id: str,
        agent_id: str,
        agent_status: AgentStatus,
        guild_status: GuildStatus,
    ) -> dict[str, Any]:
        gid = quote(guild_id, safe="")
        payload = {
            "agent_id": agent_id,
            "agent_status": _enum_wire_value(agent_status),
            "guild_status": _enum_wire_value(guild_status),
        }
        return self._request(
            "POST",
            f"/manager/guilds/{gid}/lifecycle/heartbeat",
            json=payload,
        )

    def _request(self, method: str, path: str, *, json: Any = None) -> dict[str, Any]:
        response = self._client.request(
            method, path, json=json, headers=self._auth_headers
        )

        if response.is_success:
            if response.status_code == 204 or not response.content:
                return {}
            return response.json()

        message = response.text
        try:
            payload = response.json()
            if isinstance(payload, dict):
                message = str(payload.get("error") or payload.get("message") or message)
        except Exception:
            pass

        raise ManagerAPIError(
            f"manager api {method} {path} failed ({response.status_code}): {message}"
        )


def _enum_wire_value(value: Any) -> Any:
    if isinstance(value, Enum):
        return value.value
    return value
