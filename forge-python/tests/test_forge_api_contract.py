import subprocess
import tempfile
import time
from pathlib import Path
from typing import Generator

import pytest
import requests
from redis import Redis

def _find_repo_root() -> Path:
    cur = Path(__file__).resolve()
    for candidate in [cur] + list(cur.parents):
        if (candidate / "forge-go").is_dir() and (candidate / "forge-python").is_dir():
            return candidate
        nested = candidate / "rustic-go"
        if (nested / "forge-go").is_dir() and (nested / "forge-python").is_dir():
            return nested
    raise RuntimeError("unable to locate repo root containing forge-go and forge-python")


repo_root = _find_repo_root()


@pytest.fixture(scope="module")
def redis_server() -> Generator[int, None, None]:
    # Start a local redis-server
    port = 26380  # use different port to avoid clashes
    import shutil

    redis_bin = shutil.which("redis-server")
    if not redis_bin:
        pytest.skip("redis-server not found in PATH")

    proc = subprocess.Popen(
        [redis_bin, "--port", str(port)],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )

    # Wait for redis
    r = Redis(host="localhost", port=port)
    for _ in range(20):
        try:
            if r.ping():
                r.flushall()
                break
        except Exception:
            time.sleep(0.5)
    else:
        proc.kill()
        pytest.fail("Could not start redis-server")

    yield port

    proc.terminate()
    proc.wait(timeout=5)


@pytest.fixture(scope="module")
def go_server(redis_server) -> Generator[str, None, None]:
    forge_bin = repo_root / "forge-go" / "forge"

    # Always compile forge to ensure latest code is used
    subprocess.run(
        ["go", "build", "-o", str(forge_bin), "main.go"],
        cwd=str(repo_root / "forge-go"),
        check=True,
    )

    with tempfile.TemporaryDirectory() as tmpdir:
        db_path = Path(tmpdir) / "forge_server.db"
        port = 9091

        # Minimal dependency config to enable guild-scoped file endpoints.
        dep_path = Path(tmpdir) / "deps.yaml"
        dep_path.write_text(
            f"""
filesystem:
  class_name: rustic_ai.core.guild.agent_ext.depends.filesystem.filesystem.FileSystemResolver
  properties:
    path_base: {tmpdir}
    protocol: file
    storage_options:
      auto_mkdir: true
"""
        )

        forge_log = open(Path(tmpdir) / "forge_server.log", "w")
        forge_proc = subprocess.Popen(
            [
                str(forge_bin),
                "server",
                "--db",
                f"sqlite://{db_path}",
                "--redis",
                f"localhost:{redis_server}",
                "--listen",
                f":{port}",
                "--dependency-config",
                str(dep_path),
            ],
            stdout=forge_log,
            stderr=forge_log,
        )

        server_url = f"http://localhost:{port}"

        # Wait for the Go server to start by hitting /ping
        for _ in range(20):
            try:
                resp = requests.get(f"{server_url}/ping")
                if resp.status_code == 200:
                    break
            except Exception:
                time.sleep(0.5)
        else:
            forge_proc.kill()
            pytest.fail(
                "Could not start Go server. Log: "
                + (Path(tmpdir) / "forge_server.log").read_text()
            )

        yield server_url

        forge_proc.terminate()
        try:
            forge_proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            forge_proc.kill()
        forge_log.close()


def test_rest_api_contract_create_guild(go_server):
    """
    Test 8.4.4 & 8.4.7:
    Hits Go Server, creates a guild, ensures the Go Server pushes the correct
    GuildManagerAgent bootstrap spawn request to Redis.
    """
    payload = {
        "org_id": "org-contract-1",
        "spec": {
            "id": "my-guild",
            "name": "Contract Guild",
            "description": "testing 123",
            "agents": [
                {"id": "a-1", "name": "First Agent", "class_name": "test.Agent1"}
            ],
            "properties": {},
        },
    }

    # 1. API creates guild
    resp = requests.post(f"{go_server}/api/guilds", json=payload)
    assert resp.status_code == 201

    data = resp.json()
    assert "id" in data
    guild_id = data["id"]

    # 2. Get guild
    resp_get = requests.get(f"{go_server}/api/guilds/{guild_id}")
    assert resp_get.status_code == 200
    get_data = resp_get.json()
    assert get_data["id"] == guild_id
    assert get_data["name"] == "Contract Guild"
    assert get_data["status"] == "requested"

    assert get_data["description"] == "testing 123"
    assert len(get_data["agents"]) == 1
    assert get_data["agents"][0]["id"] == "a-1"
    assert get_data["agents"][0]["name"] == "First Agent"
    assert get_data["agents"][0]["class_name"] == "test.Agent1"


def test_rest_api_filesystem_contract(go_server):
    """
    Test File System Endpoints
    """
    import io

    # 1. Upload File
    files = {
        "file": ("test_file.txt", io.BytesIO(b"Hello Integration Tests!"), "text/plain")
    }

    # To use a known guild_id, we create one first
    payload = {
        "org_id": "org-contract-1",
        "spec": {
            "id": "file-guild",
            "name": "File Guild",
            "agents": [
                {
                    "id": "echo-1",
                    "name": "Echo Agent",
                    "class_name": "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
                }
            ],
        },
    }
    resp = requests.post(f"{go_server}/api/guilds", json=payload)
    assert resp.status_code == 201
    guild_id = resp.json()["id"]

    resp_up = requests.post(f"{go_server}/api/guilds/{guild_id}/files/", files=files)
    assert resp_up.status_code == 200
    upload_data = resp_up.json()
    assert upload_data["guild_id"] == guild_id
    assert upload_data["filename"] == "test_file.txt"
    assert upload_data["content_length"] == len("Hello Integration Tests!")

    # 2. List Files
    resp_list = requests.get(f"{go_server}/api/guilds/{guild_id}/files/")
    assert resp_list.status_code == 200
    file_list = resp_list.json()
    assert len(file_list) == 1
    assert file_list[0]["name"] == "test_file.txt"
    assert file_list[0]["mimetype"] == "text/plain"
    assert file_list[0]["on_filesystem"] is True

    # 3. Download File
    resp_dl = requests.get(f"{go_server}/api/guilds/{guild_id}/files/test_file.txt")
    assert resp_dl.status_code == 200
    assert resp_dl.content == b"Hello Integration Tests!"

    # 4. Delete File
    resp_del = requests.delete(f"{go_server}/api/guilds/{guild_id}/files/test_file.txt")
    assert resp_del.status_code == 204

    resp_list2 = requests.get(f"{go_server}/api/guilds/{guild_id}/files/")
    assert resp_list2.status_code == 200
    assert len(resp_list2.json()) == 0
