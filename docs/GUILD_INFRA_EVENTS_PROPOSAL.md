# Guild Infrastructure Events Proposal

## Goal

Expose Forge runtime and orchestration progress to HTTP/WS clients as first-class,
guild-scoped events without mixing those events into normal agent-to-agent or
user-to-agent communication.

This proposal is grounded in the current Forge implementation in:

- `forge-go/api`
- `forge-go/control`
- `forge-go/supervisor`
- `forge-go/gateway`
- `forge-python/src/rustic_ai/forge/agents/system/guild_manager_agent.py`

## Current Behavior

### HTTP launch is enqueue-only

`POST /guilds` via `HandleCreateGuild()` calls `guild.Bootstrap()`, which:

1. applies defaults and merges dependencies
2. persists the guild and agents
3. enqueues a `SpawnRequest` for the guild manager agent
4. returns success to the HTTP caller

It does not wait for the manager-agent spawn result.

Implication:

- the HTTP client learns that launch was accepted and persisted
- it does not learn whether the manager-agent process actually started

### Launch success and failure exist on the control response channel

`ControlQueueHandler.handleSpawn()` sends either:

- `protocol.SpawnResponse`
- `protocol.ErrorResponse`

Those responses are published to the control transport response path keyed by
`request_id`.

Implication:

- a control-plane client using Redis/NATS and `WaitResponse()` can receive exact
  launch failures
- a browser client using HTTP/WS does not naturally receive them today

### Status is coarse and partly indirect

Forge currently exposes state through:

- guild status rows in the metastore
- agent status rows in the metastore
- supervisor status entries in `AgentStatusStore`

Those are useful snapshots, but they are not a lifecycle timeline.

Current limitations:

- they do not explain which step failed
- they collapse multiple launch phases into a few states
- they do not preserve ordered runtime facts
- some early spawn failure paths can leave a temporary stale `"starting"` status
  in `AgentStatusStore` until TTL expiry

### `syscomms` is already the closest WS surface for runtime traffic

`gateway.SysCommsHandler()` currently subscribes to:

- `user_system_notification:{user_id}`
- `guild_status_topic`

It also publishes an initial `HealthCheckRequest` to `guild_status_topic` when
the socket connects.

Implication:

- `syscomms` already behaves more like an operator/runtime socket than a normal
  user conversation channel
- but its event model is still ad hoc and tied to guild manager health messages,
  not Forge infrastructure lifecycle

### Guild manager health traffic is not enough

The Python guild manager currently uses `GuildTopics.GUILD_STATUS_TOPIC` for
health-related messages such as `AgentsHealthReport`.

That channel is useful, but it is not designed to represent:

- request acceptance
- control dispatch
- per-agent spawn handoff
- process start failure
- restart loops
- node placement decisions

## Problem Statement

Forge currently has three separate representations of launch/runtime state:

1. enqueue acceptance through HTTP
2. exact spawn success/failure through control responses
3. coarse status/heartbeat state through metastore and guild status messaging

The UI needs a single guild-scoped event stream that answers:

- what is Forge doing for this guild right now?
- which component emitted this event?
- which agent does it apply to?
- did launch advance, stall, retry, fail, or recover?
- what exact error happened?

## Proposal Summary

Introduce a dedicated guild-scoped infrastructure event stream in the messaging
namespace for each guild.

This stream is for runtime/control-plane events only. It is not for agent
business messages, user messages, or normal guild application traffic.

The stream should be:

- append-only
- structured
- emitted by Forge Go and Forge Python runtime components
- consumable over WebSocket
- optionally queryable as recent history

## Topic Naming

### Recommendation

Add a new guild-local topic:

- `guild_infra_events`

Rationale:

- consistent with existing plain string topic names such as `guild_status_topic`
- explicit about scope: guild-local
- explicit about domain: infrastructure
- avoids overloading `guild_status_topic`, which already has established health
  semantics

### Alternatives considered

- `forge.infra.events`
  - clearer ownership
  - less consistent with existing topic naming

- `infra.events`
  - short
  - too generic for a system that may later add other infra streams

- reuse `guild_status_topic`
  - smallest surface change
  - bad semantic fit
  - mixes health/application status with transport/supervisor/control events

### Recommendation on compatibility

Do not replace `guild_status_topic`.

