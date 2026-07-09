# Agent Needs and Dependency Needs Design

Status: Proposed  
Audience: Forge, Rustic UI, and platform reviewers  
Last updated: 2026-03-27  
Primary scope: `rustic-go` and `rustic-ui`, with canonical behavior references in `rustic-ai`

## 1. Executive Summary

This document proposes a first-class requirements model for agents and dependency
providers without adding setup metadata to `AgentSpec`.

The core recommendation is:

- keep `AgentSpec` as the guild/runtime configuration object
- introduce `AgentNeeds` keyed by agent `class_name`
- introduce `DependencyNeeds` keyed by dependency provider `class_name`
- compute effective needs by combining agent-class needs and resolved
  dependency-provider needs
- expose those effective needs to the UI with satisfaction state and required
  user actions

This design supports:

- showing users what an agent can access before launch
- prompting for missing secrets
- prompting for OAuth connection when needed
- prompting for capability approval such as network or filesystem access
- avoiding provider-specific secret names in generic agent specs

## 2. Goals

- Let the UI show agent setup requirements before launch.
- Keep `AgentSpec` free of setup-only requirements.
- Support agent-class requirements and dependency-provider requirements.
- Support provider-specific needs such as OpenAI vs Gemini keys.
- Support user-facing actions for:
  - add secret
  - connect OAuth account
  - approve filesystem access
  - approve network access
- Support merging and deduplicating needs across a guild.
- Preserve existing launch-time secret injection and capability enforcement flows.

## 3. Non-Goals

- This document does not redesign `AgentSpec` as a resolved plan object.
- This document does not store raw secret values in guild specs.
- This document does not define full cloud tenancy and RBAC behavior.
- This document does not replace the existing registry runtime model.
- This document does not define every OAuth provider adapter in detail.

## 4. Problem Statement

Forge currently has pieces of the requirement model spread across different
surfaces:

- `AgentSpec.Resources.Secrets`
- `AgentSpec.DependencyMap`
- registry entry fields such as `Secrets`, `Network`, and `Filesystem`
- external secret providers and injected environment variables

This is not a good user-facing setup model because:

- `AgentSpec` is a runtime launch object, not a setup contract
- provider-specific requirements are not tied cleanly to the selected dependency
  implementation
- the UI does not have a single object describing missing vs satisfied setup
  needs
- secrets, OAuth login, network access, and filesystem grants are conceptually
  different but are not modeled consistently

Example:

- `LLMAgent` depends on an abstract `llm` dependency
- if `llm` is satisfied by `OpenAILLM`, the effective setup needs are:
  - `OPENAI_API_KEY`
  - network access to `api.openai.com`
- if `llm` is satisfied by `GeminiLLM`, the effective setup needs are:
  - `GEMINI_API_KEY`
  - network access to Google AI endpoints

Those needs belong to the selected dependency provider, not to the generic
`LLMAgent` runtime spec.

## 5. Design Principles

- Keep runtime configuration separate from setup requirements.
- Express setup requirements declaratively and by class identity.
- Make dependency-provider requirements composable.
- Make effective needs deterministic from guild configuration plus dependency
  selection.
- Never store raw secret values in needs metadata.
- Let the UI render needs and actions without inferring from env vars.
- Preserve least privilege by showing and enforcing concrete capabilities.

## 6. Proposed Model

### 6.1 AgentSpec remains runtime-focused

`AgentSpec` remains part of `GuildSpec` and continues to describe:

- class name
- properties
- dependency bindings
- resources used for scheduling/runtime
- topics, predicates, and QoS

`AgentSpec` should not become the primary place for:

- secret declarations
- OAuth requirements
- user-facing capability prompts

### 6.2 AgentNeeds

Introduce a catalog object keyed by agent `class_name`.

`AgentNeeds` describes setup and capability needs inherent to the agent class
itself, independent of which guild is using it.

Examples:

- Browser agent needs browser capability and external network access
- DB agent needs filesystem access to a chosen project path
- Cache agent may require nothing external

### 6.3 DependencyNeeds

Introduce a catalog object keyed by dependency provider `class_name`.

`DependencyNeeds` describes setup and capability needs contributed by a specific
dependency implementation.

Examples:

- `OpenAILLM` needs `OPENAI_API_KEY` and `api.openai.com`
- `GeminiLLM` needs `GEMINI_API_KEY` and Google endpoints
- `GithubOAuthClient` needs GitHub OAuth scopes and a connected account

### 6.4 EffectiveNeeds

For a given guild launch, Forge computes:

`EffectiveNeeds = AgentNeeds(agent.class_name) + DependencyNeeds(each resolved dependency provider class_name)`

This merged object is what the UI and preflight checks consume.

## 7. Needs Schema

### 7.1 Common shape

Each `Needs` object should support the following categories:

- `secrets`
- `oauth`
- `capabilities`
- `network`
- `filesystem`

Compute resources such as CPU and GPU remain part of `resources`, not setup
needs.

### 7.2 Secrets

Secrets are references by key name only.

Example:

```yaml
secrets:
  - key: OPENAI_API_KEY
    label: OpenAI API Key
    required: true
```

Semantics:

- `key` is the lookup key in the secret store
- `label` is the UI-facing label

### 7.3 OAuth

OAuth needs represent connected-account requirements, not raw token material.

Example:

```yaml
oauth:
  - provider: google
    label: Google Account
    scopes:
      - gmail.readonly
```

Semantics:

- if the account is not connected, the UI shows `Connect`
- if connected but missing scopes, the UI shows `Reconnect`

### 7.4 Capabilities

Capabilities are coarse approvals that the user can understand.

Example:

```yaml
capabilities:
  - type: network
    label: External network access
  - type: filesystem
    label: Local filesystem access
```

