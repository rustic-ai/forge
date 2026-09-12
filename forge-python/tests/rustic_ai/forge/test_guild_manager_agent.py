from types import SimpleNamespace
from unittest.mock import Mock

from rustic_ai.core.agents.system.models import AgentLaunchRequest, ConflictResponse
from rustic_ai.core.guild import AgentSpec
from rustic_ai.core.guild.agent_ext.mixins.health import HeartbeatStatus
from rustic_ai.core.guild.agent_ext.depends.dependency_resolver import DependencySpec
from rustic_ai.core.guild.metastore.models import AgentStatus, GuildStatus
from rustic_ai.core.guild.metaprog.agent_registry import AgentDependency, AgentRegistry

import rustic_ai.forge.agents.system.guild_manager_agent as guild_manager_module
from rustic_ai.forge.agents.system.guild_manager_agent import GuildManagerAgent


def dynamic_manager(monkeypatch) -> GuildManagerAgent:
    manager = object.__new__(GuildManagerAgent)
    manager.guild_spec = SimpleNamespace(
        properties={
            "dependency_selections": {
                "models": {
                    "dependency_key": "llm",
                    "required_type": "example.LLM",
                    "profiles": {
                        "llm_qwen": {
                            "display_name": "Qwen",
                            "dependency_key": "snapshot_llm",
                        }
                    },
                },
                "filesystems": {
                    "dependency_key": "filesystem",
                    "required_type": "example.Filesystem",
                    "profiles": {
                        "filesystem_local": {
                            "display_name": "Local Filesystem",
                            "dependency_key": "snapshot_filesystem",
                        }
                    },
                },
            }
        },
        dependency_map={
            "snapshot_llm": DependencySpec(class_name="example.Qwen"),
            "snapshot_filesystem": DependencySpec(
                class_name="example.LocalFilesystem"
            ),
        },
    )
    registry_entry = SimpleNamespace(
        agent_dependencies=[
            AgentDependency(dependency_key="llm", required_type="example.LLM"),
            AgentDependency(
                dependency_key="filesystem", required_type="example.Filesystem"
            ),
        ]
    )
    monkeypatch.setattr(guild_manager_module, "get_agent_class", lambda _: object)
    monkeypatch.setattr(
        AgentRegistry,
        "get_agent",
        classmethod(lambda _cls, _class_name: registry_entry),
    )
    return manager


def launch_request(
    agent_spec: AgentSpec, dependency_selections: dict
) -> AgentLaunchRequest:
    return AgentLaunchRequest(
        agent_spec=agent_spec,
        dependency_selections=dependency_selections,
    )


def test_dynamic_catalog_selector_matches_key_display_name_and_alias():
    profiles = {
        "llm_openai": {
            "display_name": "OpenAI GPT-5.4",
            "aliases": ["gpt-5.4", "gpt"],
        }
    }

    for selector in ("llm_openai", "openai gpt-5.4", "GPT"):
        matches = GuildManagerAgent._match_catalog_profiles(profiles, selector)
        assert matches == [("llm_openai", profiles["llm_openai"])]


def test_dynamic_catalog_selector_reports_ambiguity_to_caller():
    profiles = {
        "first": {"display_name": "First", "aliases": ["gpt"]},
        "second": {"display_name": "Second", "aliases": ["GPT"]},
    }

    assert len(GuildManagerAgent._match_catalog_profiles(profiles, "gpt")) == 2


def test_dependency_materialization_preserves_requested_agent_identity(monkeypatch):
    manager = dynamic_manager(monkeypatch)
    requested = AgentSpec(
        id="reviewer-a",
        name="Strict Reviewer",
        description="Reviews an answer",
        class_name="example.Agent",
        properties={},
    )

    materialized, profile_keys = manager._materialize_dependency_selections(
        launch_request(
            requested,
            {"llm": {"catalog_key": "models", "selector": "Qwen"}},
        )
    )

    assert materialized.id == "reviewer-a"
    assert materialized.name == "Strict Reviewer"
    assert materialized is not requested
    assert materialized.dependency_map["llm"].class_name == "example.Qwen"
    assert profile_keys == ["llm_qwen"]


