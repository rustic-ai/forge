# HTTP API

Forge's control-plane server exposes a single Gin HTTP surface (`forge server`, default `--listen :9090`) that layers three route surfaces on one router: a public OpenAPI/contract REST surface, a `/rustic/*` compatibility surface, and a token-guarded `/manager/*` metastore surface. Alongside those it drives the distributed node registry, upgrades canonical WebSockets, and answers health/metrics probes.

This page is a map of that surface — what each route family is for, who calls it, and where to go for the details.

## Route families

| Prefix | Audience | Purpose |
|---|---|---|
| `/api/*`, `/catalog/*`, `/addons/*` | External clients, dashboards, automation | The public OpenAPI/contract surface — guild lifecycle, guild/agent files, agent registry, blueprint catalog, boards |
| `/rustic/*` | Local Rustic UI | Compatibility surface — the whole contract surface re-mounted under `/rustic`, plus WebSocket bootstrap + proxy-compat gateway, model-fit, observability, OAuth, and file URLs rewritten for the UI |
| `/manager/*` | Forge-python `GuildManagerAgent` | Metastore round-trips (guild/agent ensure, status, routing, heartbeat) used by the Python manager during bootstrap; optionally token-guarded |
| `/nodes/*` | Worker nodes (`forge client`) | Registration, heartbeat, deregistration, listing — feeds the `NodeRegistry` and `Scheduler` |
| `/ws/*` | External clients | Canonical WebSocket upgrade for usercomms/syscomms |
| `/healthz`, `/readyz`, `/metrics` | Operators, orchestrators | Liveness, readiness, Prometheus metrics |

!!! note "Feature-flagged surfaces"
    The public OpenAPI surface (`/api/*`, `/catalog/*`, canonical `/ws/*`, the contract routes) mounts only when `FORGE_ENABLE_PUBLIC_API` is truthy (default on); the `/rustic/*` compatibility surface mounts only when `FORGE_ENABLE_UI_API` is truthy (default on). In local mode (`FORGE_IDENTITY_MODE=local`, `FORGE_QUOTA_MODE=local`, both the default), the public surface also serves stub identity and quota routes under `/api/users`, `/api/organizations`, `/api/roles`, and `/api/quotas`.

!!! note "Two health surfaces, two processes"
    The API server (`forge server`, default `:9090`) serves `/healthz`, `/readyz`, and `/metrics` directly. The client's metrics server (`forge client` / in-process client via `--client-metrics-addr`, default `:9091`) is a separate process that exposes its own health/readiness/metrics — see [Health and readiness](#health-and-readiness) below.

## Guild lifecycle routes

These are the primary entry points for creating and managing guilds. See [Guilds](guilds/) for the full authoring and bootstrap story.

| Method | Route | Handler | Notes |
|---|---|---|---|
| `POST` | `/api/guilds` | `HandleCreateGuild` | Body is `CreateGuildRequest{spec, org_id}`; runs `guild.Bootstrap`, returns `201` with the guild `id`. Launch is asynchronous — the true progress/failure signal streams over syscomms as `InfraEvent`s. |
| `GET` | `/api/guilds/{id}` | `HandleGetGuild` | Returns the persisted, canonical spec plus `status` (round-tripped through `store.ToGuildSpec`); `404` if unknown. |
| `POST` | `/api/guilds/{id}/relaunch` | `HandleRelaunchGuild` | Records a row in `guilds_relaunch` and re-enqueues the `GuildManagerAgent` only if it isn't already running; returns `200` with `{"is_relaunching": <bool>}`, `400` if guild status is `stopped`/`stopping`, `404` if unknown. |

```bash
curl -X POST http://localhost:9090/api/guilds \
  -H 'Content-Type: application/json' \
  -d '{
    "org_id": "acme",
    "spec": {
      "id": "my-guild-01",
      "name": "My Guild",
      "description": "demo",
      "agents": [
        {"name": "Echo", "description": "echoes",
         "class_name": "rustic_ai.agents.EchoAgent"}
      ]
    }
  }'
```

### Guild and agent file routes

Guilds and their agents each get a scoped file namespace under `/api/*`:

- `POST` / `GET` / `DELETE /api/guilds/{id}/files/...`
- `POST` / `GET` / `DELETE /api/guilds/{id}/agents/{agent_id}/files/...`

