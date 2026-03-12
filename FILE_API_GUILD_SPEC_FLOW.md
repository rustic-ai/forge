# File API Guild Spec Flow

This note captures how the guild spec moves through Forge today, and where a Forge-specific `filesystem.path_base` rewrite would need to happen if it should be persisted once and then travel unchanged across the system.

## 1. Public Guild Launch Flow

```mermaid
sequenceDiagram
    autonumber
    actor User
    participant API as forge-go/api/guild.go<br/>HandleCreateGuild
    participant Bootstrap as forge-go/guild/bootstrap.go<br/>Bootstrap
    participant Store as forge-go/store
    participant Redis as Redis control queue
    participant Server as forge-go/agent/server.go<br/>queueListener.OnSpawn
    participant Ctrl as forge-go/control/handler.go<br/>handleSpawn
    participant Env as forge-go/helper/envvars/envvars.go<br/>BuildAgentEnv
    participant Runner as forge-python/agent_runner.py
    participant GMA as forge-python/guild_manager_agent.py

    User->>API: POST /api/guilds
    API->>Bootstrap: Bootstrap(spec, orgID, dependencyConfigPath())
    Bootstrap->>Bootstrap: applyDefaults(spec)
    Bootstrap->>Bootstrap: mergeDependencies(spec, configPath)
    Note over Bootstrap: This is the first good hook<br/>to rewrite filesystem.path_base once
    Bootstrap->>Store: CreateGuildWithAgents(guildModel, agentModels)
    Bootstrap->>Redis: EnqueueGuildManagerSpawn(spec, orgID)

    Redis->>Server: SpawnRequest for GuildManagerAgent
    Server->>Store: GetGuild(req.GuildID)
    Server->>Server: req.ClientProperties["guild_spec"] = store.ToGuildSpec(gm)

    Server->>Ctrl: dispatch spawn to node queue
    Ctrl->>Store: GetGuild(req.GuildID)
    Ctrl->>Ctrl: guildSpec = store.ToGuildSpec(guildModel)
    Ctrl->>Env: BuildAgentEnv(guildSpec, agentSpec, ...)
    Env->>Runner: FORGE_GUILD_JSON=<persisted guild spec>
    Runner->>Runner: _load_guild_spec(FORGE_GUILD_JSON)
    Runner->>GMA: start GuildManagerAgent(props.guild_spec)
```

Relevant call sites:

- `forge-go/api/guild.go#HandleCreateGuild`
- `forge-go/guild/bootstrap.go#Bootstrap`
- `forge-go/guild/bootstrap.go#EnqueueGuildManagerSpawn`
- `forge-go/agent/server.go#queueListener.OnSpawn`
- `forge-go/control/handler.go#handleSpawn`
- `forge-go/helper/envvars/envvars.go#BuildAgentEnv`
- `forge-python/src/rustic_ai/forge/agent_runner.py#main`

## 2. GuildManagerAgent Manager Round-Trip

```mermaid
sequenceDiagram
    autonumber
    participant GMA as forge-python/guild_manager_agent.py
    participant Client as forge-python/manager_client.py
    participant MAPI as forge-go/api/manager.go<br/>HandleManagerEnsureGuild
    participant Store as forge-go/store

    GMA->>Client: ensure_guild(guild_spec, organization_id)
    Client->>MAPI: POST /manager/guilds/ensure

    MAPI->>Store: GetGuild(guildID)

    alt guild exists
        Store-->>MAPI: guild model
        MAPI->>MAPI: store.ToGuildSpec(model)
        MAPI-->>Client: persisted guild_spec
        Client-->>GMA: persisted guild_spec
    else guild missing
        Note over MAPI: This path persists req.GuildSpec directly
        MAPI->>MAPI: normalizeManagerSpecIDs(spec)
        MAPI->>Store: store.FromGuildSpec(spec, orgID)
        MAPI->>Store: CreateGuildWithAgents(...)
        MAPI->>Store: GetGuild(guildID)
        MAPI->>MAPI: store.ToGuildSpec(created)
        MAPI-->>Client: created guild_spec
        Client-->>GMA: created guild_spec
    end
```

Relevant call sites:

- `forge-python/src/rustic_ai/forge/agents/system/guild_manager_agent.py#GuildManagerAgent.__init__`
- `forge-python/src/rustic_ai/forge/metastore/manager_client.py#ensure_guild`
- `forge-go/api/manager.go#HandleManagerEnsureGuild`

Important detail:

- In the normal launch flow, `HandleManagerEnsureGuild` usually hits the `guild exists` branch and reads the persisted spec.
- But the handler also has a `guild missing` branch that can persist a guild spec directly.

## 3. Child Agent Spawn Flow

```mermaid
sequenceDiagram
    autonumber
    participant GMA as GuildManagerAgent
    participant Exec as forge-python/execution_engine.py<br/>ForgeExecutionEngine
    participant Redis as Redis control queue
    participant Server as forge-go/agent/server.go<br/>queueListener.OnSpawn
    participant Store as forge-go/store
    participant Ctrl as forge-go/control/handler.go
    participant Env as forge-go/helper/envvars/envvars.go
    participant Runner as forge-python/agent_runner.py

    GMA->>Exec: GuildBuilder.from_spec(persisted_spec).launch(...)
    Exec->>Redis: LPUSH SpawnRequest

    Redis->>Server: OnSpawn(req)
    Server->>Store: GetGuild(req.GuildID)
    Server->>Server: req.ClientProperties["guild_spec"] = store.ToGuildSpec(gm)

    Server->>Ctrl: node control queue
    Ctrl->>Store: GetGuild(req.GuildID)
    Ctrl->>Ctrl: guildSpec = store.ToGuildSpec(guildModel)
    Ctrl->>Env: BuildAgentEnv(guildSpec, req.AgentSpec, ...)
    Env->>Runner: FORGE_GUILD_JSON=<persisted guild spec>
    Runner->>Runner: guild_spec = _load_guild_spec(FORGE_GUILD_JSON)
```

Relevant call sites:

- `forge-python/src/rustic_ai/forge/execution_engine.py#run_agent`
- `forge-go/agent/server.go#queueListener.OnSpawn`
- `forge-go/control/handler.go#handleSpawn`
- `forge-go/helper/envvars/envvars.go#BuildAgentEnv`

## Conclusion

If the goal is:

- resolve `dependency_map.filesystem.properties.path_base` once,
- save the resolved value into the guild spec,
- and let that exact resolved spec flow everywhere after that,

then the main insertion point is:

- `forge-go/guild/bootstrap.go#Bootstrap`

Specifically:

- after dependency defaults are merged,
- before the spec is persisted,
- before the spec is embedded into the initial spawn payload.

If the codebase should be fully consistent even when the manager ensure path is the first writer, the same normalization must also exist in:

- `forge-go/api/manager.go#HandleManagerEnsureGuild`

because that handler can also create and persist a guild when it does not already exist.
