export const meta = {
  name: 'forge-website',
  description: 'Understand the Forge project deeply, design a uni-style docs IA, and write the content',
  phases: [
    { title: 'Understand', detail: 'parallel readers over Forge subsystems + design docs' },
    { title: 'Design IA', detail: 'synthesize nav tree + per-page briefs' },
    { title: 'Write', detail: 'one technical-writer agent per page → markdown on disk' },
  ],
}

const REPO = '/home/rohit/work/dragonscale/project-go/rustic-go'
const GO = `${REPO}/forge-go`
const DOCS = `${REPO}/website/docs`

// ---------- Phase 1: Understand ----------
phase('Understand')

const UNDERSTAND_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  required: ['purpose', 'keyConcepts', 'publicSurface', 'howItFits', 'notableDetails', 'codeSnippets'],
  properties: {
    purpose: { type: 'string', description: 'What this subsystem is and the problem it solves (3-6 sentences)' },
    keyConcepts: { type: 'array', items: { type: 'object', additionalProperties: false, required: ['name', 'explanation'], properties: { name: { type: 'string' }, explanation: { type: 'string' } } } },
    publicSurface: { type: 'array', description: 'Public APIs, CLI flags, HTTP routes, config, message types a user/dev would touch', items: { type: 'string' } },
    howItFits: { type: 'string', description: 'How it interacts with other subsystems (control plane, guilds, messaging, etc.)' },
    notableDetails: { type: 'array', description: 'Design decisions, failure handling, distributed-systems behavior, gotchas worth documenting', items: { type: 'string' } },
    codeSnippets: { type: 'array', description: 'Real, accurate snippets (Go, CLI, YAML, JSON) worth putting on a docs page, with a caption', items: { type: 'object', additionalProperties: false, required: ['caption', 'lang', 'code'], properties: { caption: { type: 'string' }, lang: { type: 'string' }, code: { type: 'string' } } } },
  },
}

const base = (scope) => `You are a senior engineer reverse-engineering the Forge project to document it. Forge is the Rustic AI runtime stack for running "guilds" (multi-agent systems): a Go control plane/runtime (forge-go) plus a Python execution bridge (forge-python).

Study THIS scope thoroughly and return an accurate, specific technical summary that a documentation writer can build pages from. Read real source — quote real type names, function names, CLI flags, HTTP routes, env vars, message/topic names. Do NOT invent APIs; if unsure, say so. Prefer precision over breadth.

SCOPE: ${scope}

Root README: ${REPO}/README.md. Go module: github.com/rustic-ai/forge/forge-go. Use ripgrep/reads freely.`

