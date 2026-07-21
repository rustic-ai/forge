# Authoring Guild Specs

A guild spec is the one document that describes an entire multi-agent system: who the agents are, how they depend on shared resources, how messages route between them, and how the outside world talks to it. Get the spec right and everything downstream — bootstrap, spawn, relaunch — just works off the persisted copy.

This guide covers how to structure a spec, split it across files, template it at build time, and build one programmatically in Go without hand-writing YAML at all.

## Anatomy of a guild spec

Every guild spec maps to a `protocol.GuildSpec` (`protocol/spec.go:816`). At the top level it has five things worth thinking about separately:

| Field | Purpose |
|---|---|
| `agents` | The list of `AgentSpec` entries that make up the guild |
| `dependency_map` | Named `DependencySpec` resolvers (filesystem, memory, custom) shared or overridden per agent |
| `routes` | A `RoutingSlip` of `RoutingRule` steps controlling message flow between agents |
| `gateway` | Optional `GatewayConfig` for exposing the guild over an external protocol |
| `properties` / `configuration` | Runtime properties (execution engine, messaging backend, state manager) and build-time template variables |

A minimal spec needs an `id`, a `name` (1-64 chars), a non-empty `description`, and at least one agent:

```yaml
id: my-guild-01
name: My Guild
description: demo guild with one echo agent
agents:
  - name: Echo
    description: echoes messages back
    class_name: rustic_ai.agents.EchoAgent
```

Each agent is an `AgentSpec` (`protocol/spec.go:670`). The only required field is `class_name` — the Python dotted path to the agent implementation that actually runs. Everything else has sane defaults:

- `listen_to_default_topic` defaults to `true`
- `act_only_when_tagged` defaults to `false`
- `additional_topics`, `predicates`, `dependency_map`, `additional_dependencies`, `forge_extra_deps` are all optional
- `resources` (`num_cpus`, `num_gpus`, `custom_resources`) defaults to zero-valued, but must never go negative

### Declaring an agent's Python packages

An agent's `class_name` tells Forge which package to install for the agent *itself*. It says nothing about classes referenced from `properties`. Plugin-style agents — a ReAct agent with a toolset, an LLM agent with a custom plugin — name those by fully-qualified path:

```yaml
agents:
  - name: Data Analyst
    description: analyses CSVs with a pandas toolset
    class_name: rustic_ai.llm_agent.react.react_agent.ReActAgent
    properties:
      toolset:
        kind: rustic_ai.pandas_analyst.react_toolset.DataAnalystReActToolset
    forge_extra_deps:
      - rusticai-pandas-analyst
```

Nothing in that `kind:` path tells the launcher which pip package provides it, so **you have to declare it**. Without `forge_extra_deps` the agent starts, fails to import the toolset, and is restarted by the supervisor in a loop.

Each entry is appended to that agent's `uvx --with` set, and only that agent's — a heavy dependency is not forced on its siblings. Entries may be package specifiers, local paths, or comma-separated lists.

