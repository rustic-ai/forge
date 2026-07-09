# Glossary

Forge's vocabulary spans two runtimes (Go control plane, Python execution bridge) and half a dozen subsystems. This page is the single authoritative definition for every term the rest of the docs link to — if a word means something specific in Forge, it's defined here.

Terms are grouped by area. Within each group they're alphabetical.

## Core domain

**Guild**
: A named, persisted multi-agent system: a collection of `AgentSpec`s plus shared configuration (execution engine, messaging backend, dependency resolvers, routing rules, gateway). Defined by the `guild` package (`github.com/rustic-ai/forge/forge-go/guild`). A guild is authored as a `GuildSpec`, persisted as a `GuildModel` + `AgentModel` rows, and launched by spawning its `GuildManagerAgent`. See [Guild](../concepts/guild-model/).

**Agent**
: A single unit of work inside a guild — an OS process running Python code (a dotted `ClassName`) that Forge's Process Supervisor spawns, monitors, and restarts. Declared as an `AgentSpec` in a `GuildSpec`.

**GuildSpec**
: The core struct (`protocol.GuildSpec`, `protocol/spec.go:816`) describing a guild: `ID`, `Name`, `Description`, `Properties` (holds `execution_engine`, `messaging`, `state_manager`, `state_manager_config`), `Configuration` (mustache-templated), `Agents`, `DependencyMap`, `Routes` (a `RoutingSlip`), and `Gateway`. Has `Normalize()` and `Validate()` (name 1-64 chars, non-empty description) and serializes as snake_case JSON/YAML.

  !!! note "The persisted spec is canonical"
      Every downstream spawn re-hydrates the spec via `store.ToGuildSpec(GetGuild(id))`, not the originally submitted JSON. Normalization must happen before `CreateGuildWithAgents` runs in `guild.Bootstrap`.

**AgentSpec**
: One agent's declaration inside a `GuildSpec` (`protocol/spec.go:670`). Fields include `ID`, `Name`, `Description`, `ClassName` (required Python dotted path), `AdditionalTopics`, `Properties`, `ListenToDefaultTopic` (default `true`), `ActOnlyWhenTagged` (default `false`), `Predicates`, `DependencyMap`, `AdditionalDependencies`, `Resources` (`NumCPUs`/`NumGPUs`/`CustomResources`), and `QOS`.

**GuildManagerAgent (GMA)**
: The system agent that drives guild launch and lifecycle from the Python side. Class name is the constant `guild.GuildManagerClassName` = `rustic_ai.forge.agents.system.guild_manager_agent.GuildManagerAgent`. `guild.Bootstrap` spawns it with `AgentSpec.ID` = `<guildID>#manager_agent`, name `<GuildName> Manager`, `AdditionalTopics` of `system_topic`/`heartbeat_topic`/`guild_status_topic`, and `ListenToDefaultTopic: false`. It does a manager round-trip back to Go via `POST /manager/guilds/ensure` and spawns child agents through the same spawn path as everything else.

**Route / RoutingSlip**
: `GuildSpec.Routes` is a `RoutingSlip` containing `Steps` of `RoutingRule` (`protocol/spec.go:586`): `Agent`/`AgentType`, `MethodName`, `OriginFilter`, `MessageFormat`, `Destination` (topics + recipients), `MarkForwarded`, `RouteTimes` (default 1), `Transformer`, `AgentStateUpdate`/`GuildStateUpdate`, `ProcessStatus`, `Reason`. Built fluently with `guild.NewRouteBuilder`. `RouteStatus` is `active` or `deleted`.

**Gateway**
: The real-time client edge (`gateway` package) that bridges WebSocket clients to the guild's messaging backend. Also refers to the optional `GatewayConfig` on a `GuildSpec` (`Enabled`, `InputFormats`, `OutputFormats`, `ReturnedFormats`) — when enabled, `GuildBuilder` auto-appends a `GatewayAgent` (`rustic_ai.core.guild.g2g.gateway_agent.GatewayAgent`) if one isn't already present. See [Gateway](../features/gateway-websockets/).

## Control plane

**Server**
: The `forge server` subcommand (`command/server.go`, `ServerCmd`) — the central control plane. Starts the HTTP API, metastore, scheduler, node registry, placement map, and reconciler. Flags parse into `agent.ServerConfig`, handed to `agent.StartServer(ctx, cfg)`. Default `--listen` is `:9090`; default `--db` is `sqlite://<forge-home>/data/forge.db`.

