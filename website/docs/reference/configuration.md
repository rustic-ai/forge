# Configuration & Environment

Every path Forge writes to, every config file it loads, and every environment variable it reads resolves back to one root: the Forge home directory. This page is the canonical reference for that tree, the YAML config files under it, and the full set of environment variables the server, client, and spawned agent processes consume.

## Forge home resolution

`forgepath` (package `github.com/rustic-ai/forge/forge-go/forgepath`) centralizes every home/config-path convention Forge uses. `forgepath.ForgeHome()` resolves the root directory with this precedence, evaluated top to bottom:

1. **`--forge-home` CLI flag** — a persistent flag on the root command, applied via `forgepath.SetHome(path)`.
2. **`FORGE_HOME` environment variable.**
3. **`~/.forge`** — the user's home directory joined with `.forge`. If the user's home directory can't be determined, Forge falls back to `os.TempDir()/.forge`.

```go
switch {
case override != "":            // set by SetHome(--forge-home)
    cached = expandHome(override)
case os.Getenv("FORGE_HOME") != "":
    cached = expandHome(os.Getenv("FORGE_HOME"))
default:
    home, _ := os.UserHomeDir()
    if home != "" {
        cached = filepath.Join(home, ".forge")
    } else {
        cached = filepath.Join(os.TempDir(), ".forge")
    }
}
```

The resolved value is cached behind a `sync.Mutex`; calling `SetHome` clears the cache so a later `ForgeHome()` call re-resolves. `expandHome` handles `~` and `~/`-prefixed paths anywhere a path-like value is accepted (DSNs, data dirs, secrets paths).

Everything else under this page is expressed relative to that root via `forgepath.Resolve(sub)`, which is just `filepath.Join(ForgeHome(), sub)`:

```go
forgepath.Resolve("data/forge.db")        // -> <forge-home>/data/forge.db
forgepath.Resolve("data")                 // -> <forge-home>/data
forgepath.Resolve("nats")                 // -> <forge-home>/nats (embedded NATS JetStream store)
```

!!! tip "Precedence in practice"
    `--forge-home` always wins, even if `FORGE_HOME` is exported in your shell. This lets you run multiple isolated Forge instances (e.g. in tests or side-by-side dev servers) from one shell session without unsetting environment variables.

### Config path helpers

Beyond the home directory itself, `forgepath` exposes helpers for the default location of every YAML config file and the OS keychain service name:

| Function | Default value |
|---|---|
| `DependencyConfigPath()` | `conf/agent-dependencies.yaml` (relative to CWD, overridable via `FORGE_DEPENDENCY_CONFIG`) |
| `LocalModelCatalogPath()` | `conf/local-model-catalog.yaml` (overridable via `FORGE_LOCAL_MODEL_CATALOG`) |
| `OAuthProvidersConfigPath()` | `conf/oauth-providers.yaml` (overridable via `FORGE_OAUTH_PROVIDERS_CONFIG`) |
| `KeychainService()` | `forge` (overridable via `FORGE_KEYCHAIN_SERVICE`) |

## Config files

Forge reads four YAML config files. None of them are stored under `~/.forge` by default — they're resolved relative to the working directory under `conf/`, and each has an env var to point at an alternate path.

### `agent-dependencies.yaml`

Declares, per dependency class, what runtime resources an agent needs and how they're satisfied — this is the source that `filesystem.DependencyConfig` (path base, protocol, storage options) is built from per guild. Loaded from `FORGE_DEPENDENCY_CONFIG` (default `conf/agent-dependencies.yaml`).

### `local-model-catalog.yaml`

Describes locally runnable model entries used by the model-fit/local-model subsystem (which local LLM binaries and weights are available, and what hardware they need). Loaded from `FORGE_LOCAL_MODEL_CATALOG` (default `conf/local-model-catalog.yaml`).

### `oauth-providers.yaml`

Declares OAuth providers available for agent delegated-auth flows. Each entry under `providers.<id>` supports `display_name`, `description`, `auth_url`, `token_url`, `scopes`, `redirect_url`, and `use_pkce` (defaults to `true`). `auth_url`/`token_url`/`redirect_url` support `${ENV}` interpolation.

```yaml
providers:
  github:
    display_name: GitHub
    description: Connect your GitHub account
    auth_url: ${GITHUB_AUTH_URL}
    token_url: https://github.com/login/oauth/access_token
    scopes: [repo, read:user]
    redirect_url: ""
    use_pkce: true
```

