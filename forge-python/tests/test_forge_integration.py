import fcntl
import logging
import os
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Generator

import pytest
import requests
from redis import Redis


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("", 0))
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        return s.getsockname()[1]


def _build_forge_binary(forge_bin: Path, repo_root: Path) -> None:
    """Build the forge binary with a file lock to prevent concurrent builds.

    When pytest-xdist runs tests in parallel, multiple workers may try to
    compile the same binary simultaneously. A file lock serializes builds;
    the second worker skips the build if the binary is < 60 seconds old.
    """
    lock_path = forge_bin.parent / ".forge.build.lock"
    with open(lock_path, "w") as lock_file:
        fcntl.flock(lock_file, fcntl.LOCK_EX)
        try:
            if forge_bin.exists() and (
                time.time() - os.path.getmtime(str(forge_bin)) < 60
            ):
                return
            subprocess.run(
                ["go", "build", "-o", str(forge_bin), "main.go"],
                cwd=str(repo_root / "forge-go"),
                check=True,
            )
        finally:
            fcntl.flock(lock_file, fcntl.LOCK_UN)


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
RUSTIC_AI_REDIS = Path(
    os.getenv("RUSTIC_AI_REDIS", str(REPO_ROOT.parent / "rustic-ai" / "redis"))
)
FORGE_PYTHON_SRC = REPO_ROOT / "forge-python" / "src"

sys.path.insert(0, str(RUSTIC_AI_CORE / "src"))
sys.path.insert(0, str(RUSTIC_AI_REDIS / "src"))
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


@pytest.mark.xdist_group("forge_server")
class TestForgeRedisIntegration(IntegrationTestABC):
    @pytest.fixture
    def wait_time(self) -> float:
        # Forge agents are out-of-process — give them extra time to start
        # and for messages to round-trip through Redis under parallel load.
        return 1.0

    @pytest.fixture
    def guild_id(self) -> str:
        return "e2e-guild-1"

    @pytest.fixture(scope="module")
    def redis_server(self) -> Generator[int, None, None]:
        # Start a local redis-server on a dynamically allocated free port
        port = _free_port()
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

            # Build forge binary (serialized across workers to avoid concurrent build slowdowns)
            _build_forge_binary(forge_bin, REPO_ROOT)

            db_path = Path(tmpdir) / "forge.db"
            listen_port = _free_port()

            env = os.environ.copy()
            env["REDIS_HOST"] = "localhost"
            env["REDIS_PORT"] = str(redis_server)
            env["FORGE_AGENT_REGISTRY"] = str(reg_path)
            env["PYTHONPATH"] = (
                f"{RUSTIC_AI_CORE / 'src'}:{RUSTIC_AI_REDIS / 'src'}:{RUSTIC_AI_CORE / 'tests'}:{FORGE_PYTHON_SRC}"
            )

            forge_log = open(Path(tmpdir) / "forge.log", "w")
            forge_proc = subprocess.Popen(
                [
                    str(forge_bin),
                    "server",
                    "--db",
                    f"sqlite://{db_path}",
                    "--redis",
                    f"localhost:{redis_server}",
                    "--listen",
                    f":{listen_port}",
                    "--with-client",
                    "--client-node-id",
                    "integration-node-1",
                ],
                env=env,
                stdout=forge_log,
                stderr=forge_log,
            )

            # Wait for Forge daemon HTTP server to be ready
            server_url = f"http://localhost:{listen_port}"
            for _ in range(40):
                try:
                    resp = requests.get(f"{server_url}/ping", timeout=1)
                    if resp.status_code == 200:
                        break
                except Exception:
                    time.sleep(0.5)
            else:
                forge_proc.kill()
                pytest.fail(
                    "Could not start Forge daemon. Log: "
                    + (Path(tmpdir) / "forge.log").read_text()
                )

            # Wait for the in-process client node to register (required before spawning agents)
            for _ in range(40):
                try:
                    resp = requests.get(f"{server_url}/nodes", timeout=1)
                    if resp.status_code == 200 and len(resp.json()) > 0:
                        break
                except Exception:
                    pass
                time.sleep(0.5)
            else:
                forge_proc.kill()
                pytest.fail(
                    "Forge client node never registered. Log: "
                    + (Path(tmpdir) / "forge.log").read_text()
                )

            yield

            forge_proc.terminate()
            try:
                forge_proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                forge_proc.kill()

            forge_log.close()
            forge_log_content = (Path(tmpdir) / "forge.log").read_text()
            print(f"FORGE DAEMON LOGS:\n{forge_log_content}")
