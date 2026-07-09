# Forge Local Model Fit Design

## Summary

This document specifies a Forge-native Go implementation of the small subset of `llmfit` that Rustic needs for local model recommendation and launch-time dependency selection.

The goal is not to port `llmfit` feature-for-feature. The goal is to answer a smaller product question:

> Given this machine, this curated local model catalog, and our existing local llama.cpp server, which local models are runnable and which should be recommended?

This design assumes:

- Forge maintains a small curated catalog of supported local models.
- The local runtime is a single OpenAI-compatible llama.cpp server.
- Remote providers such as OpenAI, Gemini, Bedrock, and Azure remain configured separately and are out of scope for fit analysis.
- The result is used by Rustic UI and Forge dependency selection, not as a standalone TUI or dashboard product.

## Goals

- Provide a Go package that scores and ranks curated local LLM options against current machine hardware.
- Keep model fit logic inside Forge, with no runtime dependency on `llmfit`.
- Support launch-time local model recommendation for `llm` dependency bindings.
- Produce deterministic, UI-friendly results with stable fit levels and explanations.
- Keep the model catalog small, explicit, and versioned in-repo.

## Non-goals

- No port of `llmfit` TUI, web dashboard, or serve API.
- No support for Ollama, LM Studio, MLX, vLLM, or runtime-specific discovery in v1.
- No automatic download, pull, or installation of models.
- No attempt to infer arbitrary Hugging Face model fit from raw model metadata.
- No broad cloud/provider recommendation engine.

## Current Forge Context

Forge already uses curated dependency entries in [forge-go/conf/agent-dependencies.yaml](/home/rohit/work/dragonscale/project-go/rustic-go/forge-go/conf/agent-dependencies.yaml).

For local LLM use, the current default pattern is:

- dependency class: `rustic_ai.litellm.agent_ext.llm.LiteLLMResolver`
- base URL: `http://localhost:55262/v1`
- model names: curated `openai/rustic/...` aliases backed by the local llama.cpp server

This design keeps that architecture. The new model-fit subsystem does not replace the resolver shape. It helps choose among the curated local `llm_*` dependency entries.

## Proposed Package

Add a new Go package:

- `forge-go/modelfit`

This package owns:

- hardware profiling
- local model catalog schema
- fit estimation
- ranking and recommendation
- result serialization types for API/UI use

The package must not depend on GUI code, Electron code, or Python runtime behavior.

## Data Model

### SystemProfile

Represents the local machine resources relevant to local llama.cpp serving.

Required fields:

- `total_ram_bytes`
- `available_ram_bytes`
- `cpu_cores`
- `has_gpu`
- `gpu_count`
- `total_vram_bytes`
- `backend` as a small enum or string, initially:
  - `cpu`
  - `cuda`
  - `metal`
  - `rocm`
  - `unknown`
- `unified_memory`

Optional descriptive fields:

- `cpu_name`
- `gpu_name`

Behavior:

- v1 should support best-effort Linux and macOS detection.
- If GPU/VRAM detection fails, the system is treated as CPU-only instead of erroring.

### ModelProfile

Represents a curated local model option that Forge is willing to recommend.

Required fields:

- `id`
- `display_name`
- `dependency_key`
- `resolver_class_name`
- `provided_type`
- `model_name`
- `base_url`
- `parameter_count_b`
- `quantization`
- `context_length`
- `min_ram_bytes`
- `preferred_vram_bytes`
- `estimated_memory_bytes`
- `embedding_only` boolean
- `use_case_tags` as a small set such as:
  - `chat`
  - `coding`
  - `reasoning`
  - `embedding`
- `quality_rank` numeric rank within the curated catalog

Optional fields:

- `notes`
- `token_speed_hint`
- `multimodal`

Important rule:

- `ModelProfile` is curated by Forge maintainers.
- V1 does not derive these values from arbitrary remote metadata.

### FitLevel

Use a small stable enum:

- `perfect`
- `good`
- `marginal`
- `too_tight`

Semantics:

- `perfect`: comfortably fits preferred memory target
- `good`: runnable with reasonable headroom
- `marginal`: likely runnable but tight
- `too_tight`: should not be recommended as runnable

### FitResult

Represents the evaluation of one curated model against one `SystemProfile`.

Required fields:

- `model_id`
- `fit_level`
- `runnable` boolean
- `estimated_memory_bytes`
- `available_memory_bytes`
- `utilization_pct`
- `estimated_tokens_per_second` optional numeric estimate
- `score`
- `explanations` as ordered human-readable reasons

Important rule:

- `FitResult` must be deterministic for the same `SystemProfile` and `ModelProfile`.

## Catalog Source of Truth

Add a new repo-tracked catalog file for local fit metadata, for example:

- `forge-go/conf/local-model-catalog.yaml`

This file should list only curated local models that Rustic wants to surface.

It must map cleanly to existing local dependency entries in `agent-dependencies.yaml`.

Each catalog item must include:

- the local dependency key, such as `llm_local_qwen3_5_0_8b`
- the actual `model` property sent to the local resolver
- the fit metadata fields required by `ModelProfile`

Important rule:

- `local-model-catalog.yaml` is the canonical fit catalog.
- `agent-dependencies.yaml` remains the canonical resolver wiring.
- The dependency key is the join key between the two.

## Fit Logic

### V1 memory model

Use a deliberately simple heuristic tuned for the curated catalog.

Primary rule:

- `estimated_memory_bytes` from the catalog is authoritative.

Available memory pool:

- If GPU is present and the model is marked GPU-preferred, compare first against VRAM.
- If unified memory is true, compare against unified available RAM.
- Otherwise compare against system available RAM.

Thresholds:

- `perfect`: utilization <= 70%
- `good`: utilization <= 85%
- `marginal`: utilization <= 100%
- `too_tight`: utilization > 100%

These thresholds are fixed in v1 and should be documented in code comments.

### V1 speed estimate

Do not attempt a full `llmfit`-style speed model.

Use one of these two approaches:

1. Preferred:
   - catalog supplies a coarse `token_speed_hint` bucket or baseline estimate
   - score adjusts by backend class and model size

2. Acceptable fallback:
   - omit `estimated_tokens_per_second` in v1 and score without it

If speed is omitted, the score must still remain stable and useful.

### V1 ranking

Recommendation ranking should be:

1. runnable models before non-runnable
2. better `fit_level`
3. better `quality_rank`
4. better speed estimate if available
5. smaller memory footprint as final tie-breaker

This ranking is intentionally simpler than `llmfit`.

## Public API Shape

Add a new Forge API endpoint for local model fit recommendations.

Recommended route:

- `GET /rustic/modelfit/local-models`

Optional query parameters:

- `use_case`
- `limit`
- `runnable_only`

Response:

- array or envelope of `FitResult` rows including the joined local dependency key and model metadata needed by the UI

Required response fields:

- `dependency_key`
- `display_name`
- `model_name`
- `fit_level`
- `runnable`
- `score`
- `estimated_memory_bytes`
- `available_memory_bytes`
- `utilization_pct`
- `estimated_tokens_per_second`
- `use_case_tags`
- `explanations`

The endpoint should only surface curated local models, not cloud LLM dependencies.

## Integration with Launch Flow

This model-fit system is intended to integrate with the Rustic launch modal dependency binding flow.

Expected behavior:

- When a blueprint requires an `llm` dependency and local models are relevant, the UI may request local fit recommendations.
- Forge returns ranked local dependency candidates from `modelfit`.
- The UI can preselect the best runnable local model or render a “Recommended for this machine” section.

Important boundary:

- `modelfit` does not modify `AgentSpec`.
- It only informs the choice of dependency binding, such as selecting `llm_local_qwen3_5_0_8b` instead of the generic `llm`.

## Implementation Plan

### Phase 1: core package

- Add `forge-go/modelfit`
- Implement:
  - `SystemProfile`
  - `ModelProfile`
  - `FitLevel`
  - `FitResult`
  - hardware detection
  - catalog loading
  - fit evaluation
  - ranking

### Phase 2: config and API

- Add `conf/local-model-catalog.yaml`
- Add a loader in Forge config/helpers
- Add `GET /rustic/modelfit/local-models`
- Return ranked local model fit results

### Phase 3: launch integration

- Integrate the endpoint into Rustic UI launch dependency selection
- Allow the launch UI to highlight recommended local `llm` bindings

## Testing

Unit tests:

- hardware profiling fallback behavior
- catalog loading and validation
- fit thresholds for `perfect`, `good`, `marginal`, `too_tight`
- deterministic ranking with tie-breaks

Fixture tests:

- CPU-only laptop
- single-GPU desktop
- Apple Silicon unified-memory machine

API tests:

- endpoint returns curated local models only
- `runnable_only` filters correctly
- `use_case` filters correctly
- stable ordering for the same fixture

Integration tests:

- joining `local-model-catalog.yaml` with `agent-dependencies.yaml`
- recommended dependency key exists and resolves to a valid local resolver entry

## Assumptions and Defaults

- V1 targets the existing local OpenAI-compatible llama.cpp server only.
- The model catalog is small and curated by the Forge team.
- Memory-fit correctness matters more than speed-estimation sophistication.
- Missing GPU detection degrades to CPU-only behavior, not failure.
- Cloud LLM providers are excluded from model-fit scoring in v1.
- `llmfit` is treated as inspiration and reference, not as a parity target.