Client ID/secret are **not** stored in this file — they're supplied per-request by the caller when starting an authorize flow. If a provider is omitted from this file entirely, six well-known providers (`github`, `google`, `google-drive`, `slack`, `microsoft`, `notion`) still resolve `auth_url`/`token_url` from hardcoded known endpoints. Loaded from `FORGE_OAUTH_PROVIDERS_CONFIG` (default `conf/oauth-providers.yaml`). See [Secrets & OAuth](../features/secrets-oauth/) for the full flow.

### `forge-agent-registry.yaml`

The agent registry: per-agent declarations of secret needs (`SecretNeed{Key, Label, Optional}`) and OAuth needs (`OAuthNeed{Provider, Label, Scopes, Optional}`), consumed by `helper/envvars.BuildAgentEnv` when constructing an agent's process environment. Overridden via `FORGE_AGENT_REGISTRY`.

!!! note "These files are not under `~/.forge`"
    Unlike the SQLite metastore and workspace files, config YAML is resolved relative to the process's working directory by default (`conf/...`), not `forgepath.ForgeHome()`. Set the corresponding `FORGE_*_CONFIG` / `FORGE_AGENT_REGISTRY` env var to point at an absolute path when running `forge` from outside the repo.

## Default local layout

A freshly initialized Forge home, using every default (no `--db`, `--data-dir`, or `FORGE_HOME` override), looks like this:

```text
~/.forge/                         # ForgeHome() default
  data/
    forge.db                      # SQLite metastore (default --db)
    workspaces/                   # filesystem base (FORGE_FILESYSTEM_GLOBAL_ROOT)
      <orgID>/<guildID>/GUILD_GLOBAL/<file>       # guild-wide files
      <orgID>/<guildID>/GUILD_GLOBAL/.<file>.meta # JSON sidecar
      <orgID>/<guildID>/<agentID>/<file>          # agent-scoped files
  nats/                            # embedded NATS JetStream store (only if --backend nats)
  secrets/
    .env                          # DotEnvSecretProvider default path
    <key>                         # FileSecretProvider: one file per secret key
```

- `data/forge.db` is the default SQLite DSN: `sqlite://<forge-home>/data/forge.db`. Override with `--db`.
- `data/workspaces/` is the root the blob-backed `filesystem` package writes into for local (non-S3/GCS) guilds. Override the parent with `--data-dir` (files still land under `<data-dir>/workspaces/`).
- `nats/` holds the embedded JetStream store directory when the messaging backend is NATS and no external `--nats` URL is given.
- `secrets/.env` and `secrets/<key>` back the `dotenv` and `file` secret providers respectively — see [Secrets & OAuth](../features/secrets-oauth/).

Object paths inside `workspaces/` are always `path.Join(orgID, guildID, agentID)`. An empty `agentID` becomes the sentinel `GUILD_GLOBAL` (guild-wide files); an empty `orgID` falls back to `guildID` so single-tenant setups still get a valid path. Every stored file has a JSON sidecar `.{filename}.meta` holding `content_length`, `content_type`, `uploaded_at` (RFC3339Nano UTC), and user metadata.

!!! warning "Don't point `--data-dir` or a DSN at `store/`"
    The top-level `store/` Go package is an empty placeholder — it is **not** where the metastore lives. The real GORM-backed metastore implementation is `guild/store` (import path `github.com/rustic-ai/forge/forge-go/guild/store`). This only matters if you're reading source, not runtime config, but it's a common point of confusion when tracing where `--db` data actually goes.

## Environment variables

### Storage

| Variable | Default | Purpose |
|---|---|---|
| `FORGE_HOME` | `~/.forge` | Root data directory (see precedence above). |
| `FORGE_FILESYSTEM_GLOBAL_ROOT` | `<data-dir>/workspaces` | Base path for the blob-backed file store. Set by the server if unset. |
| `FORGE_DEPENDENCY_CONFIG` | `conf/agent-dependencies.yaml` | Path to the dependency config YAML. |

The `--db` DSN itself isn't an env var, but `FORGE_DATABASE_URL` (documented under [devx](#devx-runtime)) is the deployment-time equivalent used by container/process managers.

### Messaging

