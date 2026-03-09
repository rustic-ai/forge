import sys
import subprocess
from pathlib import Path

import pytest
from rustic_ai.core.guild.agent import Agent


class DummyAgent(Agent):
    def __init__(self, *args, **kwargs):
        pass

    def process(self, *args, **kwargs):
        pass


class DummyModule:
    def __getattr__(self, name):
        return DummyAgent


SKIPPED_YAML_FILES = {"code_review_guild.yaml", "test_react_guild.yaml"}

HELPER_SRC_DIR = (
    Path(__file__).resolve().parent.parent.parent.parent
    / "forge-go"
    / "testutil"
    / "contract"
)
FIXTURES_DIR = (
    Path(__file__).resolve().parent.parent.parent.parent
    / "forge-go"
    / "guild"
    / "testdata"
    / "e2e"
)


def _mock_missing_module(name):
    if name not in sys.modules:
        sys.modules[name] = DummyModule()


# Mock out fake modules referenced in E2E test YAMLs so Pydantic class validation passes
_mock_missing_module("helpers")
_mock_missing_module("helpers.scatter_gather_agents")
_mock_missing_module("rustic_ai.forge.messaging")
_mock_missing_module("rustic_ai.forge.messaging.redis_backend")
_mock_missing_module("rustic_ai.llm_agent")
_mock_missing_module("rustic_ai.llm_agent.react")


def get_fixture_yaml_files():
    """Returns fixture YAML files, excluding known incompatible ones."""
    return [f for f in FIXTURES_DIR.glob("*.yaml") if f.name not in SKIPPED_YAML_FILES]


@pytest.fixture
def fixture_yaml_files():
    return get_fixture_yaml_files()


@pytest.fixture(scope="session")
def helper_bin(tmp_path_factory):
    helper_dir = tmp_path_factory.mktemp("contract-helper")
    helper_bin = helper_dir / "contract_helper"
    result = subprocess.run(
        ["go", "build", "-o", str(helper_bin), "main.go"],
        cwd=str(HELPER_SRC_DIR),
        capture_output=True,
    )
    if result.returncode != 0:
        pytest.fail(
            "Failed to build contract helper from source: "
            + result.stderr.decode("utf-8")
        )
    return helper_bin