def test_dependency_materialization_preserves_generated_agent_id(monkeypatch):
    manager = dynamic_manager(monkeypatch)
    requested = AgentSpec(
        name="Generated Reviewer",
        description="Reviews an answer",
        class_name="example.Agent",
        properties={},
    )

    materialized, _ = manager._materialize_dependency_selections(
        launch_request(
            requested,
            {"llm": {"catalog_key": "models", "selector": "llm_qwen"}},
        )
    )

    assert requested.id
    assert materialized.id == requested.id


def test_multiple_profiles_are_order_independent_and_do_not_define_identity(
    monkeypatch,
):
    manager = dynamic_manager(monkeypatch)
    agent = AgentSpec(
        id="reviewer",
        name="Reviewer",
        description="Reviews an answer",
        class_name="example.Agent",
        properties={},
    )
    selections = {
        "llm": {"catalog_key": "models", "selector": "llm_qwen"},
        "filesystem": {
            "catalog_key": "filesystems",
            "selector": "filesystem_local",
        },
    }

    first, first_profiles = manager._materialize_dependency_selections(
        launch_request(agent, selections)
    )
    second, second_profiles = manager._materialize_dependency_selections(
        launch_request(agent, dict(reversed(list(selections.items()))))
    )

    assert first.id == second.id == "reviewer"
    assert first.name == second.name == "Reviewer"
    assert first.dependency_map == second.dependency_map
    assert first_profiles == second_profiles == ["filesystem_local", "llm_qwen"]


def test_distinct_agents_can_share_the_same_profile(monkeypatch):
    manager = dynamic_manager(monkeypatch)
    selection = {"llm": {"catalog_key": "models", "selector": "llm_qwen"}}

    first, _ = manager._materialize_dependency_selections(
        launch_request(
            AgentSpec(
                id="reviewer-a",
                name="Reviewer A",
                description="First reviewer",
                class_name="example.Agent",
                properties={"prompt": "Be strict"},
            ),
            selection,
        )
    )
    second, _ = manager._materialize_dependency_selections(
        launch_request(
            AgentSpec(
                id="reviewer-b",
                name="Reviewer B",
                description="Second reviewer",
                class_name="example.Agent",
                properties={"prompt": "Be creative"},
            ),
            selection,
        )
    )

    assert first.id == "reviewer-a"
    assert second.id == "reviewer-b"
    assert first.dependency_map == second.dependency_map


def test_dynamic_launch_rejects_a_duplicate_agent_name(monkeypatch):
    manager = dynamic_manager(monkeypatch)
    existing = AgentSpec(
        id="reviewer-a",
        name="Reviewer",
        description="Existing reviewer",
        class_name="example.Agent",
        properties={},
    )
    requested = AgentSpec(
        id="reviewer-b",
        name="Reviewer",
        description="New reviewer",
        class_name="example.Agent",
        properties={},
    )
    manager.guild = SimpleNamespace(list_agents=lambda: [existing])
    manager.metastore = Mock()
    ctx = SimpleNamespace(
        payload=launch_request(
            requested,
            {"llm": {"catalog_key": "models", "selector": "llm_qwen"}},
        ),
        send=Mock(),
    )

    GuildManagerAgent.launch_agent.__wfn__(manager, ctx)

    response = ctx.send.call_args.args[0]
    assert isinstance(response, ConflictResponse)
    assert response.error_field == "name"
    assert response.message == "Agent name already exists: Reviewer"
    manager.metastore.ensure_agent.assert_not_called()


def test_existing_agent_id_is_not_a_name_conflict():
    existing = AgentSpec(
        id="reviewer-a",
        name="Reviewer",
        description="Existing reviewer",
        class_name="example.Agent",
        properties={},
    )
    requested = existing.model_copy(deep=True)

    assert GuildManagerAgent._find_agent_name_conflict([existing], requested) is None


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
