# Troubleshooting

Most Forge failures fall into a handful of known shapes: a broken agent-spawn sandbox, a control-plane request that never seems to run, a node flapping between healthy and evicted, a telemetry endpoint returning an unexpected status, or a storage backend rejecting writes. This page walks through each symptom, its root cause in the runtime, and the fix.

## uv/permission failures on agent spawn

**Symptom:** Agent processes fail to start, with errors from `uv`/`uvx` about permission denied, cache directory, or being unable to write to a `.cache`/`.local` path — usually inside a container, CI runner, or a locked-down `$HOME`.

**Root cause:** Agents run via `uvx`, and `uvx`/`uv` need a writable cache directory and a writable XDG data/cache tree. If `HOME` or the default XDG paths aren't writable (common in containers or sandboxed CI), the spawn fails before the agent process ever starts.

**Fix:** Point Forge's uv/XDG environment at writable directories before starting the server or client.

```bash
mkdir -p /tmp/forge-uv-cache /tmp/forge-xdg-cache /tmp/forge-xdg-data

FORGE_PYTHON_PKG=./forge-python \
FORGE_UV_CACHE_DIR=/tmp/forge-uv-cache \
UV_CACHE_DIR=/tmp/forge-uv-cache \
XDG_CACHE_HOME=/tmp/forge-xdg-cache \
XDG_DATA_HOME=/tmp/forge-xdg-data \
./bin/forge server --with-client
```

This is exactly the pattern Forge's own hermetic test harness uses: `e2e/main_test.go` and `testutil/e2e/main_test.go` create a temp base directory and set `HOME`, `XDG_CACHE_HOME`, `XDG_DATA_HOME`, `TMPDIR`, `FORGE_UV_CACHE_DIR`, and `UV_CACHE_DIR` into it before running anything.

!!! tip
    Set all four variables together (`FORGE_UV_CACHE_DIR`, `UV_CACHE_DIR`, `XDG_CACHE_HOME`, `XDG_DATA_HOME`). Setting only one is a common half-fix — `uv` and its transitive tooling read different combinations of them depending on version.

## Guild returns 201 but nothing runs

**Symptom:** A spawn request (or guild launch) gets a `201`/accepted response, but the agent never appears to run — no process, no logs, no state change.

**Root cause:** Spawn dispatch is asynchronous by design. The server's `ControlQueueListener.OnSpawn` does not place the agent synchronously: it idempotency-gates on `IsActivelyTracked`, calls `MarkAccepted` with the serialized payload, and immediately acks the caller with "spawn request accepted." Placement (`Scheduler.Schedule`), `MarkDispatched`, and the push to `forge:control:node:<nodeID>` all happen afterward in a background goroutine (`dispatchAcceptedSpawn`). A `201` only means the request was durably queued — not that an agent process exists yet.

**Fix:**

1. **Watch InfraEvents on syscomms**, not just the HTTP response. Spawn progress is signaled through infrastructure events as the placement moves through its state machine (`accepted -> dispatched -> acknowledged -> running`), not through the original request/response cycle.
2. **Check node health.** If there are no healthy nodes, or none with sufficient capacity, `Scheduler.Schedule` fails with `no healthy nodes available in the cluster` or `no node with sufficient capacity [...]`, and the placement stays in `accepted` — retried by the reconciler, not resolved.
3. **Confirm the client is actually registered and heartbeating.** An agent can only reach `dispatched` once a node is in `ListHealthy()`.

```bash
curl -sS http://127.0.0.1:3001/nodes | jq .
```

If the response is an empty array, there is no healthy node to schedule onto — see the next section.

!!! note
    This two-phase accept-then-dispatch design exists so the caller gets a fast, durable ack even under scheduler contention. If dispatch fails (bad scheduling, push failure), the placement reverts to `accepted` with an incremented attempt count, and the [Reconciler](../concepts/placement-reconciliation/) retries it later rather than failing the original request.

## Node evicted / re-registering

**Symptom:** A worker node repeatedly disappears from `/nodes`, gets marked dead, and re-registers — sometimes looping.

**Root cause:** Two independent health thresholds exist, and they don't match:

| Threshold | Value | Effect |
|---|---|---|
| `IsHealthy` / `ListHealthy` (registry) | `time.Since(LastHeartbeat) < 10s` | Node stops being eligible for new scheduling |
| `DeadNodeTimeout` (reconciler) | `> 15s` | Node is deregistered and its agents are re-enqueued |

Between 10s and 15s of heartbeat silence, a node is invisible to the scheduler but **not yet evicted** — a narrow window that can look like flapping under load or GC pauses. Once a node crosses 15s, the reconciler's `reconcileDeadNodes` phase deregisters it and re-pushes every orphaned agent placement back onto the global queue `forge:control:requests`, exactly like a fresh spawn.

