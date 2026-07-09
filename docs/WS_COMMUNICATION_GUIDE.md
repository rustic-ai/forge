# Forge WebSocket Communication Guide

This document explains the current WebSocket behavior in Forge from the point of view of a client or UI.

The goal is to make the runtime behavior understandable without requiring the client to read Go and Python internals. It focuses on:

- which WebSocket endpoints exist
- which messages flow over each socket
- how to understand status, progress, and failure
- how infrastructure lifecycle events differ from guild/business messages
- what a client should render or ignore

## 1. High-level model

Forge exposes two different WebSocket channels per guild:

- `usercomms`
  Used for user-to-guild conversational and business messages.

- `syscomms`
  Used for system-facing traffic relevant to the current user session, guild status traffic, and guild infrastructure lifecycle events.

These channels are intentionally different.

`usercomms` is where a browser or CLI sends user requests and receives agent responses.

`syscomms` is where a browser or CLI should look for:

- system notifications targeted at the current user
- guild health/status traffic
- launch/progress/failure events emitted by Forge runtime infrastructure

## 2. Endpoints

### Public canonical endpoints

These are the public routes registered by the main API server:

- `GET /ws/guilds/:id/usercomms/:user_id/:user_name`
- `GET /ws/guilds/:id/syscomms/:user_id`

These endpoints use the canonical Forge `protocol.Message` wire shape.

### Local UI proxy-compatible endpoints

The local Rustic UI exposes proxy-compatible routes:

- `GET /rustic/ws/:ws_id/usercomms`
- `GET /rustic/ws/:ws_id/syscomms`

These use a compatibility shaping layer that accepts and emits a more UI-oriented JSON shape. Internally they still map onto the same backend topics and message flow.

If you are writing a new external client, prefer the canonical public endpoints unless you explicitly need parity with the local Rustic UI transport.

## 3. Namespaces and topics

Forge messaging is guild-scoped. A WebSocket connected to guild `g1` only sees traffic within namespace `g1`.

The important topics for WebSocket clients are:

- `user:<user_id>`
  Inbound user requests from `usercomms`

- `user_notifications:<user_id>`
  Outbound user-facing responses delivered to `usercomms`

- `user_system:<user_id>`
  Inbound system requests from `syscomms`

- `user_system_notification:<user_id>`
  Outbound user-targeted system notifications delivered to `syscomms`

- `guild_status_topic`
  Guild-manager health/status traffic delivered to `syscomms`

- `infra_events_topic`
  Forge runtime lifecycle events delivered to `syscomms`

The split matters:

- `usercomms` is about guild interaction
- `syscomms` is about runtime/system state

## 4. Canonical WebSocket message shape

The public WebSocket routes send and receive the canonical `protocol.Message` JSON shape.

At a minimum, clients should expect fields like:

```json
{
  "id": 9650997620256485376,
  "sender": {
    "id": "echo-agent",
    "name": "Echo Agent"
  },
  "topics": ["default_topic"],
  "payload": {
    "message": "hello"
  },
  "format": "some.qualified.Format",
  "thread": [9650997620256485376],
  "message_history": [],
  "traceparent": "00-...",
  "topic_published_to": "user_notifications:u1"
}
```

The client should treat the following fields as the primary routing metadata:

- `format`
  The semantic type of the message. This is the most important field for rendering.

- `payload`
  The actual message body.

- `sender`
  Who produced the message.

- `topic_published_to`
  Which topic the message was actually delivered on. Useful for debugging and telemetry.

- `thread`
  Conversation lineage.

- `message_history`
  Process lineage added by Forge/guild execution.

## 5. Proxy-compatible shape

The local UI compatibility layer accepts alternate field names such as:

- `data` instead of `payload`
- `topic` instead of `topics`
- `threads` instead of `thread`
- `messageHistory` instead of `message_history`
- `recipientList` instead of `recipient_list`
- `conversationId` instead of `conversation_id`
- `inReplyTo` instead of `in_response_to`

It also aliases some short format names like:

- `healthcheck`
- `questionResponse`
- `formResponse`
- `participantsRequest`
- `chatCompletionRequest`
- `stopGuildRequest`

For new clients, this compatibility mode should be treated as legacy/UI-specific rather than the primary contract.

## 6. `usercomms` behavior

### What `usercomms` subscribes to

`usercomms` subscribes to:

- `user_notifications:<user_id>`

That means the socket only receives outbound user-visible responses and notifications for that user.

### What the client sends on `usercomms`

A client sends an application message. Forge wraps it into a canonical `protocol.Message` and publishes it to:

- `user:<user_id>`

The wrapped message uses:

- `sender.id = user_socket:<user_id>`
- `sender.name = <user_name>`
- `format = rustic_ai.core.messaging.core.message.Message`
- `payload = normalized user envelope`

The normalized inner payload preserves user-supplied fields like:

