# Quickstart (Single-Process)

Run the whole Forge stack — HTTP API, message broker, metastore, and a compute node — as one process, and confirm it's healthy before you've finished your coffee.

## What you're starting

`forge server --with-client` boots four things inside a single OS process:

- **HTTP API** — the control-plane server (routes, health checks, `/manager/*`, `/rustic/*`)
- **Embedded miniredis** — an in-process Redis-compatible broker, auto-started when you don't pass `--redis`
- **SQLite metastore** — durable storage for guilds, agents, and placement state
- **In-process node** — a Forge client running in the same process, ready to accept agent spawn requests

There's no separate broker to install, no external database to provision, and no second terminal for a worker node. This is the mode LOCAL_DEBUG.md and the README use for local development, and it's the fastest path to a working guild on your laptop.

!!! note "Single-process vs. distributed"
    This page covers `--with-client`, where the server and the compute node share one process. Production and multi-node setups run `forge server` and one or more `forge client` daemons separately — see [Distributed Mode](../guides/distributed-deployment/) (once you're ready to scale beyond one machine).

## Prerequisites

- Go 1.25+ toolchain if you're building from source (`make build` in `forge-go/`)
- Python 3.13, with `uv`/`uvx` on `PATH` — agents run as spawned Python processes via `uvx`
- A local checkout of `forge-python` (or the path to wherever the package lives), since `FORGE_PYTHON_PKG` must point at it
- Free ports: **3001** (HTTP API in this example), **6379** (embedded Redis default), and **3000** (if you're following the LOCAL_DEBUG runbook through to the Rustic UI)

!!! warning "Port conflicts"
    Embedded Redis binds `127.0.0.1:6379` by default (`--embedded-redis-addr`). If you already have Redis running locally, stop it or override `--embedded-redis-addr` before starting Forge.

## Start the server

```bash
mkdir -p /tmp/forge-uv-cache /tmp/forge-xdg-cache /tmp/forge-xdg-data

FORGE_PYTHON_PKG=./forge-python \
FORGE_UV_CACHE_DIR=/tmp/forge-uv-cache \
UV_CACHE_DIR=/tmp/forge-uv-cache \
XDG_CACHE_HOME=/tmp/forge-xdg-cache \
XDG_DATA_HOME=/tmp/forge-xdg-data \
./bin/forge server \
  --listen :3001 \
  --db sqlite:////tmp/forge-local.db \
  --with-client \
  --client-node-id local-single-node \
  --client-metrics-addr 127.0.0.1:19091
```

**What each piece is doing:**

- `FORGE_PYTHON_PKG=./forge-python` — points the runtime at the Python execution bridge so spawned agents install from your local source, not a registry.
- `FORGE_UV_CACHE_DIR` / `UV_CACHE_DIR` / `XDG_CACHE_HOME` / `XDG_DATA_HOME` — writable cache directories for `uv`/`uvx`. Without these, agent spawn can fail with permission errors when the default cache locations aren't writable.
- `--listen :3001` — the HTTP API bind address.
- `--db sqlite:////tmp/forge-local.db` — the metastore DSN. Note the **four slashes**: two for the `sqlite://` scheme, plus the absolute path `/tmp/forge-local.db`. Three slashes gives you a relative path, which isn't what you want here.
- `--with-client` — starts the in-process node alongside the server.
- `--client-node-id local-single-node` — names the in-process node (defaults to the machine hostname if omitted).
- `--client-metrics-addr 127.0.0.1:19091` — the client's own health/metrics HTTP server address.

!!! note "These ports are overrides, not defaults"
    `--listen :3001` and `--client-metrics-addr 127.0.0.1:19091` are the values used in the README/LOCAL_DEBUG examples so they don't collide with anything else on your machine. The **compiled defaults** if you omit these flags are `--listen :9090` for the API and `:9091` for the client metrics server. Don't assume 3001/19091 apply unless you pass them explicitly.

## Verify it's healthy

The API server and the client metrics server each expose their own health endpoints. Hit the API server's:

```bash
curl -sS http://127.0.0.1:3001/healthz
# {"status":"ok"}

curl -sS http://127.0.0.1:3001/readyz
# {"status":"ready"}
```

`/healthz` confirms the process is alive; `/readyz` confirms it's actually ready to serve (the same endpoint distributed-mode tests poll before sending traffic). `GET /ping` and `GET /__health` are equivalent aliases for `/healthz` if you need them.

The in-process client's metrics server answers independently on its own address, and also serves Prometheus-format metrics:

```bash
curl -sS http://127.0.0.1:19091/healthz
curl -sS http://127.0.0.1:19091/readyz
curl -sS http://127.0.0.1:19091/metrics
```

If all four calls return without error, you have a fully wired single-process Forge instance: API up, broker up, metastore reachable, and a node registered and ready to accept agents.

## Troubleshooting

**Nothing on port 3001 / 6379 / 3000.** Something else is bound to a port Forge needs. Free it or pass `--listen`, `--embedded-redis-addr`, or the equivalent flag to move Forge off the conflicting port.

**Agent spawn fails with a permission error from `uv`.** Your `uv`/`uvx` cache or data directories aren't writable. Point `FORGE_UV_CACHE_DIR`, `UV_CACHE_DIR`, `XDG_CACHE_HOME`, and `XDG_DATA_HOME` at writable temp directories, as in the example above.

**`/readyz` never returns `ready`.** The SQLite DSN is usually the culprit — double-check you used four slashes (`sqlite:////tmp/forge-local.db`) for an absolute path, and that the parent directory exists and is writable.

## Next steps

- Load a catalog and register a blueprint against this running server — see the local runbook for the full end-to-end flow.
- Move to [Distributed Mode](../guides/distributed-deployment/) when you need more than one node.
- Point the Rustic UI at `http://127.0.0.1:3001` (or whatever `--listen` you chose) to drive guilds visually.