!!! warning "Three similar-looking fields, three different jobs"
    - `forge_extra_deps` — **Python packages** to install into this agent's environment.
    - `dependency_map` / `additional_dependencies` — framework-injected **dependency resolvers** (filesystem, LLM, memory). These do not install anything.
    - `FORGE_EXTRA_DEPS` — the same idea as `forge_extra_deps` but an environment variable applied **guild-wide**, to every agent.

    Full details in [Per-agent Python packages](../reference/configuration/#per-agent-python-packages-forge_extra_deps).

!!! note "The persisted spec is canonical"
    Whatever you submit to `POST /api/guilds` gets normalized and persisted as `GuildModel`/`AgentModel` rows. Every later spawn — including relaunches — reconstructs the spec from the store via `store.ToGuildSpec`, not from your original YAML. Write specs so they're correct standing alone; don't rely on external state that only existed at submission time.

## Modular YAML: `include` and `code`

Real guilds outgrow a single file fast — a dozen agents, each with a system prompt, quickly turns one YAML file into an unreadable wall of text. `guild.ParseFile` (`guild/parse.go`) resolves two custom tags before decoding, so you can split a spec across files and still get back one `GuildSpec`.

**`!include`** splices another YAML file's tree in place of the tag. It only accepts `.yaml`/`.yml` files:

```yaml
id: my-guild-01
name: My Guild
description: demo
agents:
  - !include agents/echo_agent.yaml
  - name: Coder
    description: writes code
    class_name: rustic_ai.agents.CoderAgent
    properties:
      system_prompt: !code prompts/system.txt
```

**`!code`** inlines a file's raw text as a plain string — the natural way to keep long system prompts, few-shot examples, or code snippets out of the YAML itself and under normal source control review.

```yaml
# agents/echo_agent.yaml
name: Echo
description: echoes messages back
class_name: rustic_ai.agents.EchoAgent
```

Includes nest — an included file can itself `!include` further files — which is what makes this useful for organizing agents one-per-file under an `agents/` directory. `ParseFile` tracks a visited-file set while resolving, so a cycle (A includes B includes A) is detected and rejected rather than recursing forever.

!!! warning "Circular includes are a hard failure"
    If file A includes file B and B includes A (directly or transitively), `ParseFile` returns an error instead of hanging or silently truncating the tree. Keep include graphs a DAG.

JSON specs pass through `ParseFile` unchanged — the `include`/`code` tags are YAML-only conveniences; there's no equivalent preprocessing step for JSON input.

## Templating the configuration map

`GuildSpec.Configuration` is a top-level map reserved for mustache templating — it is *not* used at runtime by Bootstrap. When you build a spec through `GuildBuilder.BuildSpec()`, the pipeline runs `resolveTemplates`, which renders every string field in the spec as a mustache template against `Configuration`, but only if `Configuration` is non-empty.

```yaml
id: my-guild-01
name: My Guild
description: demo
configuration:
  region: us-west-2
  model_name: gpt-4o
agents:
  - name: Coder
    description: writes code
    class_name: rustic_ai.agents.CoderAgent
    properties:
      model: "{{model_name}}"
      bucket_region: "{{region}}"
```

This keeps environment-specific values (regions, model names, endpoint hosts) out of the agent definitions themselves — one spec, many rendered variants, no copy-pasted YAML per environment.

!!! tip "Templating is a build-time author feature"
    `resolveTemplates` runs inside `GuildBuilder.BuildSpec()`, not inside `guild.Bootstrap`. If you hand-parse a spec with `guild.ParseFile` and skip the builder, your `{{...}}` placeholders will not be resolved — route the spec through a builder (`GuildBuilderFromYAML`/`GuildBuilderFromJSON`/`GuildBuilderFromSpec`) if you want templating applied.

## Building specs in Go

For specs generated programmatically — dynamic agent counts, specs assembled from a database, or specs built inside a CLI tool — use the fluent builders in the `guild` package instead of round-tripping through YAML.

```go
spec, err := guild.NewGuildBuilder().
    SetName("My Guild").
    SetDescription("demo").
    SetExecutionEngine("rustic_ai.forge.execution_engine.ForgeExecutionEngine").
    AddAgentSpec(protocol.AgentSpec{
        Name:        "Echo",
        Description: "echoes",
        ClassName:   "rustic_ai.agents.EchoAgent",
    }).
    BuildSpec()
```

`AgentBuilder` produces individual `AgentSpec` values with an auto-generated short-UUID `ID`, and validates name, description, and `class_name` plus resource fields when you call its own `BuildSpec()`:

```go
agentSpec, err := guild.NewAgentBuilder().
    SetName("Coder").
    SetDescription("writes code").
    SetClassName("rustic_ai.agents.CoderAgent").
    SetResources(protocol.ResourceSpec{NumCPUs: 2}).
    BuildSpec()
```

`RouteBuilder` builds a `RoutingRule` from either an `AgentTag`/`AgentSpec` source or a plain string `agent_type`, letting you wire routing without hand-writing `RoutingSlip` YAML:

```go
route, err := guild.NewRouteBuilder().
    SetAgent(agentSpec).
    SetMethodName("on_message").
    BuildSpec()
```

Entry points for loading existing specs into a builder: `GuildBuilderFromSpec`, `GuildBuilderFromYAML(File)`, `GuildBuilderFromJSON(File)` — useful when you want to load a YAML/JSON spec and then programmatically append agents or routes before finalizing.

### Accumulated-first-error semantics

Every setter on `GuildBuilder`, `AgentBuilder`, and `RouteBuilder` returns the builder itself so calls chain, but errors don't abort the chain immediately. Each builder tracks the *first* error it hit internally; subsequent setter calls become no-ops, and the error surfaces only when you call `BuildSpec()`. This means you can write a long fluent chain without a `if err != nil` after every line — check once, at the end:

```go
spec, err := guild.NewGuildBuilder().
    SetName(""). // invalid: triggers the first error
    SetDescription("demo").
    AddAgentSpec(agentSpec).
    BuildSpec() // err is non-nil here; SetDescription/AddAgentSpec still ran but the error is remembered
if err != nil {
    // handle the first validation failure in the chain
}
```

`BuildSpec()` itself runs the full pipeline in order: `applyDefaults`, `mergeDependencyMap` (forge-home config, then conf path), `resolveTemplates` (mustache over `Configuration`), then `Validate`. If a setter already recorded an error, `BuildSpec()` returns it before running the pipeline.

## Dependency map precedence

A guild's `dependency_map` holds named `DependencySpec` entries (`ClassName`, `ProvidedType`, `Properties`) — filesystem access, memory backends, or custom resolvers agents can request by name. Dependencies can be declared at three layers, and they are merged, not overwritten:

1. **Spec-level** `dependency_map` (highest precedence — wins on key conflicts)
2. **Forge-home** `agent-dependencies.yaml` (resolved via `forgepath.DependencyConfigFile`)
3. **Conf path** `agent-dependencies.yaml` (resolved via `forgepath.DependencyConfigPath`, default `conf/agent-dependencies.yaml`)

The merge only *adds* keys that are missing at a higher layer — it never overwrites a key your spec already defined. Both `GuildBuilder.BuildSpec()` and `guild.Bootstrap` perform this merge (forge-home first, then conf), so a dependency you declare in the spec is always safe from being clobbered by environment-wide defaults.

!!! danger "A half-declared dependency is worse than none"
    The merge is **key-level, not property-level**. Declaring a key with empty `properties` still counts as declaring it, so the defaults are skipped and nothing fills the gap:

    ```yaml
    dependency_map:
      llm:
        class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
        properties: {}          # blocks the default `model`; does NOT inherit it
    ```

    This fails at agent startup with `LiteLLMResolver.__init__() missing 1 required positional argument: 'model'`, not at validation time. Either omit the key entirely to take the configured default, or specify it fully. The same applies on the Python side (`GuildHelper.get_guild_dependency_map`), so the behaviour is identical however the guild is launched.

### Pointing filesystem dependencies at file/S3/GCS roots

The built-in filesystem dependency's `path_base` and `protocol` are rewritten at bootstrap time by `guild.ApplyFilesystemGlobalRoot`, driven by the `FORGE_FILESYSTEM_GLOBAL_ROOT` env var. It rewrites the root for both the guild-level dependency map and every agent-level dependency map, and supports three protocols:

```yaml
dependency_map:
  filesystem:
    class_name: rustic_ai.forge.dependencies.filesystem.FileSystemDependency
    properties:
      protocol: s3
      path_base: my-bucket/guild-workspaces
      storage_options:
        region: us-west-2
```

Recognized roots: `file` (local disk, the default), `s3`, and `gs`/`gcs`. `ApplyFilesystemGlobalRoot` enforces path-traversal protection, checks bucket/scheme matching for object stores, and requires the resolved path stay contained within the configured root — a spec cannot point itself outside the sandboxed root Forge was configured with.

!!! note "Env var and defaults"
    If `FORGE_FILESYSTEM_GLOBAL_ROOT` is unset, the server falls back to `<forge-home>/data/workspaces` and file storage is scoped per `orgID/guildID/agentID` (or `GUILD_GLOBAL` when no agent-specific dependency applies). See [Storage & Filesystem](../features/storage/) for the full path-resolution model.

## Validation checklist

Before submitting a spec — whether hand-written or builder-produced — run through these checks. `guild.Validate` and `protocol.GuildSpec.Validate` are split across two layers, and both must pass:

- **Name length**: `name` must be 1-64 characters (`protocol.GuildSpec.Validate`).
- **Description non-empty**: `description` must be set (`protocol.GuildSpec.Validate`).
- **At least one agent**: `guild.Validate` rejects a spec with an empty `agents` list.
- **Unique agent names**: no two `AgentSpec` entries in the same guild may share a `name`.
- **Non-negative resources**: `NumCPUs`, `NumGPUs`, and any `CustomResources` values must be `>= 0`.
- **Messaging object shape**: if you set `properties.messaging` yourself, it must conform to the expected shape `guild.Validate` checks for.

```go
if err := guild.Validate(spec); err != nil {
    log.Fatalf("invalid guild spec: %v", err)
}
```

When you build through `GuildBuilder.BuildSpec()`, this validation runs automatically as the last pipeline step — a builder chain that reaches `BuildSpec()` without error has already passed every rule above.

## Submitting the spec

Once you have a valid `GuildSpec`, either hand it to `guild.Bootstrap` directly in Go, or POST it to the HTTP API:

```bash
curl -X POST http://localhost:PORT/api/guilds \
  -H 'Content-Type: application/json' \
  -d '{
    "organization_id": "acme",
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

`Bootstrap` runs `applyDefaults`, merges dependencies (forge-home, then conf), applies the filesystem global root, persists `GuildModel`/`AgentModel` rows with status `requested`, and enqueues the system `GuildManagerAgent` to drive the rest of the launch. See [Guild Lifecycle](../concepts/guild-model/) for what happens after submission, and [Quickstart](../getting-started/quickstart/) for a first end-to-end run.