Instead:

- keep `guild_status_topic` for guild manager health and status-oriented messages
- add `guild_infra_events` for runtime lifecycle events

This preserves current behavior while creating a clean boundary.

## Event Model

### Design principles

Infrastructure events should be:

- facts, not mutable snapshots
- specific to a lifecycle step
- attributable to a component
- correlated by `guild_id`, `agent_id`, `request_id`, and `node_id`
- safe to show directly in the UI

They should not:

- require the UI to parse log text
- reuse agent business message formats
- depend on implicit topic meaning

### Envelope

Each infrastructure event should use the normal `protocol.Message` transport with
a dedicated format such as:

- `rustic_ai.forge.runtime.InfraEvent`

The payload should be a structured JSON object:

```json
{
  "schema_version": 1,
  "event_id": "01HV...",
  "kind": "agent.process.start_failed",
  "severity": "error",
  "timestamp": "2026-03-25T18:42:11.431Z",
  "guild_id": "guild-1",
  "agent_id": "guild-1#manager_agent",
  "organization_id": "org-1",
  "request_id": "bootstrap-guild-1",
  "node_id": "node-a",
  "source": {
    "component": "forge-go.control-handler",
    "instance_id": "node-a"
  },
  "attempt": 1,
  "message": "failed to start agent process",
  "detail": {
    "error": "executable file not found in $PATH",
    "runtime": "process"
  }
}
```

### Required fields

- `schema_version`
- `event_id`
- `kind`
- `severity`
- `timestamp`
- `guild_id`
- `source.component`
- `message`

### Recommended optional fields

- `agent_id`
- `organization_id`
- `request_id`
- `node_id`
- `attempt`
- `detail`
- `traceparent`

### Severity

Recommended values:

- `debug`
- `info`
- `warning`
- `error`

### Event kinds

Use stable, dot-delimited kind names.

Recommended top-level groups:

- `guild.launch.*`
- `agent.spawn.*`
- `agent.process.*`
- `agent.runtime.*`
- `guild.stop.*`

## Proposed Event Taxonomy

### Guild launch events

- `guild.launch.requested`
  - emitted when Forge accepts a guild create/relaunch request at the API layer

- `guild.launch.persisted`
  - emitted after guild and agent metadata are stored successfully

- `guild.launch.enqueue_requested`
  - emitted before the guild manager `SpawnRequest` is pushed

- `guild.launch.enqueued`
  - emitted after the guild manager `SpawnRequest` is pushed successfully

- `guild.launch.enqueue_failed`
  - emitted if enqueue fails

These events belong to the bootstrap/control boundary, not to the guild manager.

### Agent spawn dispatch events

- `agent.spawn.dispatched`
  - emitted when a spawn request is submitted to the control queue

- `agent.spawn.received`
  - emitted when `handleSpawn()` receives the request

- `agent.spawn.rejected`
  - emitted when spawn is rejected before process start
  - examples:
    - registry lookup failed
    - env build failed
    - no supervisor available
    - already managed locally

- `agent.spawn.accepted`
  - emitted when the supervisor accepts responsibility for launch and begins
    process startup

- `agent.spawn.skipped_existing_remote`
  - emitted when idempotency logic observes that another node already has the
    agent in `starting` or `running`

### Agent process lifecycle events

- `agent.process.starting`
  - emitted immediately before `cmd.Start()`

- `agent.process.started`
  - emitted after successful `cmd.Start()`
  - should include PID and runtime

- `agent.process.start_failed`
  - emitted if workdir setup, transport setup, bridge creation, or `cmd.Start()`
    fails

- `agent.process.exited`
  - emitted whenever the process exits
  - should include exit code and whether stop was requested

- `agent.process.restarting`
  - emitted when Forge schedules a restart after an unexpected exit
  - should include restart attempt and next delay

- `agent.process.restart_failed`
  - emitted when a restart attempt cannot be launched

- `agent.process.failed`
  - emitted when Forge gives up after retry exhaustion or unrecoverable launch
    failure

- `agent.process.stopped`
  - emitted when an intentional stop completes

### Optional higher-level runtime events

These are useful, but can be deferred:

- `agent.runtime.heartbeat_warning`
- `agent.runtime.heartbeat_error`
- `guild.runtime.health_report_published`

