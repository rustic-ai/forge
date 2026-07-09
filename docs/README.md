# Forge Design Docs

Internal engineering design documents, specifications, and runbooks for Forge. These are the raw source-of-truth design records — for the polished, published documentation site see [`website/`](../website/) (Zensical docs at `website/docs/`).

## Architecture & Operations

| Document | What it covers |
|---|---|
| [Distributed Architecture & SRE Guide](distributed-architecture.md) | The distributed system design: control plane, message broker, worker nodes, happy paths, failure/recovery scenarios, and observability. |
| [Local Debug Guide](LOCAL_DEBUG.md) | End-to-end local debug runbook for the single `forge` binary. |

## Design Specs

| Document | What it covers |
|---|---|
| [Agent Needs & Dependency Needs Design](AGENT_NEEDS_DESIGN.md) | How agents declare needs/dependencies and how they are resolved and injected. |
| [Guild Infrastructure Events Proposal](GUILD_INFRA_EVENTS_PROPOSAL.md) | Infrastructure event model for guild lifecycle and control-plane signalling. |
| [File API Guild Spec Flow](FILE_API_GUILD_SPEC_FLOW.md) | The file API and the spec-to-running-guild flow. |
| [Desktop Secrets & OAuth Design](DESKTOP_SECRETS_OAUTH_DESIGN.md) | Secret storage/injection, keychain integration, and desktop OAuth flows. |
| [Local Model Fit Design](MODEL_FIT_DESIGN.md) | Local model-fit recommendations and runtime capability detection. |

## Protocols

| Document | What it covers |
|---|---|
| [WebSocket Communication Guide](WS_COMMUNICATION_GUIDE.md) | The gateway WebSocket protocol, message envelope, and connection lifecycle. |