**Client / Node**
: The `forge client` subcommand (`command/client.go`, `ClientCmd`) — a worker/compute node daemon. Detects local hardware, registers with the server, and runs agent processes via a Process Supervisor. Flags parse into `agent.ClientConfig`, handed to `agent.StartClient(ctx, cfg)`. In single-process mode (`server --with-client`), an in-process client/node runs alongside the server.

**NodeRegistry**
: `scheduler/registry.go`'s in-memory `map[string]*NodeState` (exposed as `scheduler.GlobalNodeRegistry`), guarded by an `RWMutex`. Each `NodeState` tracks `NodeID`, `TotalCapacity`/`UsedCapacity` (`ResourceCapacity{CPUs, Memory, GPUs}`), and `LastHeartbeat`. A node is healthy (`IsHealthy`/`ListHealthy`) only if `time.Since(LastHeartbeat) < 10s`.

  !!! warning "Two heartbeat thresholds, not one"
      The registry marks a node unhealthy at 10s of silence, but the Reconciler doesn't declare it dead and evict it until 15s (`DeadNodeTimeout`). Between 10s and 15s a node is invisible to the scheduler but not yet reclaimed.

**Scheduler**
: `scheduler.GlobalScheduler` wraps the `NodeRegistry` and performs best-fit placement. `Schedule(agentSpec protocol.AgentSpec) (string, error)` filters healthy nodes to those with enough remaining CPU/Memory/GPU, then picks the node with the *highest* score `remMem + remCPUs*1024` — a most-free / worst-fit spread rather than tight bin-packing. On success it immediately calls `AllocateCapacity` on the chosen node.