const TARGETS = [
  { key: 'overview', prompt: base(`The overall product & CLI. Read ${REPO}/README.md, ${GO}/main.go, ${GO}/command/ (root.go, server.go, client.go, version.go), ${GO}/version/, and ${REPO}/docs/distributed-architecture.md (architecture overview + component roles). Capture: what Forge is, the server vs client model, single-process vs distributed mode, every CLI command and flag, the top-level architecture (control plane, message broker, worker nodes), and how someone builds/runs it (Makefile).`) },
  { key: 'guild', prompt: base(`The Guild model — the core domain concept. Read ${GO}/guild/ (38 files) and ${REPO}/docs/FILE_API_GUILD_SPEC_FLOW.md. Capture: what a guild is, guild spec/definition format, agents within a guild, guild lifecycle (create/launch/stop), the guild manager, and the spec-to-running-guild flow. This is the central concept — be thorough.`) },
  { key: 'agent', prompt: base(`The Agent model & agent needs. Read ${GO}/agent/ and ${REPO}/docs/AGENT_NEEDS_DESIGN.md. Capture: what an agent is in Forge, agent lifecycle, agent "needs"/dependencies/capabilities, how agents are defined and placed, and the relationship to guilds and supervisors.`) },
  { key: 'api', prompt: base(`The HTTP API surface. Read ${GO}/api/ (41 files). Capture: the public OpenAPI surface, the /rustic/* compatibility surface, the /manager/* metastore surface, key routes/handlers grouped by resource, request/response shapes, and auth. List concrete endpoints.`) },
  { key: 'gateway', prompt: base(`The gateway & WebSocket communication. Read ${GO}/gateway/ and ${REPO}/docs/WS_COMMUNICATION_GUIDE.md. Capture: what the gateway does, the WebSocket protocol/message envelope, client<->server real-time comms, channels/topics, and connection lifecycle.`) },
  { key: 'scheduler', prompt: base(`Scheduling, placement & reconciliation (the distributed core). Read ${GO}/scheduler/, ${GO}/control/, ${GO}/registry/, and the failure/recovery + happy-path sections of ${REPO}/docs/distributed-architecture.md. Capture: the global scheduler, node registry, placement map, reconciler, how agents get placed on nodes, Redis control queues/topics, and failure/recovery mechanisms (node loss, rescheduling, leases/heartbeats).`) },
  { key: 'supervisor', prompt: base(`Supervision & the worker node runtime. Read ${GO}/supervisor/ (30 files). Capture: what a supervisor does, how it runs/monitors agent processes on a node, restart/backoff policies, health, and its interaction with the scheduler/control plane.`) },
  { key: 'messaging', prompt: base(`Messaging & the message bus. Read ${GO}/messaging/ and ${GO}/embed/ (embedded Redis). Capture: the messaging abstraction, Redis usage, embedded vs external Redis, topics/queues/streams, delivery semantics, and how messages flow between control plane, supervisors, and agents.`) },
  { key: 'model', prompt: base(`Model management & model-fit. Read ${GO}/model/, ${GO}/modelfit/, and ${REPO}/docs/MODEL_FIT_DESIGN.md. Capture: how Forge represents LLM/models & dependencies, model-fit recommendations, local model fit, runtime capability detection, and how this is exposed to guilds/agents. (Recent commits added model-fit; be current.)`) },
  { key: 'security', prompt: base(`Secrets, OAuth & keychain. Read ${GO}/secrets/, ${GO}/oauth/, ${GO}/keychain/, and ${REPO}/docs/DESKTOP_SECRETS_OAUTH_DESIGN.md and ${REPO}/SECURITY.md. Capture: secret storage/injection, OAuth flows (desktop), keychain integration, and how credentials reach agents securely.`) },
  { key: 'telemetry', prompt: base(`Observability. Read ${GO}/telemetry/. Capture: OTel-first telemetry (recent commits made it OTel-first), metrics/traces/logs, the --client-metrics-addr flag, observability compatibility, and what operators can monitor.`) },
  { key: 'storage', prompt: base(`Storage, filesystem & paths. Read ${GO}/store/, ${GO}/filesystem/, ${GO}/forgepath/. Capture: the metastore (SQLite mentioned in README), the DB abstraction (--db flag), guild/agent filesystem, path conventions, and durability.`) },
  { key: 'protocol', prompt: base(`Protocol, infra events & typed dependencies. Read ${GO}/protocol/, ${GO}/infraevents/, and ${REPO}/docs/GUILD_INFRA_EVENTS_PROPOSAL.md. Capture: the wire protocol/message types, infrastructure events, dependency typing (recent commit), and how components speak a common protocol.`) },
  { key: 'python', prompt: base(`The Python bridge. Read ${REPO}/forge-python/ (README/pyproject and package layout). Capture: what forge-python does, the GuildManagerAgent, the execution bridge between Go runtime and Python agents, how uv/uvx is used, and the contract tests. Do not read every file — get the shape and public entry points.`) },
  { key: 'devx', prompt: base(`Developer experience: build, test, local debug. Read ${GO}/Makefile, ${REPO}/docs/LOCAL_DEBUG.md, ${GO}/helper/, ${GO}/testutil/, and skim ${GO}/e2e/. Capture: prerequisites, make targets, the single-process quick start, distributed local run, env vars (FORGE_*), and the healthz check. This feeds Getting Started / Installation / Quickstart pages.`) },
]

const summaries = await parallel(TARGETS.map(t => () =>
  agent(t.prompt, { label: `understand:${t.key}`, phase: 'Understand', schema: UNDERSTAND_SCHEMA })
    .then(s => ({ key: t.key, summary: s }))
))

const byKey = {}
for (const s of summaries) if (s && s.summary) byKey[s.key] = s.summary
const allKeys = Object.keys(byKey)
log(`Understood ${allKeys.length}/${TARGETS.length} subsystems: ${allKeys.join(', ')}`)

const digest = (keys) => (keys && keys.length ? keys : allKeys)
  .filter(k => byKey[k])
  .map(k => `### SUBSYSTEM: ${k}\n${JSON.stringify(byKey[k], null, 1)}`)
  .join('\n\n')

// ---------- Phase 2: Design the information architecture ----------
phase('Design IA')

const IA_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  required: ['navYaml', 'pages'],
  properties: {
    navYaml: { type: 'string', description: 'The complete mkdocs `nav:` block as YAML (starting with "nav:"), 2-space indent, referencing every page path below exactly. Mirror uni-db\'s section taxonomy.' },
    pages: {
      type: 'array',
      description: 'Every documentation page to write, one per markdown file.',
      items: {
        type: 'object',
        additionalProperties: false,
        required: ['path', 'title', 'section', 'oneLiner', 'keyPoints', 'sourceKeys'],
        properties: {
          path: { type: 'string', description: 'docs-relative path, e.g. "getting-started/installation.md"' },
          title: { type: 'string' },
          section: { type: 'string', description: 'Top-level nav section this belongs to' },
          oneLiner: { type: 'string', description: 'What this page must accomplish for the reader' },
          keyPoints: { type: 'array', items: { type: 'string' }, description: 'Concrete points/sections the page must cover' },
          sourceKeys: { type: 'array', items: { type: 'string' }, description: `Which subsystem summary keys feed this page. Valid keys: ${allKeys.join(', ')}` },
          suggestedExamples: { type: 'array', items: { type: 'string' } },
        },
      },
    },
  },
}

