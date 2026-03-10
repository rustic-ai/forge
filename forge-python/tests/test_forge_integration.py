import logging
import os
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Generator

import pytest
from redis import Redis

import sys


def _find_repo_root() -> Path:
    cur = Path(__file__).resolve()
    for candidate in [cur] + list(cur.parents):
        if (candidate / "forge-go").is_dir() and (candidate / "forge-python").is_dir():
            return candidate
        nested = candidate / "rustic-go"
        if (nested / "forge-go").is_dir() and (nested / "forge-python").is_dir():
            return nested
    raise RuntimeError(
        "unable to locate repo root containing forge-go and forge-python"
    )


# Add rustic-ai/core/tests to sys.path so we can import integration base helpers.
REPO_ROOT = _find_repo_root()
RUSTIC_AI_CORE = Path(
    os.getenv("RUSTIC_AI_CORE", str(REPO_ROOT.parent / "rustic-ai" / "core"))
)
FORGE_PYTHON_SRC = REPO_ROOT / "forge-python" / "src"

sys.path.insert(0, str(RUSTIC_AI_CORE / "src"))
sys.path.insert(0, str(RUSTIC_AI_CORE / "tests"))
sys.path.insert(0, str(FORGE_PYTHON_SRC))

try:
    from integration.execution.base_test_integration import IntegrationTestABC
    from integration.execution.integration_agents import (
        InitiatorProbeAgent,
        LocalTestAgent,
        ResponderProbeAgent,
    )
    from rustic_ai.core.messaging.core.messaging_config import MessagingConfig
except ImportError as e:
    pytest.skip(
        f"Could not import integration test classes: {e}", allow_module_level=True
    )

logger = logging.getLogger(__name__)


class TestForgeRedisIntegration(IntegrationTestABC):
    @pytest.fixture
    def guild_id(self) -> str:
        return "e2e-guild-1"

    @pytest.fixture(scope="module")
    def redis_server(self) -> Generator[int, None, None]:
        # Start a local redis-server
        port = 26379
        import shutil

        redis_bin = shutil.which("redis-server")
        if not redis_bin:
            pytest.skip("redis-server not found in PATH")

        proc = subprocess.Popen(
            [redis_bin, "--port", str(port)],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )

        # Wait for redis to be ready
        r = Redis(host="localhost", port=port)
        for _ in range(20):
            try:
                if r.ping():
                    break
            except Exception:
                time.sleep(0.5)
        else:
            proc.kill()
            pytest.fail("Could not start redis-server")

        os.environ["REDIS_PORT"] = str(port)
        yield port

        proc.terminate()
        proc.wait(timeout=5)

    @pytest.fixture
    def messaging(self, guild_id, redis_server) -> MessagingConfig:
        return MessagingConfig(
            backend_module="rustic_ai.redis.messaging.backend",
            backend_class="RedisMessagingBackend",
            backend_config={
                "redis_client": {
                    "host": "localhost",
                    "port": redis_server,
                    "db": 0,
                }
            },
        )

    @pytest.fixture
    def execution_engine(self) -> str:
        return "rustic_ai.forge.execution_engine.ForgeExecutionEngine"

    @pytest.fixture(autouse=True)
    def setup_forge_daemon(self, redis_server):
        # Create a registry.yaml mapping the test agent classes
        initiator_cls = (
            f"{InitiatorProbeAgent.__module__}.{InitiatorProbeAgent.__name__}"
        )
        responder_cls = (
            f"{ResponderProbeAgent.__module__}.{ResponderProbeAgent.__name__}"
        )
        local_cls = f"{LocalTestAgent.__module__}.{LocalTestAgent.__name__}"
        echo_cls = "rustic_ai.core.agents.testutils.echo_agent.EchoAgent"

        python_bin = sys.executable

        with tempfile.TemporaryDirectory() as tmpdir:
            registry_yaml = f"""
entries:
  - id: initiator
    class_name: {initiator_cls}
    runtime: binary
    executable: "{python_bin}"
    args: ["-m", "rustic_ai.forge.agent_runner"]
  - id: responder
    class_name: {responder_cls}
    runtime: binary
    executable: "{python_bin}"
    args: ["-m", "rustic_ai.forge.agent_runner"]
  - id: local
    class_name: {local_cls}
    runtime: binary
    executable: "{python_bin}"
    args: ["-m", "rustic_ai.forge.agent_runner"]
  - id: manager
    class_name: rustic_ai.forge.agents.system.guild_manager_agent.GuildManagerAgent
    runtime: binary
    executable: "{python_bin}"
    args: ["-m", "rustic_ai.forge.agent_runner"]
  - id: echo
    class_name: {echo_cls}
    runtime: binary
    executable: "{python_bin}"
    args: ["-m", "rustic_ai.forge.agent_runner"]
"""
            reg_path = Path(tmpdir) / "registry.yaml"
            reg_path.write_text(registry_yaml)

            # Start forge daemon
            forge_bin = REPO_ROOT / "forge-go" / "forge"

            # compile forge if not exists
            if not forge_bin.exists():
                subprocess.run(
                    ["go", "build", "-o", str(forge_bin), "main.go"],
                    cwd=str(REPO_ROOT / "forge-go"),
                    check=True,
                )

            db_path = Path(tmpdir) / "forge.db"
            spec_path = (
                REPO_ROOT
                / "forge-go"
                / "testutil"
                / "e2e"
                / "testdata"
                / "echo-guild.yaml"
            )

            env = os.environ.copy()
            env["REDIS_HOST"] = "localhost"
            env["REDIS_PORT"] = str(redis_server)
            env["PYTHONPATH"] = (
                f"{RUSTIC_AI_CORE / 'src'}:{RUSTIC_AI_CORE / 'tests'}:{FORGE_PYTHON_SRC}"
            )

            forge_log = open(Path(tmpdir) / "forge.log", "w")
            forge_proc = subprocess.Popen(
                [
                    str(forge_bin),
                    "run",
                    str(spec_path),
                    "--registry",
                    str(reg_path),
                    "--db-path",
                    str(db_path),
                    "--redis",
                    f"localhost:{redis_server}",
                ],
                env=env,
                stdout=forge_log,
                stderr=forge_log,
            )

            # Wait for Forge daemon to start up its listeners
            time.sleep(2)

            yield

            forge_proc.terminate()
            try:
                forge_proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                forge_proc.kill()

            forge_log.close()
            forge_log_content = (Path(tmpdir) / "forge.log").read_text()
            print(f"FORGE DAEMON LOGS:\n{forge_log_content}")
