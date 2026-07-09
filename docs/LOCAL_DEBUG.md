# Local Debug Guide (Single Forge Binary)

This runbook is the current local debug flow for the `forge` repo using a single Forge process:

1. `forge server` (API + manager endpoints)
2. embedded miniredis (no external Redis container)
3. in-process Forge client/node (`--with-client`)
4. SQLite metastore

## 1. Prerequisites

1. `go`, `uvx`, `jq`, `curl`, `docker` installed.
2. No process already bound to ports `3001`, `6379`, or `3000`.

## 2. Build the New Binary

```bash
cd forge-go
make build
```

Binary path:
`forge-go/bin/forge`

## 3. Start Forge in Single-Process Mode

```bash
cd forge-go

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

Notes:
1. `FORGE_PYTHON_PKG` must point to local `forge-python`.
2. Embedded Redis starts automatically on `127.0.0.1:6379` when `--redis` is not set.
3. The cache env vars above avoid local uv permission issues.

Health check:

```bash
curl -sS http://127.0.0.1:3001/healthz
```

## 4. Load Catalog Data

```bash
cd atelier

RUSTIC_AI_HOST=http://127.0.0.1:3001 ./scripts/load_agent_data.sh
RUSTIC_AI_HOST=http://127.0.0.1:3001 DATA_FOLDER=./data ./scripts/load_data.sh
```

## 5. Register `echo_app.json`

Use the canonical file in project root and set org visibility for local UI:

```bash

jq '.organization_id="acmeorganizationid"' echo_app.json > /tmp/echo_app_local.json

curl -sS -X POST http://127.0.0.1:3001/catalog/blueprints/ \
  -H 'content-type: application/json' \
  --data-binary @/tmp/echo_app_local.json
```

Verify it is UI-accessible:

```bash
curl -sS http://127.0.0.1:3001/rustic/catalog/users/dummyuserid/blueprints/accessible/ | jq -r '.[].name'
```

Expected includes:
1. `Simple Echo`
2. `Researcher`

## 6. Start Rustic UI Docker Image

```bash
docker rm -f rustic-ui-local >/dev/null 2>&1 || true

docker run -d \
  --name rustic-ui-local \
  -p 3000:3000 \
  -e RUSTIC_API_BASEPATH=http://localhost:3001/rustic \
  -e API_BASEPATH=http://localhost:3001/ \
  -e AUTO_CREATE_ORG=false \
  -e SUPPORT_UPLOAD=true \
  us-west1-docker.pkg.dev/rustic-ai-v1/dragonscale-ai/rustic-ui:latest
```

Open:
`http://127.0.0.1:3000/apps`

## 7. Smoke Test (Simple Echo)

1. Open **Simple Echo** in App Store.
2. Launch app with any guild name.
3. Send: `hello from playwright` (or any message).
4. Expect bot reply to echo exact message text.

## 8. Quick Troubleshooting

1. App stuck in loading:
Check Forge logs for manager/agent spawn errors.
2. App missing in UI:
Ensure blueprint exposure is `public` and `organization_id` is `acmeorganizationid`.
3. Manager spawn fails with uv permission errors:
Set `FORGE_UV_CACHE_DIR`, `UV_CACHE_DIR`, `XDG_CACHE_HOME`, and `XDG_DATA_HOME` as in section 3.
4. Confirm runtime agent state:

```bash
redis-cli -p 6379 KEYS 'forge:agent:status:*'
```