- `topics`
- `payload`
- `format`
- `recipient_list`
- `thread`
- `message_history`
- `in_response_to`
- `conversation_id`

### Important client rule

`usercomms` is not the right place to look for launch progress or runtime failures. Even if an agent eventually surfaces an error as a business message, infrastructure state belongs on `syscomms`.

## 7. `syscomms` behavior

### What `syscomms` subscribes to

`syscomms` subscribes to three topic families:

- `user_system_notification:<user_id>`
- `guild_status_topic`
- `infra_events_topic`

This means a single `syscomms` socket carries three categories of outbound messages:

1. direct system notifications for the user
2. guild manager health/status messages
3. Forge runtime lifecycle messages

### What the client sends on `syscomms`

The client can send system-oriented messages with:

- `format`
- `payload`

Forge wraps them and publishes them to:

- `user_system:<user_id>`

Important behavior:

- inbound syscomms messages missing either `format` or `payload` are dropped
- the server resets the thread to `[current_message_id]`
- the server injects its own trace context
- the sender becomes `sys_comms_socket:<user_id>`

### Automatic health check on connect

When a `syscomms` socket connects, Forge immediately publishes a `HealthCheckRequest` to `guild_status_topic`.

This is an internal kick to prompt guild-manager health/status reporting. A client should not interpret the connection itself as proof that the guild is healthy. Wait for actual outbound messages on `guild_status_topic` and `infra_events_topic`.

## 8. How to think about status, progress, and failure

A client should not treat HTTP `201 Created` from guild creation as "guild is running".

Guild launch is asynchronous.

The correct model is:

1. HTTP create/relaunch says the launch request was accepted.
2. `syscomms` shows runtime progress over time.
3. `infra_events_topic` explains what Forge runtime is doing.
4. `guild_status_topic` reflects guild-manager health/status once the manager is alive enough to emit it.

This gives you two complementary views:

- infrastructure lifecycle view: `infra_events_topic`
- guild health/application view: `guild_status_topic`

## 9. Infrastructure events on `infra_events_topic`

Forge now emits structured runtime events with:

- topic: `infra_events_topic`
- format: `rustic_ai.forge.runtime.InfraEvent`

### Event payload shape

The `payload` of a canonical `protocol.Message` on `infra_events_topic` looks like:

```json
{
  "schema_version": 1,
  "event_id": "a1b2c3d4",
  "kind": "agent.process.started",
  "severity": "info",
  "timestamp": "2026-03-25T23:31:08.754Z",
  "guild_id": "guild-dist-docker",
  "agent_id": "echo-agent",
  "organization_id": "e2e-org",
  "request_id": "66936059-f857-47bd-b95f-bee0896796d9",
  "node_id": "local-node",
  "source": {
    "component": "forge-go.supervisor.process"
  },
  "attempt": 1,
  "message": "agent process started",
  "detail": {
    "pid": 12345
  }
}
```

### Severity

Current severities are:

- `info`
- `warning`
- `error`

### Current event kinds

Guild launch events:

- `guild.launch.requested`
- `guild.launch.persisted`
- `guild.launch.enqueue_requested`
- `guild.launch.enqueued`
- `guild.launch.enqueue_failed`

Spawn handling events:

- `agent.spawn.received`
- `agent.spawn.rejected`
- `agent.spawn.skipped_existing_remote`

Process lifecycle events:

- `agent.process.starting`
- `agent.process.started`
- `agent.process.start_failed`
- `agent.process.exited`
- `agent.process.restarting`
- `agent.process.failed`
- `agent.process.stopped`

### How clients should use them

Treat infra events as the primary source of progress and failure for launch/runtime operations.

A good UI model is:

- show a timeline from `kind`, `timestamp`, and `message`
- show current phase derived from the most recent event
- highlight `severity = error`
- show retry count from `attempt` when present
- show process details like `pid`, exit code, or error text from `detail`

### Practical interpretation examples

Healthy launch:

1. `guild.launch.requested`
2. `guild.launch.persisted`
3. `guild.launch.enqueue_requested`
4. `guild.launch.enqueued`
5. `agent.spawn.received`
6. `agent.process.starting`
7. `agent.process.started`

Pre-launch rejection:

1. `guild.launch.enqueued`
2. `agent.spawn.received`
3. `agent.spawn.rejected`

Crash with retries:

1. `agent.process.started`
2. `agent.process.exited`
3. `agent.process.restarting`
4. `agent.process.started`
5. repeat
6. `agent.process.failed`

Explicit stop:

1. `agent.process.started`
2. `agent.process.stopped`

## 10. Guild status traffic on `guild_status_topic`

`guild_status_topic` is not the same thing as infrastructure lifecycle.

It is the guild-manager health/status lane.

Typical formats seen here include:

- `rustic_ai.core.guild.agent_ext.mixins.health.HealthCheckRequest`
- `rustic_ai.core.guild.agent_ext.mixins.health.AgentsHealthReport`