These should only be added if the UI genuinely needs them. The first rollout
should focus on launch and process lifecycle.

## Event Sources in Current Code

This section maps proposed events to concrete source points in the current code.

### API and bootstrap

`forge-go/api/guild.go`

- `HandleCreateGuild()`
  - emits `guild.launch.requested`

`forge-go/guild/bootstrap.go`

- before `db.CreateGuildWithAgents(...)`
  - optional `guild.launch.persisting`
- after `db.CreateGuildWithAgents(...)`
  - `guild.launch.persisted`
- before `EnqueueGuildManagerSpawn(...)`
  - `guild.launch.enqueue_requested`
- after successful enqueue
  - `guild.launch.enqueued`
- on enqueue failure
  - `guild.launch.enqueue_failed`

### Control handler

`forge-go/control/handler.go`

- entry to `handleSpawn()`
  - `agent.spawn.received`

- cross-node idempotency short-circuit
  - `agent.spawn.skipped_existing_remote`

- registry/env/supervisor rejection paths
  - `agent.spawn.rejected`

- immediately before `sup.Launch(...)`
  - `agent.spawn.accepted`

- after successful `sup.Launch(...)`
  - optional `agent.spawn.completed`
  - this is distinct from process success only if different supervisors later
    need different semantics

### Dispatching supervisor

`forge-go/supervisor/dispatcher.go`

- after runtime selection
  - `agent.spawn.supervisor_selected`
  - optional event, useful if runtime diversity matters in the UI

This event is probably useful in logs, but optional in the first event stream.

### Process supervisor

`forge-go/supervisor/process.go`

- start of `startProcess()`
  - `agent.process.starting`

- bridge setup failure
  - `agent.process.start_failed`

- `cmd.Start()` success
  - `agent.process.started`

- `cmd.Wait()` return in `monitorProcess()`
  - `agent.process.exited`

- transition to restart/backoff
  - `agent.process.restarting`

- exhausted retries
  - `agent.process.failed`

- explicit stop path
  - `agent.process.stopped`

### Python agent runner

`forge-python/src/rustic_ai/forge/agent_runner.py`

The Go supervisor already observes process start and exit. The Python runner does
not need to emit launch events for the first version.

That said, if we later need finer-grained startup detail inside the spawned
process, the Python runner is the right place for optional events such as:

- `agent.runtime.bootstrap_started`
- `agent.runtime.guild_spec_loaded`
- `agent.runtime.agent_spec_loaded`
- `agent.runtime.wrapper_started`
- `agent.runtime.bootstrap_failed`

These are valuable for deep debugging, but not required for the core UI problem.

### Guild manager health/status

`forge-python/src/rustic_ai/forge/agents/system/guild_manager_agent.py`

Current `guild_status_topic` traffic should remain separate from infrastructure
events. It can continue to power higher-level guild health and readiness views.

## WebSocket Delivery

There are two reasonable options.

### Option A: extend `syscomms`

Subscribe `syscomms` to `guild_infra_events` in addition to:

- `user_system_notification:{user_id}`
- `guild_status_topic`

Pros:

- minimal API surface change
- fits current operator/runtime role of `syscomms`
- local UI bootstrap already exposes `/rustic/ws/:ws_id/syscomms`

Cons:

- `syscomms` becomes a mixed stream:
  - user-specific system notifications
  - guild manager health/status
  - Forge infrastructure events
- clients must filter more aggressively

### Option B: add dedicated WS

Add:

- public: `/ws/guilds/:id/infra/:user_id`
- local UI bootstrap: `/rustic/ws/:ws_id/infra`

This socket would subscribe only to:

- `guild_infra_events`

Pros:

- clean separation of concerns
- simplest client semantics
- easier to evolve independently

Cons:

- slightly larger API surface
- more sockets in the UI

### Recommendation

Use a dedicated infra socket if the UI is expected to display:

- launch timelines
- retries
- placement/runtime metadata
- failure diagnostics

Reuse `syscomms` only if the immediate goal is incremental adoption with minimal
backend work.

Pragmatic rollout path:

1. Publish infra events to the new topic
2. Also subscribe `syscomms` to that topic temporarily
3. Add dedicated `/infra` WS once the event model stabilizes
4. Remove mixed-stream dependency from the UI later if desired