The Rustic-compat gateway rewrites these into `/rustic/api/guilds/{guild}/files/...` URLs when it rewrites media/file references for the local UI (see [proxy shaping](#rustic-compat-and-the-gateway) below).

## Node registry routes

Every worker node started with `forge client` (or the in-process node from `forge server --with-client`) talks to the control plane exclusively through these routes. They are the sole HTTP surface between a node and the `NodeRegistry` / `Scheduler` described in [Distributed scheduling](distributed-scheduling/).

| Method | Route | Handler | Behavior |
|---|---|---|---|
| `POST` | `/nodes/register` | `RegisterNodeHandler` | Registers capacity; `201 Created`. Missing/empty `node_id` → `422`. |
| `POST` | `/nodes/{node_id}/heartbeat` | `NodeHeartbeatHandler` | Refreshes `LastHeartbeat`. Unknown node → `404`, which the client treats as a signal to re-register. |
| `DELETE` | `/nodes/{node_id}` | `NodeDeregisterHandler` | Removes the node; `204 No Content`. |
| `GET` | `/nodes` | `ListNodesHandler` | Returns the JSON array of currently healthy `NodeState` entries. |

```bash
curl -X POST http://localhost:9090/nodes/register \
  -H 'Content-Type: application/json' \
  -d '{
    "node_id": "worker-1",
    "capacity": {
      "cpus": 8,
      "memory": 16384,
      "gpus": 0
    }
  }'
```

!!! warning "Heartbeat cadence vs. eviction thresholds"
    Clients heartbeat every 5s. The registry marks a node unhealthy (invisible to the scheduler) after 10s of silence, but the reconciler doesn't evict and reassign its agents until 15s. A `404` from the heartbeat route means the registry has forgotten the node entirely and it must call `/nodes/register` again.

## Rustic compat and the gateway

The `/rustic/*` prefix serves the local Rustic UI. The entire public contract surface is re-mounted under `/rustic` (the same handlers, with `BaseURL: "/rustic"`), and on top of it the UI gets a set of compatibility extras that layer a UI-oriented wire shape on the same guild/messaging backend — see [Real-time gateway](gateway-websockets/) for the full picture.

- `GET /rustic/guilds/{guild_id}/ws?user=<name>` — bootstraps a short-lived (30-minute) `wsBootstrapSession`, returning `{wsId}`.
- `GET /rustic/ws/{ws_id}/usercomms` and `GET /rustic/ws/{ws_id}/syscomms` — proxy-compat WebSocket upgrades keyed to that session, using the proxy-compat handlers (renamed fields, rewritten file URLs, format-specific transforms).
- `GET`/`POST /rustic/api/guilds/{guild}/files/...` — guild file routes whose response URLs are rewritten (via `gateway.ProxyRewriteGuildFileURL` / `ProxyBaseOrigin`) into `/rustic`-prefixed, origin-qualified links for the UI.
- `GET /rustic/modelfit/*`, `GET /rustic/observe/...`, `GET /rustic/dependencies`, and the OAuth routes (see below) are all UI-only additions mounted under this prefix.

## Canonical WebSocket routes

Outside of the Rustic UI, external clients upgrade directly to canonical `protocol.Message` JSON — no proxy shaping, no bootstrap session:

| Method | Route | Socket kind |
|---|---|---|
| `GET` | `/ws/guilds/{id}/usercomms/{user_id}/{user_name}` | Conversational traffic |
| `GET` | `/ws/guilds/{id}/syscomms/{user_id}` | System/health/infra-event traffic |

Both kinds are guild-scoped: a socket opened against guild `g1` only ever sees messages published within `g1`. usercomms and syscomms are separate connections with separate subscription sets — open both for a full picture of a guild's activity. Details on topic subscriptions, message wrapping, and the proxy-compat wire shape live on the [Real-time gateway](gateway-websockets/) page.

!!! tip "No origin restriction at this layer"
    The WebSocket upgrader accepts connections from any origin (`CheckOrigin` always returns `true`). Any origin/auth control has to live upstream of the gateway.

## Manager routes

`/manager/*` is not meant for end users — it's the internal metastore channel the forge-python `GuildManagerAgent` uses to talk back to the Go control plane during its own bootstrap. It is a set of round-trips against the guild store, not a single call:

| Method | Route | Handler |
|---|---|---|
| `POST` | `/manager/guilds/ensure` | `HandleManagerEnsureGuild` |
| `GET` | `/manager/guilds/{guild_id}/spec` | `HandleManagerGetGuildSpec` |
| `PATCH` | `/manager/guilds/{guild_id}/status` | `HandleManagerUpdateGuildStatus` |
| `POST` | `/manager/guilds/{guild_id}/agents/ensure` | `HandleManagerEnsureAgent` |
| `PATCH` | `/manager/guilds/{guild_id}/agents/{agent_id}/status` | `HandleManagerUpdateAgentStatus` |
| `POST` | `/manager/guilds/{guild_id}/routes` | `HandleManagerAddRoutingRule` |
| `DELETE` | `/manager/guilds/{guild_id}/routes/{rule_hashid}` | `HandleManagerRemoveRoutingRule` |
| `POST` | `/manager/guilds/{guild_id}/lifecycle/heartbeat` | `HandleManagerProcessHeartbeat` |

`POST /manager/guilds/ensure` (body `EnsureGuildRequest{guild_spec, organization_id}`) is the secondary write path alongside `guild.Bootstrap`: if the guild already exists it flips status to `starting`, otherwise it normalizes agent IDs, applies the filesystem global root, and calls `store.FromGuildSpec` to persist the spec. Either way it replies with `EnsureGuildResponse{guild_spec, was_created, status}`. See the guild lifecycle notes in [Guilds](guilds/).

!!! note "Optional manager token"
    When `FORGE_MANAGER_API_TOKEN` is set, every `/manager/*` handler requires it — supplied as the `X-Forge-Manager-Token` header or as a `Bearer` token — and returns `401` otherwise. If the variable is unset, the routes are unauthenticated. The base URL the Python side uses is `FORGE_MANAGER_API_BASE_URL` (default derived from the bind address), configured via `--manager-api-base-url` and injected into each spawned agent's `AgentSpec.Properties.manager_api_base_url`.

## Model-fit and OAuth surfaces

Two feature areas mount their own routes on this same server; each has a dedicated page:

- **Model-fit** — `GET /rustic/modelfit/local-models` (ranked local model recommendations; query params `use_case`, `limit`, `runnable_only`) and `GET /rustic/modelfit/capabilities` (full `SystemProfile` hardware/runtime detection). See [Model fit](model-fit/).
- **OAuth** — mounted under the `/rustic` prefix (and only when providers are configured): `GET /rustic/oauth/organizations/{org_id}/providers`, `POST .../providers/{provider_id}/authorize`, `GET .../providers/{provider_id}/callback`, `GET .../providers/{provider_id}/status`, `DELETE .../providers/{provider_id}`. See [Secrets and OAuth](secrets-oauth/).

## Health and readiness

There are two independent health surfaces, one per process role:

- **API server** (`forge server`, `--listen`): serves `GET /healthz` (`{"status":"ok"}`), `GET /readyz` (`{"status":"ready"}`), and `GET /metrics` directly, plus `GET /ping`, `GET /__health`, `GET /openapi.json`, and `GET /openapi.sha256`. Use `/healthz` to confirm the control plane (and, in single-process mode, the embedded Redis/SQLite it depends on) is up.
- **Client metrics server** (`forge client` / in-process client, `--client-metrics-addr` / `--metrics-addr`, default `:9091`): a separate process that exposes its own health/readiness/metrics routes so orchestrators can distinguish "node process is alive" from "node is registered and ready to accept spawns."

```bash
# API server liveness
curl -sS http://127.0.0.1:3001/healthz

# Client metrics server readiness (default :9091, or your --client-metrics-addr)
curl -sS http://127.0.0.1:19091/readyz
```

!!! note "Readiness vs. registry health"
    `/readyz` on the client reflects the node process's own startup state. It is distinct from the control plane's view of node health (`NodeRegistry.IsHealthy`, driven by heartbeat recency) — a node can be `ready` locally while the registry has already marked it unhealthy due to a missed heartbeat window.

## Where routes are registered

All of these routes are wired onto a single Gin router in `api/server.go`'s `buildRouter`. The generated OpenAPI/contract routes are registered from `api/contract/gen.go` and dispatched through `api/contract_server.go`; the Rustic-compat additions come from `api/local_ui_api.go`, the manager metastore routes from `api/manager.go`, node registry handlers from `api/nodes.go`, and the model-fit, observability, and OAuth routes from `api/modelfit.go`, `api/observe.go`, and `api/oauth.go`. If you're adding a new route family, `buildRouter` is the layer to extend — each subsystem owns its handlers but they share this one HTTP entry point.

## Related pages

- [Guilds](guilds/) — the lifecycle behind `/api/guilds*` and `/manager/guilds/ensure`
- [Distributed scheduling](distributed-scheduling/) — what `/nodes/*` feeds into
- [Real-time gateway](gateway-websockets/) — usercomms/syscomms wire formats, topics, and proxy shaping
- [Model fit](model-fit/) — `/rustic/modelfit/*` in depth
- [Secrets and OAuth](secrets-oauth/) — `/oauth/*` in depth