### Recommended client interpretation

Use `guild_status_topic` to answer questions like:

- Is the guild manager alive enough to respond?
- What does the manager say about agent health?
- Has the application-level guild reached a healthy state?

Do not use it as the only source of launch truth, because infrastructure failures can happen before the manager is alive enough to publish anything useful.

That is exactly why `infra_events_topic` exists.

## 11. Direct system notifications on `user_system_notification:<user_id>`

These are user-targeted system messages delivered over `syscomms`.

They are not necessarily lifecycle events and should not be interpreted as such unless their `format` or `payload` explicitly says so.

A client should treat them as a separate rendering lane from infra events.

## 12. Recommended client state model

A robust client should keep separate derived state for:

- conversation state from `usercomms`
- runtime lifecycle state from `infra_events_topic`
- health/state summary from `guild_status_topic`
- user-targeted system notifications from `user_system_notification:<user_id>`

One practical model is:

- `conversationTimeline`
  Messages from `usercomms`

- `runtimeTimeline`
  `InfraEvent` payloads from `syscomms`

- `guildHealth`
  Latest health/status payload from `guild_status_topic`

- `systemNotifications`
  Everything from `user_system_notification:<user_id>`

## 13. Recommended rendering rules

### For progress

Drive progress indicators from the latest infra event:

- `guild.launch.*` means launch is being orchestrated
- `agent.process.starting` means the process launch has begun
- `agent.process.restarting` means the process is in retry/backoff
- `agent.process.started` means process startup succeeded

### For failure

Show launch/runtime failure when you see:

- `guild.launch.enqueue_failed`
- `agent.spawn.rejected`
- `agent.process.start_failed`
- `agent.process.failed`

Use:

- `severity`
- `message`
- `detail.error`
- `detail.exit_code`
- `attempt`

to build a human-readable error summary.

### For health

Use `AgentsHealthReport` or other guild status messages to represent the current guild/application health, not launch orchestration.

## 14. Recommended reconnection behavior

WebSockets are live subscriptions, not durable progress replay.

After reconnect, a client should:

1. reconnect `usercomms` and/or `syscomms`
2. wait for fresh messages
3. refresh guild metadata over HTTP if it needs a current summary
4. treat new infra events as authoritative going forward

If a client needs durable historical timelines, that requires a separate history or persistence layer. The WebSocket by itself should be treated as a live stream.

## 15. Minimal canonical examples

### Sending a user message on `usercomms`

Client sends:

```json
{
  "format": "my.app.UserPrompt",
  "topics": ["default_topic"],
  "payload": {
    "text": "hello"
  }
}
```

Forge wraps and publishes it internally to `user:<user_id>`.

### Sending a system message on `syscomms`

Client sends:

```json
{
  "format": "my.app.ControlAction",
  "payload": {
    "action": "refresh"
  }
}
```

Forge wraps and publishes it internally to `user_system:<user_id>`.

### Receiving an infra event on `syscomms`

Client receives canonical `protocol.Message`:

```json
{
  "id": 9650997620256485376,
  "sender": {
    "id": "forge-go.supervisor.process"
  },
  "topics": ["infra_events_topic"],
  "format": "rustic_ai.forge.runtime.InfraEvent",
  "payload": {
    "schema_version": 1,
    "event_id": "abc123",
    "kind": "agent.process.failed",
    "severity": "error",
    "timestamp": "2026-03-25T23:31:55.000Z",
    "guild_id": "test-guild-bwrap",
    "agent_id": "echo-agent-bwrap",
    "message": "agent process failed after retry exhaustion",
    "detail": {
      "error": "Read-only file system"
    }
  },
  "topic_published_to": "infra_events_topic"
}
```

## 16. What a new client should do

If you are building a CLI or browser UI, the recommended baseline is:

1. Open `usercomms` for conversational traffic.
2. Open `syscomms` for runtime/system traffic.
3. Route by `format`.
4. Treat `rustic_ai.forge.runtime.InfraEvent` as the source of progress/failure.
5. Treat `HealthCheckRequest` and `AgentsHealthReport` as guild health traffic, not launch orchestration.
6. Do not equate HTTP create/relaunch success with runtime success.

## 17. Current limitations

Some important constraints in the current design:

- `syscomms` multiplexes three different streams on one socket.
- The client must inspect `format` and sometimes `topic_published_to` to distinguish them.
- WebSocket delivery is live-stream oriented, not durable replay.
- Launch success/failure is asynchronous; the socket view is more accurate than the immediate HTTP response.

## 18. Summary

The short version is:

- use `usercomms` for normal guild interaction
- use `syscomms` for status, progress, and failures
- use `infra_events_topic` for runtime lifecycle
- use `guild_status_topic` for guild-manager health/status
- treat the WebSocket as the live truth for launch progress after HTTP accept/queueing