**PlacementMap**
: `scheduler.GlobalPlacementMap`, an in-memory `map["guildID:agentID"]AgentPlacement` tracking each spawn's `SpawnState` lifecycle: `accepted → dispatched → acknowledged → running`, plus terminal `failed`. Stores the original byte-for-byte `SpawnRequest` payload so a dead node's agents can be re-enqueued as fresh spawns.

  !!! note "In-memory only"
      PlacementMap state is not persisted. A control-plane/leader restart loses accepted/dispatched tracking; recovery then leans on the `AgentStatusStore` (TTL'd in Redis/NATS) and idempotency gates instead.

**Reconciler**
: The background loop (`scheduler/reconciler.go`, `NewReconciler`) that runs every `ReconcileInterval` (default 15s) but only does work when `elector.IsLeader()` is true. Each tick runs five ordered phases: `reconcileDeadNodes`, `reconcileAccepted`, `reconcileStaleDispatches`, `reconcileStaleAcks`, `cleanupFailedPlacements`. Defaults: `AckTimeout` 30s, `LaunchTimeout` 120s, `MaxAttempts` 5, `DeadNodeTimeout` 15s, `FailedCleanupAge` 5m. Dead-node handling re-pushes the cached `Payload` back onto `forge:control:requests` — indistinguishable from a brand-new spawn.

**Leader Elector**
: The `leader.LeaderElector` interface (`Acquire`/`IsLeader`/`Resign`/`Watch`) that gates the Reconciler to a single writer per cluster. Three implementations: `RedisElector` (SET NX + TTL lock on `forge:control:leader`, 5s TTL, Lua-script renewal), `RaftElector` (HashiCorp raft + memberlist gossip, dummy FSM — leadership only, no replicated state), and `SingleNodeElector` (always leader, for single-process mode). Selection: raft if `LeaderElectionMode == "raft"`, else Redis if a Redis client exists, else single-node.

## Messaging

**Backend**
: The `messaging.Backend` interface (`messaging/backend.go`) — the data-plane abstraction with methods `PublishMessage`, `GetMessagesForTopic`, `GetMessagesSince`, `GetMessagesByID`, `Subscribe`, `Close`. Two implementations satisfy it: `RedisBackend` (ZSET history + string cache + PubSub) and `NATSBackend` (JetStream stream + KV bucket + core NATS pub/sub). Selected at server startup via `--backend redis|nats`.

**Subscription**
: The interface returned by `Backend.Subscribe`, exposing `Channel() <-chan SubMessage`, `ErrChannel() <-chan error`, and `Close()`. Both the Redis and NATS implementations buffer 100 messages and drop delivery (with a warning) if the consumer channel is full for 50ms — live delivery is at-most-once and best-effort; durable history is the source of truth for replay.

**GemstoneID**
: A 64-bit snowflake-style message ID (`protocol/gemstoneid.go`) packing `Priority`, `Timestamp` (ms since epoch), `MachineID` (0-255), and `SequenceNumber` (0-4095). Both messaging backends sort retrieved messages via `protocol.Compare` on parsed Gemstone IDs, and it's the basis for the Redis ZSET score and NATS `StartTime` hints.

**namespace / topic**
: Every topic is namespaced as `namespace + ":" + topic`, where `namespace` is the guild ID — mirroring how Python's `MessagingInterface` prepends `{guild_id}:` internally. `PublishMessage` stores the *bare* (un-namespaced) topic in `msg.TopicPublishedTo`.

**usercomms / syscomms**
: The two WebSocket socket kinds the [Gateway](../features/gateway-websockets/) exposes per guild. `usercomms` carries conversational/business traffic (outbound subscription: `user_notifications:<user_id>` only). `syscomms` carries system notifications, guild health, and infra lifecycle events (outbound subscription: `user_system_notification:<user_id>`, `guild_status_topic`, and `infra_events_topic` — all on one socket). Public routes: `GET /ws/guilds/:id/usercomms/:user_id/:user_name` and `GET /ws/guilds/:id/syscomms/:user_id`.

**InfraEvent**
: A lifecycle/telemetry event (format `rustic_ai.forge.runtime.InfraEvent`) published by the `infraevents.Publisher` onto `infra_events_topic`, surfaced to clients only via `syscomms`. Shape includes `schema_version`, `event_id`, `kind` (e.g. `agent.process.failed`), `severity`, `timestamp`, `guild_id`, `agent_id`, `attempt`, `message`, `detail`. Because guild launch is asynchronous, HTTP `201` from create/relaunch only means "accepted" — the InfraEvent stream on `syscomms` is the real progress/failure signal.

## Runtime

**Process Supervisor**
: The component the worker `client` daemon uses to manage OS-level agent lifecycle: starts agents via `exec.CommandContext`, stops them with POSIX signals to the process group, and applies cgroup/resource limits. Configurable per node (`--client-default-supervisor` / `--default-supervisor`: `docker` or `bwrap`). Local crash recovery uses exponential backoff (base 1s, max 30s, ±25% jitter, 10 retries, `StableTime` 60s resets the attempt counter).

**AgentStatusStore**
: The distributed source of truth the Reconciler consults to disambiguate "message delivered" from "agent actually launched." Interface: `WriteStatus` (with TTL), `RefreshStatus`, `GetStatus` (returns `nil, nil` if absent), `DeleteStatus`. Backed by Redis (`forge:agent:status:<guildID>:<agentID>`, SET/GET/EXPIRE/DEL) or a NATS KV-backed implementation. States the Reconciler checks are `"starting"` and `"running"`.

**control queues**
: The Redis/NATS queues carrying spawn/stop commands. `forge:control:requests` is the single global ingest queue (the Scheduler `BRPOP`s it); each node has its own `forge:control:node:<node_id>` queue (`LPush`ed by the scheduler, `BRPop`ped by the client). Every payload is wrapped as `{"command": "spawn", "payload": {...}}` (`ControlMessageWrapper`). NATS uses JetStream work-queue streams (`CTRL_<sanitized-key>`) as the equivalent transport.

**SpawnRequest / SpawnResponse**
: The control payload types (`protocol` package) that move an agent from "declared" to "running." A `SpawnRequest` carries the `AgentSpec` (plus, for DB-less workers, the enriched guild messaging config and full `guild_spec`); the worker's `ControlQueueHandler.handleSpawn` calls the supervisor and returns a `SpawnResponse{NodeID, PID}`. The server's `OnSpawn` handler is two-phase: it `MarkAccepted`s and acks the caller immediately, then dispatches (`Schedule` + `MarkDispatched` + push to the node queue) in a background goroutine — so acceptance and actual placement are decoupled.

## Observability & storage

**OTLP**
: The OpenTelemetry Protocol used to export traces and metrics. Forge's telemetry has two export modes (`telemetry.Config.Mode`): `TelemetryModeExternalOTLP` (`"external_otlp"`) ships straight to a user-supplied OTLP/HTTP endpoint (`--otel-endpoint`), and traces/metrics go to `/v1/traces` and `/v1/metrics` respectively.

**desktop_sqlite**
: The other telemetry mode (`TelemetryModeDesktopSQLite`, `"desktop_sqlite"`). Launches a bundled `sqlite-otel` sidecar process (`--otel-sqlite-binary`) that listens for OTLP/HTTP on `--otel-sqlite-port` (default 4318) and writes spans into a local SQLite DB (default `<forge-home>/telemetry/sqlite-otel.db`). This local span store is what the `/rustic/observe/guilds/:guild_id/messages/:msg_id/spans` API queries; that endpoint returns `501 Not Implemented` when telemetry mode isn't `desktop_sqlite`.

**metastore vs. `store/` stub**
: The relational metastore (guilds, agents, routes, board, catalog/blueprints — ~20 GORM models) lives in `guild/store` (Go import `github.com/rustic-ai/forge/forge-go/guild/store`), *not* the top-level `store/` package.

  !!! warning "`store/` is an empty placeholder"
      The top-level `store` package is just `package store` with a doc comment — no metastore code. Don't document it as "the metastore"; that's `guild/store`.

  The metastore driver/DSN is resolved by `store.ResolveDriverAndDSN(rawDSN)`, supporting `sqlite` (pure-Go `modernc.org/sqlite`, so CGO-disabled builds still work) and `postgres` (`postgres://`, `postgresql://`, `postgresql+psycopg://`). Default DSN is `sqlite://<forge-home>/data/forge.db` (`--db`).

**filesystem scope / GUILD_GLOBAL**
: The `filesystem` package's blob store (`gocloud.dev/blob`-backed, supporting local disk, S3, GCS) scopes every stored file to a `Scope{Protocol, BucketURL, ObjectPath, LocalRoot}`, where `ObjectPath = path.Join(orgID, guildID, agentID)`. When a file belongs to the guild as a whole rather than a specific agent, `agentID` is replaced with the sentinel constant `filesystem.GuildGlobalScope = "GUILD_GLOBAL"`. Files live under `--data-dir` (default `<forge-home>/data`) + `workspaces/`, exported as `FORGE_FILESYSTEM_GLOBAL_ROOT`.

## Security & model-fit

**SecretProvider**
: The single-method interface (`secrets.SecretProvider`, `Resolve(ctx, key string) (string, error)`) implemented by every secret backend: `EnvSecretProvider`, `DotEnvSecretProvider` (`~/.forge/secrets/.env`), `FileSecretProvider` (`~/.forge/secrets/<key>`), and the OS-keychain-backed `keychain.SecretProvider`. `secrets.DefaultProvider()` chains them per `FORGE_SECRET_PROVIDERS` (default `"env,dotenv,file"`); the chain returns the first success or `secrets.ErrSecretNotFound`.

**OAuth StoreKey**
: The convention that ties the OAuth token store, the keychain provider, and secret resolution together: `oauth.StoreKey(orgID, providerID) = "oauth:" + orgID + "|" + providerID`. An `OAuthNeed` on a registry entry resolves via this key through the secret chain; if the chain includes the `keychain` provider, it delegates to `oauth.Manager.GetAccessToken` for a live, auto-refreshed access token rather than a static secret.

**SystemProfile**
: The model-fit subsystem's (`modelfit` package) merged view of the host machine: RAM, CPU cores, GPU count/VRAM, selected `Backend`, `RuntimeUsableAcceleration`, `Confidence`, and `ReasonCodes`. It combines static hardware detection (`HardwareProfile`) with a live probe of the local `llama-server` binary (`RuntimeCapabilityProfile`) — distinguishing "a GPU exists" from "the runtime can actually offload to it." Exposed via `GET /rustic/modelfit/capabilities`.

**FitLevel / FitResult**
: `FitLevel` is one of `perfect` / `good` / `marginal` / `too_tight`, decided purely by memory-utilization percentage (`classify()`: ≤70% perfect, ≤85% good, ≤100% marginal, else too_tight). `FitResult` is the deterministic evaluation of one curated local model against one `SystemProfile` — `fit_level`, `runnable`, `estimated_memory_bytes`, `utilization_pct`, `score`, `selected_backend`, `confidence`, `explanations`. Ranked recommendations are served from `GET /rustic/modelfit/local-models` (query params `use_case`, `limit`, `runnable_only`).

## See also

- [Overview](../getting-started/quickstart/) for how these pieces fit into a running Forge deployment.
- [Guild](../concepts/guild-model/) and [Gateway](../features/gateway-websockets/) for the domain concepts in depth.
- [CLI Reference](cli/) for the full flag set on `server` and `client`.
