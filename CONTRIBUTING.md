# Contributing

## HTTP API: adding a new route

The HTTP API is **contract-first**. The OpenAPI document
`forge-go/api/openapi/openapi.json` (OpenAPI 3.1, served at `/openapi.json`) is
the single source of truth. From it we generate:

- the **Go server contract** (`forge-go/api/contract/gen.go`) consumed by the gin server, and
- the **TypeScript client** (`clients/typescript`, published as `@rustic-ai/api-client`).

Both generators read `openapi.json` directly — `oapi-codegen` (Go) and OpenAPI
Generator (TypeScript) both support OpenAPI 3.1, so there is no intermediate spec.

### Prerequisites

- Go (see `forge-go/go.mod`)
- Node 24+ and a JDK 11+ (the OpenAPI Generator runs on the JVM)

Install the client's tooling once: `cd clients/typescript && npm ci`.

### Steps

1. **Describe the endpoint in the spec.** Edit
   `forge-go/api/openapi/openapi.json`: add the path, its `operationId`,
   parameters, request body, and response schemas.

   - The `operationId` becomes both the Go `ServerInterface` method name and the
     TypeScript client method name — make it clear and camelCase
     (e.g. `listOrganizationSecrets`).
   - Reuse existing component schemas where possible; add new ones under
     `components/schemas` (kept alphabetically sorted).

2. **Regenerate.** Regenerate both artifacts from the spec — each command runs
   from its own directory:

   ```bash
   cd forge-go && go generate ./api/contract/    # regenerates gen.go
   cd clients/typescript && npm run generate      # regenerates the TS client
   ```

   (See `clients/typescript/README.md` for the client's regenerate/build steps.)

3. **Implement the handler.** Regenerating adds a method to the generated
   `contract.ServerInterface`. `Server` must implement the whole interface —
   enforced by `var _ contract.ServerInterface = (*Server)(nil)` in
   `contract_server.go` — so the Go build now fails until you add it. Run:

   ```bash
   cd forge-go && go build ./...
   ```

   The compile error names the exact method(s) missing. This is intentional — it
   prevents the server from silently drifting from the spec. To satisfy it, add
   the method to `forge-go/api/contract_server.go`. Most handlers are thin
   adapters over an `http.HandlerFunc` via the `dispatch` helper, which copies
   path parameters into the request:

   ```go
   func (s *Server) GetWidget(c *gin.Context, widgetID string, _ contract.GetWidgetParams) {
       s.dispatch(c, s.handleGetWidget(), map[string]string{"widget_id": widgetID})
   }
   ```

   Then write the handler logic itself (e.g. in a new or existing
   `forge-go/api/*.go` file).

4. **Verify.**

   ```bash
   cd forge-go && go build ./... && go test ./api/...
   cd ../clients/typescript && npm run build
   ```

5. **Commit** the spec plus every generated artifact and your handler:
   `openapi.json`, `gen.go`, `clients/typescript/src/`, and your
   `contract_server.go` / handler changes.

The TypeScript client method appears automatically — no manual client work.

### Conventions and gotchas

- **Double-mounting.** Every contract route is served both bare (e.g.
  `/catalog/...`) and under `/rustic/...`. This is automatic; do nothing.

- **64-bit / gemstone IDs.** JavaScript's `number` loses precision above 2^53.
  Mark such fields `"format": "int64"` in the spec; the client generates them as
  `bigint` (see `openapitools.json`). Ordinary counts should stay plain
  `integer` → `number`.

- **Consistent path-parameter names.** gin panics if two sibling routes use
  different wildcard names at the same position, so use the *same* parameter
  name across them in the spec (e.g. both `/catalog/categories/{category}` and
  `/catalog/categories/{category}/blueprints/` use `{category}`, not
  `{category_id}` vs `{category_name}`).

- **Optional / gated features.** Endpoints backed by an optional manager
  (e.g. secrets, OAuth) are always part of the contract; their handlers return
  `404` when the manager is not configured, rather than being conditionally
  registered.

- **Non-contract routes.** A few internal routes (manager metastore, local
  identity/quota, WebSocket upgrades) are registered directly in
  `forge-go/api/server.go` and are intentionally not part of the public OpenAPI
  contract. Add new *public* routes through the spec, as above.