const ia = await agent(
  `You are a principal technical-writing lead and product manager designing the documentation website for Forge (the Rustic AI runtime for running guilds — a Go control plane + Python bridge).

Design a COMPLETE information architecture modeled closely on the uni-db docs site, which uses this top-level taxonomy: Home, Why <Product>, Getting Started (Overview/Installation/Quickstart/CLI Reference), Features (overview + one page per capability), Concepts (Architecture, data/identity models, etc.), Guides (task-oriented), Use Cases, Reference (CLI/API/Config/Troubleshooting/Glossary). Adapt this taxonomy to Forge's reality — do not force uni's exact pages.

Produce:
1. navYaml — the full mkdocs \`nav:\` block. Include Home (index.md) and a "Why Forge" page (why-forge.md). Group Features around Forge's real capabilities (guilds, agents, distributed scheduling/placement, supervision, messaging, gateway/websockets, model-fit, secrets/oauth, telemetry, storage, HTTP API). Add Concepts (Architecture, Guild model, Agent model, Distributed control plane, Messaging/protocol, Placement & reconciliation), Guides (running a guild, distributed deployment, securing secrets, observability, model-fit), a Reference section (CLI reference, HTTP API, Configuration/env vars, Troubleshooting, Glossary), and an Architecture/Internals section for the distributed SRE material. Keep it deep but real — every page must be groundable in the summaries.
2. pages — one entry per markdown file referenced in navYaml, with the source subsystem keys that feed it.

Be comprehensive: aim for a rich site (roughly 30-45 pages), but ONLY include pages the summaries can actually support. Every path in navYaml MUST appear in pages and vice-versa.

Here are the subsystem understanding summaries:\n\n${digest(allKeys)}`,
  { label: 'design-ia', phase: 'Design IA', schema: IA_SCHEMA, effort: 'high' }
)

const pages = (ia.pages || []).filter(p => p && p.path && p.title)
log(`IA designed: ${pages.length} pages`)

// ---------- Phase 3: Write every page ----------
phase('Write')

const STYLE = `VOICE & STYLE (match the uni-db docs site exactly):
- Write as an expert technical writer + product manager. Confident, precise, concrete. No marketing fluff, no hedging, no "in this section we will".
- Open with an H1 that is the page title, then a punchy 1-2 sentence framing of the problem or capability.
- Use short declarative paragraphs, bold lead-ins for lists, and real code blocks (\`\`\`go, \`\`\`bash, \`\`\`yaml, \`\`\`json). Use tables for reference/comparison data. Use admonitions (!!! note / !!! warning / !!! tip) where they help.
- Prefer accurate, runnable examples grounded in the summaries (real CLI flags, env vars, routes, type names). NEVER invent APIs that aren't in the summaries — if a detail is unknown, describe behavior at a level the summaries support rather than fabricating specifics.
- Cross-link related pages with relative markdown links ending in "/" (mkdocs style), e.g. [Quickstart](../getting-started/quickstart/).
- Mermaid diagrams are supported via \`\`\`mermaid fences — use one where an architecture/flow diagram genuinely helps.
- Never mention AI assistants, Claude, or how this was written.
- Output ONLY the final markdown file content (no commentary, no code fence around the whole thing).`

const results = await parallel(pages.map(p => () =>
  agent(
    `Write the documentation page "${p.title}" for the Forge docs site.

PAGE PURPOSE: ${p.oneLiner}
NAV SECTION: ${p.section}
FILE PATH (docs-relative): ${p.path}

MUST COVER:\n${(p.keyPoints || []).map(k => `- ${k}`).join('\n')}
${(p.suggestedExamples && p.suggestedExamples.length) ? `\nSUGGESTED EXAMPLES:\n${p.suggestedExamples.map(e => `- ${e}`).join('\n')}` : ''}

Ground everything in these verified subsystem summaries (real type names, flags, routes, snippets are in here — use them, do not invent beyond them):

${digest(p.sourceKeys)}

${STYLE}

Write the complete page now, then save it to the file "${DOCS}/${p.path}" using the Write tool (create parent directories as needed). After writing, respond with just the path you wrote.`,
    { label: `write:${p.path}`, phase: 'Write', model: 'sonnet' }
  ).then(() => p.path).catch(() => null)
))

const written = results.filter(Boolean)
log(`Wrote ${written.length}/${pages.length} pages`)

return { navYaml: ia.navYaml, pages: pages.map(p => ({ path: p.path, title: p.title })), written }