Separately, each client heartbeats to `POST /nodes/{node_id}/heartbeat`. If the server has already deregistered the node (it crossed 15s, or the server restarted and lost in-memory registry state), that heartbeat comes back `404`, and the client's own logic re-registers via `POST /nodes/register`.

**Fix:**

- If nodes flap around the 10-15s boundary, check for heartbeat delivery latency (network jitter, GC pauses, CPU starvation on the client) rather than assuming the node itself is unhealthy.
- A `404` on heartbeat followed by re-registration is *expected recovery behavior*, not a bug — it's how the client responds when the registry has evicted it. Frequent re-registration cycles indicate the heartbeat interval is too close to the 10s/15s thresholds for your network conditions.
- Remember placement state (`GlobalPlacementMap`) is in-memory only. A control-plane restart drops all accepted/dispatched tracking; recovery then relies on the `AgentStatusStore` (Redis/NATS, TTL'd) and the idempotency gates, not on replayed placement history.

```bash
# Is the node currently visible/healthy to the scheduler?
curl -sS http://127.0.0.1:3001/nodes | jq '.[] | {node_id, last_heartbeat}'
```

See [Scheduler & Reconciler](../concepts/placement-reconciliation/) for the full reconciliation phase order.

## Missing FORGE_PYTHON_PKG, uvx not on PATH, or port conflicts

**Symptom:** Server/client starts but every agent spawn fails immediately, or the server refuses to bind at all.

**Root cause and fix:**

- **`FORGE_PYTHON_PKG` unset or wrong.** Every agent spawns as a Python process launched by `uvx`, and `FORGE_PYTHON_PKG` must point at the `forge-python` package so the runtime installs from source. Without it, spawns fail before any agent code runs.

    ```bash
    export FORGE_PYTHON_PKG=/absolute/path/to/forge-python
    ```

- **`uvx` not on PATH.** Prerequisites are Go 1.25+, Python 3.13, and `uv`/`uvx` on PATH (Docker is also required for some integration/e2e paths). Confirm with:

    ```bash
    command -v uvx || echo "uvx not found on PATH"
    ```

    If missing, install `uv` (which provides `uvx`), or rely on the registry's bootstrap path — `registry` resolves `uvx` in order: bundled binary next to `forge` -> PATH -> `~/.forge/bin` -> `FORGE_UVX_PATH`, downloading `astral-sh/uv` as a last resort.

- **Port conflicts on 3001 / 6379 / 3000.** The local single-process runbook binds the server to `:3001`, the embedded Redis (miniredis) to `127.0.0.1:6379` by default, and expects the Rustic UI container on `3000`. Free these ports first:

    ```bash
    lsof -iTCP:3001 -sTCP:LISTEN
    lsof -iTCP:6379 -sTCP:LISTEN
    lsof -iTCP:3000 -sTCP:LISTEN
    ```

    If 6379 is already in use by another Redis, pass `--embedded-redis-addr` to bind elsewhere, or point `--redis` at the existing instance instead of using the embedded default.

## Telemetry: /rustic/observe returns 501, external_otlp needs an endpoint

**Symptom 1:** `GET /rustic/observe/guilds/:guild_id/messages/:msg_id/spans` returns `501 Not Implemented`.

**Root cause:** That endpoint reads spans out of the local sqlite-otel sidecar database. It only works when telemetry mode is `desktop_sqlite`. Any other mode (including `external_otlp`, or telemetry disabled entirely) returns 501 with `"observability spans query is only available with desktop sqlite telemetry"`.

**Fix:** Enable telemetry in desktop mode if you need this endpoint:

```bash
forge server --otel-enabled \
  --otel-mode desktop_sqlite \
  --otel-sqlite-binary /usr/local/bin/sqlite-otel \
  --otel-sqlite-port 4318
# spans land in <forge-home>/telemetry/sqlite-otel.db
```

If you're running `external_otlp`, query your own OTLP backend/collector instead — Forge does not proxy span queries for external endpoints.

**Symptom 2:** Server fails to start (or telemetry silently doesn't export) with a config error mentioning an OTLP endpoint.

**Root cause:** `external_otlp` mode requires `--otel-endpoint`; `desktop_sqlite` mode requires `--otel-sqlite-binary`. Config validation is strict — missing either produces `"requires an OTLP endpoint URL"` or `"requires a sqlite-otel binary path"`, and the endpoint must parse as a valid `http`/`https` URL with a host.

**Fix:**

```bash
forge server --otel-enabled \
  --otel-mode external_otlp \
  --otel-endpoint http://otel-collector:4318 \
  --otel-service-name forge-server
```

Check the client and server metrics endpoints are actually up:

```bash
curl -sS http://127.0.0.1:9090/metrics   | head -5   # main API server
curl -sS http://127.0.0.1:9091/healthz               # client metrics server
curl -sS http://127.0.0.1:9091/readyz
```

See [Telemetry](../features/telemetry/) for the full metric catalog and export modes.

## Storage failures

### SQLite "invalid cross-device link"

**Symptom:** File uploads fail with an `invalid cross-device link` error, typically when the data directory and the system temp directory are on different mounts (common in containers with a separate `/tmp` tmpfs).

**Root cause:** `gocloud.dev/blob`'s fileblob implementation stages writes via temp files before renaming them into place. If those temp files land on a different filesystem/mount than the target bucket directory, the final `rename` fails with a cross-device link error.

**Fix:** Forge already applies this fix internally — the local file bucket URL is constructed with `no_tmp_dir=1` (`resolveFileScope` in `filesystem/resolver.go`), which forces temp files to be created inside the bucket directory itself rather than `os.TempDir()`. If you're still hitting this, confirm your `--data-dir` isn't pointed at a read-only or unusual mount, and that the resolved workspace path (`<data-dir>/workspaces`) is writable end-to-end.

### SQLite single-connection contention

**Symptom:** Slow or serialized writes under load; requests queueing up against the metastore even though the machine has spare CPU.

**Root cause:** This is by design, not a bug. The SQLite metastore is deliberately pinned to a single connection (`SetMaxOpenConns(1)`, `SetMaxIdleConns(1)`, `SetConnMaxLifetime(0)`) to avoid parallel-writer contention on the embedded desktop database. `PRAGMA busy_timeout=5000` is always set, and for real file-backed DSNs, `PRAGMA journal_mode=WAL` and `PRAGMA synchronous=NORMAL` are also applied. This tuning targets an embedded, single-writer desktop deployment — not high-concurrency multi-writer workloads.

**Fix:** If you're seeing metastore contention under real load, that's the signal to move to Postgres rather than tune SQLite further.

### Postgres schema parity

**Symptom:** A Postgres-backed metastore has unexpected table/column names, or a mixed Go/Python deployment sees schema mismatches.

**Root cause:** The Go metastore (`guild/store`, driver `postgres` via `gorm.io/driver/postgres`) and the Python side (`rustic-ai` SQLModel) must share one Postgres schema in mixed deployments. On Postgres, Forge runs `runSchemaParityMigrations` after `AutoMigrate` specifically to reconcile GORM's naming with the Python schema — renaming tables (e.g. `blueprint_shared_with_organization` -> `blueprintsharedwithorganization`, `blueprint_review` -> `blueprint_reviews`), renaming review columns, forcing a composite primary key `(id, guild_id)` on `agents`, and dropping Go-only legacy columns. These migrations only run when `db.Name() == "postgres"` — they do not apply to SQLite.

**Fix:** Point `--db` at your Postgres instance using a supported DSN form (`postgres://`, `postgresql://`, `postgresql+psycopg://`, `postgresql+psycopg2://` — the last two are normalized to plain `postgres://`), and let Forge's migrations run on startup. Don't hand-migrate the schema outside of Forge; if you're maintaining a shared Postgres database with `forge-python`, upgrade both sides together so the parity migrations stay in sync with the Python SQLModel schema.

```bash
forge server --db postgres://forge:forge@localhost:5432/forge
```

## Quick health/readiness checks

Use these as a first diagnostic pass whenever something "isn't running":

```bash
# Main API server
curl -sS http://127.0.0.1:3001/healthz   # -> {"status":"ok"}
curl -sS http://127.0.0.1:3001/readyz    # -> {"status":"ready"}

# Client / in-process node metrics server
curl -sS http://127.0.0.1:19091/healthz
curl -sS http://127.0.0.1:19091/readyz
curl -sS http://127.0.0.1:19091/metrics | head

# Node registry (scheduler view of the cluster)
curl -sS http://127.0.0.1:3001/nodes | jq .
```

!!! warning
    `/healthz` returning `ok` only means the HTTP server is up. It does not mean Redis/NATS is reachable, the metastore is writable, or any node is registered. Always cross-check `/readyz` and `/nodes` before concluding a "stuck" deployment is actually healthy.

## Related pages

- [Getting Started: Quickstart](../getting-started/quickstart/)
- [Scheduler & Reconciler](../concepts/placement-reconciliation/)
- [Telemetry](../features/telemetry/)
- [Storage](../features/storage/)
