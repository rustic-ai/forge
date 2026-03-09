# Forge

Forge is the Rustic AI runtime stack for running guilds with:

- `forge-go`: Go control plane/runtime (API, scheduler, supervisors, storage, distributed node support)
- `forge-python`: Python execution bridge and system agents (including `GuildManagerAgent`)

## Repository and Module

- Git repository: `git@github.com:rustic-ai/forge.git`
- Go module root: `github.com/rustic-ai/forge/forge-go`
- Go module file: `forge-go/go.mod`

If you import Forge Go packages, use:

```go
import "github.com/rustic-ai/forge/forge-go/<package>"
```

## Prerequisites

- Go `1.25+`
- Python `3.13`
- Poetry
- `uvx` available on PATH
- Docker (required for some integration/e2e scenarios)

## Build and Test

```bash
export FORGE_REPO_DIR="/absolute/path/to/forge"

cd "$FORGE_REPO_DIR/forge-go"
make build
make test
make lint
```

Binary output:

- `"$FORGE_REPO_DIR/forge-go/bin/forge"`

## Quick Start (Single Process)

This starts everything in one Forge process:

- HTTP API server
- Embedded Redis (`127.0.0.1:6379`, default when `--redis` is omitted)
- In-process Forge client/node (`--with-client`)
- SQLite metastore

```bash
export FORGE_REPO_DIR="/absolute/path/to/forge"

cd "$FORGE_REPO_DIR/forge-go"
make build

mkdir -p /tmp/forge-uv-cache /tmp/forge-xdg-cache /tmp/forge-xdg-data

FORGE_PYTHON_PKG="$FORGE_REPO_DIR/forge-python" \
FORGE_UV_CACHE_DIR=/tmp/forge-uv-cache \
UV_CACHE_DIR=/tmp/forge-uv-cache \
XDG_CACHE_HOME=/tmp/forge-xdg-cache \
XDG_DATA_HOME=/tmp/forge-xdg-data \
"$FORGE_REPO_DIR/forge-go/bin/forge" server \
  --listen :3001 \
  --db sqlite:////tmp/forge-local.db \
  --with-client \
  --client-node-id local-single-node \
  --client-metrics-addr 127.0.0.1:19091
```

Health check:

```bash
curl -sS http://127.0.0.1:3001/healthz
```

## Distributed Mode (Server + External Client)

Start server:

```bash
export FORGE_REPO_DIR="/absolute/path/to/forge"

cd "$FORGE_REPO_DIR/forge-go"
"$FORGE_REPO_DIR/forge-go/bin/forge" server --listen :3001 --db sqlite:////tmp/forge-server.db
```

Start a separate client node:

```bash
export FORGE_REPO_DIR="/absolute/path/to/forge"

cd "$FORGE_REPO_DIR/forge-go"
FORGE_PYTHON_PKG="$FORGE_REPO_DIR/forge-python" \
"$FORGE_REPO_DIR/forge-go/bin/forge" client --server http://127.0.0.1:3001 --redis 127.0.0.1:6379
```

## Python Package (`forge-python`)

Install and run tests:

```bash
export FORGE_REPO_DIR="/absolute/path/to/forge"

cd "$FORGE_REPO_DIR/forge-python"
poetry install
poetry run pytest
```

Contract-only tests:

```bash
poetry run pytest tests/contract/
```

## Rustic AI Integration Dependencies

Some integration/parity flows depend on the canonical `rustic-ai` repository.
Use the actual repository, not a relative sibling path:

- `https://github.com/rustic-ai/rustic-ai`

Example:

```bash
git clone https://github.com/rustic-ai/rustic-ai.git /absolute/path/to/rustic-ai
export RUSTIC_AI_CORE="/absolute/path/to/rustic-ai/core"
```

## APIs

Forge exposes:

- Public OpenAPI/API surface
- Rustic compatibility surface under `/rustic/*`
- Internal manager metastore surface under `/manager/*`

## Local Debug Runbook

Detailed end-to-end local debug steps are in:

- `LOCAL_DEBUG.md`

## License

Apache License 2.0. See `LICENSE`.