These are the user-facing capability summary. Fine-grained constraints live
under `network` and `filesystem`.

### 7.5 Network

Network needs describe concrete host-level access.

Example:

```yaml
network:
  allow:
    - api.openai.com
    - generativelanguage.googleapis.com
```

### 7.6 Filesystem

Filesystem needs describe concrete paths and modes.

Example:

```yaml
filesystem:
  allow:
    - path: /home/user/code/project
      mode: rw
```

## 8. Data Model

### 8.1 Agent needs catalog

Recommended shape:

```yaml
agent_needs:
  - class_name: rustic_ai.browser.agent.BrowserAgent
    needs:
      capabilities:
        - type: network
          label: External network access
        - type: filesystem
          label: Local filesystem access
```

### 8.2 Dependency needs catalog

Recommended shape:

```yaml
dependency_needs:
  - class_name: rustic_ai.openai.llm.OpenAILLM
    needs:
      secrets:
        - key: OPENAI_API_KEY
          label: OpenAI API Key
      network:
        allow:
          - api.openai.com

  - class_name: rustic_ai.google.llm.GeminiLLM
    needs:
      secrets:
        - key: GEMINI_API_KEY
          label: Gemini API Key
      network:
        allow:
          - generativelanguage.googleapis.com
```

### 8.3 Connection to guild runtime objects

The association is indirect:

- `AgentSpec.class_name` selects `AgentNeeds`
- `AgentSpec.dependency_map` resolves dependency providers
- each resolved dependency provider `class_name` selects `DependencyNeeds`

No needs are embedded directly into `AgentSpec`.

## 9. Resolution Flow

### 9.1 Inputs

The resolver takes:

- `GuildSpec`
- agent registry entries
- dependency definitions
- agent needs catalog
- dependency needs catalog
- current secret/account/capability satisfaction state

### 9.2 Resolution steps

For each agent:

1. read `AgentSpec.class_name`
2. load `AgentNeeds` for that class
3. inspect `AgentSpec.dependency_map`
4. determine resolved dependency provider class names
5. load `DependencyNeeds` for each provider
6. merge all needs into one effective per-agent set
7. deduplicate by semantic key
8. evaluate which needs are already satisfied
9. attach UI actions for unsatisfied needs

### 9.3 Merging rules

- secrets dedupe by secret key
- OAuth dedupe by provider plus normalized scope set
- network dedupe by host entry
- filesystem dedupe by path plus mode
- capabilities dedupe by capability type

Conflicts should fail closed. Example:

- one provider requires read-only path
- another provider requires read-write path

The merged result should use the stricter or broader rule according to explicit
policy, not silently guess.

## 10. Satisfaction Model

The UI does not need just the declared needs. It needs status.

Each effective need should resolve to one of:

- `satisfied`
- `missing`
- `approval_required`
- `reauth_required`
- `unsupported`

Examples:

- secret key exists in the secret provider chain -> `satisfied`
- OAuth provider not connected -> `missing`
- network host requested but not approved -> `approval_required`
- OAuth account connected but scopes stale -> `reauth_required`

## 11. User Actions

Each unsatisfied need should map to a typed next action.

Examples:

- `provide_secret`
- `connect_oauth`
- `reauthorize_oauth`
- `approve_network_access`
- `approve_filesystem_access`

Example resolved need:

```json
{
  "id": "secret:OPENAI_API_KEY",
  "kind": "secret",
  "label": "OpenAI API Key",
  "status": "missing",
  "action": {
    "type": "provide_secret",
    "secret_key": "OPENAI_API_KEY"
  }
}
```

## 12. UI Implications

Rustic UI should be able to render:

- what this agent can access
- which requirements come from the agent itself
- which requirements come from selected dependency providers
- what is already connected or approved
- what the user must do before launch

Recommended screens:

- per-agent `Needs`
- per-guild aggregated `Launch Requirements`
- settings pages for:
  - secrets
  - connected accounts
  - capability approvals

## 13. Enforcement Boundary

This design is not only for display. It should also drive enforcement.

Recommended boundary:

- needs catalogs define expected requirements
- resolution computes effective needs
- preflight blocks launch on unsatisfied mandatory needs
- launch-time env and capability injection use the effective needs result

This allows the display model and enforcement model to stay aligned.

## 14. Relationship to Existing Forge Structures

This proposal intentionally does not replace the following immediately:

- `registry.AgentRegistryEntry.Secrets`
- `registry.AgentRegistryEntry.Network`
- `registry.AgentRegistryEntry.Filesystem`
- `AgentSpec.DependencyMap`
- existing secret provider chain

Instead, the first implementation should treat those current fields as
enforcement/runtime surfaces and introduce `AgentNeeds` and `DependencyNeeds`
as the user-facing declarative layer.

Later, the registry and launch code can be progressively aligned so those
runtime fields are derived from effective needs rather than hand-maintained
separately.

## 15. Open Questions

- Where should the needs catalogs live:
  - agent registry YAML
  - separate needs YAML
  - catalog/metastore records
- How should dependency provider class names be resolved canonically across Go
  and Python?
- Should filesystem approvals be absolute paths only, or allow symbolic
  workspace aliases?
- Should capability approvals be stored per:
  - user
  - org
  - guild
  - agent class
- How should optional vs required needs be represented?

## 16. Recommendation

Adopt `AgentNeeds` and `DependencyNeeds` as separate, class-keyed requirement
catalogs and keep `AgentSpec` unchanged as the runtime launch object.

This gives Forge and Rustic UI a clean setup model:

- the UI can explain agent access clearly
- dependency-provider needs become explicit
- secrets and OAuth flows can be driven from the same model
- runtime launch stays decoupled from setup metadata

This is the right foundation for user-visible secret prompts, OAuth connect
flows, and capability approval UX.