## WS Message Shape

### Recommendation

Reuse the normal `protocol.Message` envelope over WS.

Reasons:

- current WS handlers already emit `protocol.Message`
- proxy compatibility logic already expects message envelopes
- no separate browser transport model is needed

Recommended values:

- `topics`: `["guild_infra_events"]`
- `format`: `rustic_ai.forge.runtime.InfraEvent`
- `payload`: infra event JSON

### Proxy compatibility

If the UI is using proxy-compat shaping, add a format alias only if needed for
frontend ergonomics. Do not weaken the canonical backend format for proxy
compatibility alone.

## Ordering and Delivery Semantics

### Recommendation

Treat the stream as:

- ordered per topic as provided by the current backend
- best-effort live delivery
- durable enough for recent history if the backend supports it

The UI should not assume exactly-once delivery.

It should deduplicate by `event_id`.

### History

Because the messaging backends already support topic history reads, the UI should
eventually be able to fetch recent infra history from:

- `guild_infra_events`

This is especially helpful when the WS connects after launch began.

## Scope Boundaries

### What belongs on `guild_infra_events`

- control-plane request acceptance
- queue dispatch
- supervisor selection
- process startup
- process exit/restart/failure
- explicit stop lifecycle
- backend/runtime metadata needed for operators and UI diagnostics

### What does not belong there

- user chat traffic
- agent output messages
- normal routing/business events
- arbitrary log lines
- high-volume heartbeat spam unless the UI truly needs it

## Relationship to Existing Status Stores

The new event stream should not replace status storage.

Recommended model:

- status rows remain the compact latest-state index
- infra events become the detailed timeline and operator UX surface

In other words:

- use status for "what is the current state?"
- use infra events for "what happened and why?"

## Suggested Initial Event Set

A narrow first rollout should include only:

- `guild.launch.requested`
- `guild.launch.persisted`
- `guild.launch.enqueued`
- `guild.launch.enqueue_failed`
- `agent.spawn.received`
- `agent.spawn.rejected`
- `agent.spawn.skipped_existing_remote`
- `agent.process.starting`
- `agent.process.started`
- `agent.process.start_failed`
- `agent.process.exited`
- `agent.process.restarting`
- `agent.process.failed`
- `agent.process.stopped`

This is enough to solve the current launch visibility gap.

## Implementation Notes

### Producer placement

Add a small Forge-Go publisher abstraction near the runtime/control layers rather
than publishing raw ad hoc `protocol.Message` values inline everywhere.

That helper should:

- build the standard infra event payload
- generate `event_id`
- attach common fields
- publish to `guild_infra_events`

### Source attribution

Use stable source component names such as:

- `forge-go.api`
- `forge-go.guild-bootstrap`
- `forge-go.control-handler`
- `forge-go.supervisor.dispatcher`
- `forge-go.supervisor.process`
- `forge-python.guild-manager`
- `forge-python.agent-runner`

### Error details

Include concise structured error details.

Do not dump stack traces or giant stderr payloads into the event body.

If needed, include:

- short message
- machine-readable reason
- runtime/supervisor type
- exit code
- retry attempt

## Open Questions

1. Should infra events be persisted longer than normal topic history?
2. Should the UI expose infra events only to privileged users?
3. Do we want a REST endpoint for recent infra history in addition to WS?
4. Should guild manager health/status eventually emit a normalized event into
   `guild_infra_events`, or remain solely on `guild_status_topic`?
5. Should restart-loop throttling produce a dedicated event kind such as
   `agent.process.backoff_scheduled`?

## Test Plan

The infra event stream needs tests at four levels:

- unit tests for event building and publishing
- component tests for control/supervisor emit points
- API and WS integration tests for browser-visible behavior
- failure-path tests that prove the UI can see launch failures that are currently
  hidden behind control responses

The most important assertion pattern is not only "an event was emitted", but:

- the right event kind was emitted
- the event order is correct
- the required correlation fields are present
- no unrelated topics receive the event
- duplicate or misleading events are not emitted on failure paths

### Core assertions for all event tests

Every emitted infra event should be checked for:

- `format == rustic_ai.forge.runtime.InfraEvent`
- `topics` contains `guild_infra_events`
- `payload.schema_version == 1`
- non-empty `payload.event_id`
- correct `payload.kind`
- correct `payload.guild_id`
- correct `payload.agent_id` when applicable
- correct `payload.request_id` when applicable
- correct `payload.source.component`
- non-empty `payload.timestamp`

### Unit tests

These should live near the event publisher/helper once introduced.

#### Event builder

- builds canonical payload with required fields
- includes optional fields only when provided
- preserves structured `detail` payload
- generates unique `event_id` values
- maps errors to `severity=error`
- serializes timestamps in a stable RFC3339 format

#### Topic and format constants

- infra publisher always targets `guild_infra_events`
- infra publisher always uses `rustic_ai.forge.runtime.InfraEvent`
- existing `guild_status_topic` constants remain unchanged

#### WS shaping

- canonical WS path forwards infra events unchanged
- proxy-compat shaping does not corrupt `format`, `topic`, or payload
- proxy-compat clients can still distinguish infra events from health/status

### Happy path integration tests

#### Guild create -> manager launch success

Start from the public HTTP guild create path and assert the event sequence:

- `guild.launch.requested`
- `guild.launch.persisted`
- `guild.launch.enqueue_requested`
- `guild.launch.enqueued`
- `agent.spawn.received`
- `agent.process.starting`
- `agent.process.started`

Assertions:

- HTTP create returns success before or independent of eventual WS delivery
- infra events appear on `guild_infra_events`
- if `syscomms` is temporarily reused, the same events arrive over that WS
- `guild_status_topic` traffic is still health/status-oriented and not replaced

#### Relaunch happy path

Via `HandleRelaunchGuild()`:

- records relaunch request
- emits relaunch-related launch events for the manager agent
- does not emit duplicate `guild.launch.persisted` if no new persistence occurs

#### Additional agent spawn success

When the guild manager launches a regular guild agent:

- `agent.spawn.dispatched` or equivalent handoff event
- `agent.spawn.received`
- `agent.process.starting`
- `agent.process.started`

Assertions:

- `agent_id` refers to the child agent, not the manager agent
- `request_id` links back to the launch request if one exists

### Control-handler failure tests

These are the highest-value tests because they close today’s visibility gap.

#### Registry lookup failure

Simulate unknown `ClassName` in `handleSpawn()`.

Expected events:

- `agent.spawn.received`
- `agent.spawn.rejected`

Assertions:

- rejection detail includes reason category like `registry_lookup_failed`
- no `agent.process.starting` event is emitted
- if HTTP/WS infra streaming is wired, the failure is visible there even though
  no process ever starts

#### Environment build failure

Make `envvars.BuildAgentEnv(...)` fail.

Expected events:

- `agent.spawn.received`
- `agent.spawn.rejected`

Assertions:

- detail includes `env_build_failed`
- no process lifecycle events are emitted afterward

#### No supervisor available

Force `supervisorForOrganization()` to return `nil`.

Expected events:

- `agent.spawn.received`
- `agent.spawn.rejected`

Assertions:

- detail indicates `no_supervisor_available`

#### Already managed locally

Spawn the same agent twice on the same node.

Expected events for the second request:

- `agent.spawn.received`
- `agent.spawn.rejected`

Assertions:

- reason indicates duplicate local management
- no second `agent.process.started`
- first process continues uninterrupted

#### Idempotent remote skip

Populate `statusStore` with remote `starting` or `running`.

Expected events:

- `agent.spawn.received`
- `agent.spawn.skipped_existing_remote`

Assertions:

- no local process start attempt
- event includes remote node identity when available

### Process supervisor failure tests

#### Workdir preparation failure

Make `ensureAgentWorkDir()` fail.

Expected events:

- `agent.spawn.received`
- `agent.process.starting`
- `agent.process.start_failed`
- `agent.process.failed`

Open question for implementation:

- whether `agent.process.starting` should be emitted before or after workdir
  preparation; tests should lock the decision once chosen

#### Supervisor-ZMQ bridge creation failure

Force `NewAgentMessagingBridge(...)` to fail.

Expected events:

- `agent.process.starting`
- `agent.process.start_failed`
- `agent.process.failed`

Assertions:

- detail indicates transport/bridge setup failure

#### `cmd.Start()` failure

Use an invalid runtime command.

Expected events:

- `agent.process.starting`
- `agent.process.start_failed`
- `agent.process.failed`

Assertions:

- no `agent.process.started`
- error detail includes OS/process start failure text

### Post-start crash and recovery tests

#### Immediate crash with restart

Spawn an agent that exits non-zero immediately.

Expected events:

- `agent.process.started`
- `agent.process.exited`
- `agent.process.restarting`
- `agent.process.started` again after retry

Assertions:

- restart attempt increments
- delay/backoff fields are present if included in payload

#### Crash loop until exhausted

Spawn an agent that always exits quickly.

Expected events:

- repeated `agent.process.exited`
- repeated `agent.process.restarting`
- final `agent.process.failed`

Assertions:

- retry count stops at `MaxRetries`
- final failure event appears exactly once
- no further restarts after failure

#### Stable run resets restart budget

Spawn an agent that survives past `StableTime`, then crashes.

Expected behavior:

- subsequent restart attempt behaves as a fresh cycle

Assertions:

- retry/backoff state resets after the stable period

### Stop-path tests

#### Explicit stop

Call stop on a running agent.

Expected events:

- `agent.process.stopped`

Assertions:

- no `agent.process.restarting`
- status entry is removed

#### Stop during backoff window

Crash agent, let restart be scheduled, then stop before retry fires.

Expected events:

- `agent.process.exited`
- `agent.process.restarting`
- `agent.process.stopped`

Assertions:

- no new `agent.process.started` after stop

### WS delivery tests

#### `syscomms` subscription receives infra events

If rollout starts by extending `syscomms`:

- connect `syscomms`
- trigger launch
- assert infra events are delivered over WS

Assertions:

- events preserve order
- existing `user_system_notification:{user}` behavior still works
- existing `guild_status_topic` passthrough still works

#### Dedicated infra WS receives only infra events

If a new `/infra` socket is added:

- connect infra WS
- publish infra and non-infra messages

Assertions:

- infra socket receives only `guild_infra_events`
- `usercomms` does not receive infra events
- `syscomms` behavior is unchanged unless explicitly configured

#### Late subscriber history

If history fetch is supported:

- emit infra events before WS connect
- connect and fetch recent history

Assertions:

- recent events are available in order
- duplicate live+history events can be deduplicated by `event_id`

### Negative isolation tests

These are important to prevent topic pollution.

#### Infra events do not appear on user topics

Assert no infra events are published to:

- `user:{id}`
- `user_notifications:{id}`
- `user_system:{id}`

#### Business messages do not appear on infra topic

Publish normal guild/user traffic and assert it is absent from
`guild_infra_events`.

#### Health/status messages stay distinct

Publish `AgentsHealthReport` or `HealthCheckRequest` traffic and assert:

- they remain on `guild_status_topic` or heartbeat topics
- they are not silently rewritten into infra events unless explicitly designed

### Schema compatibility tests

#### Required field stability

Lock the schema with golden tests so clients can rely on:

- `kind`
- `severity`
- `timestamp`
- `guild_id`
- `agent_id`
- `request_id`
- `source.component`
- `message`

#### Forward-compatibility

Clients should ignore unknown fields in payload.

Test:

- add an extra field in a fixture
- ensure decode paths used by WS or REST history consumers still succeed

### Recommended initial test matrix

The minimum high-value suite should include:

1. guild create happy path emits launch and process-start events
2. control handler registry lookup failure emits visible rejection event
3. process start failure emits `start_failed` and final `failed`
4. crash loop emits restart events and terminal failure
5. explicit stop emits `stopped` without restart
6. `syscomms` or infra WS delivers infra events to browser clients
7. non-infra topics remain clean

If those seven are solid, the feature will have good behavioral coverage from
both backend and UI perspectives.

## Recommendation

Proceed with a dedicated topic:

- `guild_infra_events`

and a dedicated canonical format:

- `rustic_ai.forge.runtime.InfraEvent`

Short term:

- publish the new events from the Forge Go control/supervisor path
- expose them on `syscomms` for fast UI adoption

Medium term:

- add a dedicated infra WS
- keep `guild_status_topic` for guild-manager health/status semantics

This gives Forge a clean, explicit runtime event model and closes the current gap
between control-plane launch failures and what HTTP/WS clients can observe.