| Variable | Default | Purpose |
|---|---|---|
| `RUSTIC_AI_REDIS_MSG_TTL` | `3600` (seconds) | TTL for Redis-backed message history entries. |
| `RUSTIC_AI_NATS_MSG_TTL` | `3600` (seconds) | TTL for NATS JetStream message history (60 days for topics matching `user_notifications:` / `user_message_broadcast`, regardless of this value). |
| `RUSTIC_AI_MESSAGING_MODULE` | — | Python messaging backend module the server tells agents to use (e.g. `rustic_ai.nats.messaging.backend`). |
| `RUSTIC_AI_MESSAGING_CLASS` | — | Python messaging backend class (e.g. `NATSMessagingBackend`). |
| `RUSTIC_AI_MESSAGING_BACKEND_CONFIG` | — | JSON backend config consumed by guild bootstrap. |
| `FORGE_ZMQ_DIR` | `/tmp/forge-zmq` | Override the directory for the agent-messaging ZMQ bridge's IPC unix sockets. |

See [Messaging](../concepts/messaging-protocol/) for how these map onto Redis/NATS resource names.

### Secrets, OAuth & identity

| Variable | Default | Purpose |
|---|---|---|
| `FORGE_SECRET_PROVIDERS` | `env,dotenv,file` | Comma-separated secret-resolution chain order. Add `keychain` to enable the OS keychain (requires the `keychain` package's side-effect import). |
| `FORGE_OAUTH_TOKEN_STORE` | `memory` | OAuth token store backend: `memory` or `keychain`. |
| `FORGE_OAUTH_PROVIDERS_CONFIG` | `conf/oauth-providers.yaml` | Path to the OAuth providers YAML. |
| `FORGE_KEYCHAIN_SERVICE` | `forge` | OS keychain service name used by both the keychain secret provider and the keychain token store. |
| `FORGE_MANAGER_API_BASE_URL` | — | Externally reachable base URL used to build OAuth callback URLs. |
| `FORGE_IDENTITY_MODE` | — | Identity mode for the manager surface. |
| `FORGE_QUOTA_MODE` | — | Quota enforcement mode. |

Full detail on the secret chain, OAuth PKCE flow, and keychain bridging is in [Secrets & OAuth](../features/secrets-oauth/).

### Model-fit / local models

| Variable | Default | Purpose |
|---|---|---|
| `FORGE_LOCAL_MODEL_CATALOG` | `conf/local-model-catalog.yaml` | Path to the local model catalog YAML. |
| `FORGE_MODELFIT_LLAMA_BINARY` | — | Path to a `llama.cpp`-compatible binary used for local model-fit runtime detection. |
| `FORGE_MODELFIT_RUNTIME_CACHE` | — | Cache directory for model-fit runtime capability detection results. |

### Identity, quota & manager

| Variable | Default | Purpose |
|---|---|---|
| `FORGE_MANAGER_API_BASE_URL` | — | Base URL the manager surface uses for externally-reachable links (also used for OAuth callbacks). |
| `FORGE_MANAGER_API_TOKEN` | — | Auth token for the internal manager API surface (`/manager/*`). |
| `FORGE_ENABLE_PUBLIC_API` | — | Toggles the public OpenAPI surface. |
| `FORGE_ENABLE_UI_API` | — | Toggles the UI-facing API surface. |
| `FORGE_STATIC_AGENTS_JSON` | — | Static agent list JSON, used in constrained/manager-driven deployments. |
| `FORGE_STATIC_GUILD_ID` | — | Pins operations to a single static guild ID. |

### Devx / runtime

| Variable | Default | Purpose |
|---|---|---|
| `FORGE_PYTHON_PKG` | — | Path to the local `forge-python` package; required for any run that spawns agents. |
| `FORGE_UV_CACHE_DIR` (+ `UV_CACHE_DIR`, `XDG_CACHE_HOME`, `XDG_DATA_HOME`) | — | Writable cache/data dirs for `uv`/`uvx` when spawning agent processes. |
| `FORGE_UVX_PATH` | — | Override the `uvx` binary path. |
| `FORGE_AGENT_REGISTRY` | — | Path to `forge-agent-registry.yaml`. |
| `FORGE_EXTRA_DEPS` | — | Extra Python dependencies to install alongside an agent. |
| `FORGE_DATABASE_URL` | — | Deployment-time equivalent of `--db` for container/process managers. |
| `FORGE_AGENT_TRANSPORT` | — | Default agent transport (`direct` \| `supervisor-zmq`), deployment-time equivalent of `--default-agent-transport`. |
| `FORGE_INJECT_FS` / `FORGE_INJECT_NET` | — | Sandbox/supervisor injection toggles for filesystem/network access. |
| `FORGE_E2E_ATELIER` | — | Set to `1` to gate the e2e ladder test suite. |
| `FORGE_E2E_ENABLE_LIVE_LLM` | — | Set to `1` to enable live-LLM e2e tests. |

## Agent-process environment variables

When the runtime spawns a Python agent process (via `uvx` + `FORGE_PYTHON_PKG`), `helper/envvars.BuildAgentEnv` assembles a dedicated environment for that child process — distinct from the server/client process environment above.

| Variable | Contents |
|---|---|
| `FORGE_GUILD_JSON` | The guild spec, as JSON. |
| `FORGE_AGENT_CONFIG_JSON` | The agent's spec/config, as JSON. |
| `FORGE_CLIENT_MODULE` / `FORGE_CLIENT_TYPE` / `FORGE_CLIENT_PROPERTIES_JSON` | Messaging client module/class/config the Python side should construct. |
| `FORGE_AGENT_WORKDIR` | The agent's working directory. |
| `FORGE_ZMQ_DIR` | ZMQ bridge socket directory (forwarded; see [Messaging](../concepts/messaging-protocol/)). |
| `FORGE_SUPERVISOR_ZMQ_ENDPOINT` / `FORGE_SUPERVISOR_ZMQ_IDENTITY` / `FORGE_SUPERVISOR_ZMQ_CONFIG_JSON` | ZMQ bridge endpoint, identity, and config for the `supervisor-zmq` agent transport. |

Plus coordinates forwarded from the parent process so the agent's messaging backend can connect directly:

| Variable | Purpose |
|---|---|
| `REDIS_HOST` / `REDIS_PORT` / `REDIS_DB` | Redis coordinates, auto-injected as `redis_client` config when the backend is `RedisMessagingBackend` and not already configured. |
| `NATS_URL` | NATS server URL (default `nats://localhost:4222`), auto-injected as `nats_client` config when the backend is `NATSMessagingBackend`. |
| `RUSTIC_AI_STATE_MANAGER` | State manager backend selection forwarded to the agent process. |

Resolved secrets and OAuth tokens are injected on top of this base set, each keyed by the need's `Label` (see [Secrets & OAuth](../features/secrets-oauth/#secret-injection-into-agents)).

## Redis / NATS resource names

Both messaging backends namespace every topic as `<guild_id>:<topic>` — the guild ID is the namespace. On top of that, each backend has its own physical resource naming:

| Backend | Resource | Naming |
|---|---|---|
| Redis | Direct-lookup cache key | `msg:{namespace}:{id}` |
| Redis | Per-topic history ZSET | `{namespace}:{topic}` (scored by Gemstone timestamp) |
| Redis | Live pub/sub channel | `{namespace}:{topic}` |
| NATS | JetStream subject | `persist.` + sanitize(`{namespace}:{topic}`) |
| NATS | JetStream stream name | `MSGS_` + sanitize(`{namespace}:{topic}`) |
| NATS | KV bucket (by-id lookup) | `msg-cache-` + sanitize(`{namespace}`) |
| NATS | Live pub/sub subject | `{namespace}:{topic}` (core NATS) |

`sanitize()` replaces `:`, `.`, and `$` with `_` — this matches the Python NATS backend's naming exactly so the two runtimes can interoperate on the same JetStream deployment.

### Control-plane queues

| Queue | Purpose |
|---|---|
| `forge:control:requests` | Global control ingest queue; the scheduler `BRPop`s spawn/stop requests here. |
| `forge:control:node:<node_id>` | Per-node queue; the scheduler `LPush`es placement decisions, the matching client `BRPop`s them. |

Every message on these queues is wrapped in a `ControlMessageWrapper`:

```json
{
  "command": "spawn",
  "payload": { "...": "underlying SpawnRequest/StopRequest" }
}
```

### Well-known topics

| Topic | Purpose |
|---|---|
| `system_topic` | System-level control traffic. |
| `guild_status_topic` | Guild status change notifications. |
| `infra_events_topic` | Infrastructure event stream. |
| `user_message_broadcast` | Broadcast user-facing messages (long-retention on NATS). |
| `default_topic` | Default fallback topic. |
| `user:{id}` | Per-user direct topic. |
| `user_notifications:{id}` | Per-user notifications (long-retention on NATS). |
| `user_system:{id}` | Per-user system messages. |
| `user_system_notification:{id}` | Per-user system notifications. |

## Related pages

- [Storage](../features/storage/) — metastore driver selection, DSN normalization, and the blob-backed filesystem in depth.
- [Messaging](../concepts/messaging-protocol/) — Redis vs. NATS backend internals, Gemstone ID ordering, and the ZMQ agent bridge.
- [Secrets & OAuth](../features/secrets-oauth/) — the secret provider chain, OAuth Manager, and keychain integration.
- [Quickstart](../getting-started/quickstart/) — single-process and distributed local run examples using these defaults.
