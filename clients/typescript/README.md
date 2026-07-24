# @rustic-ai/api-client

TypeScript/axios client for the Rustic AI HTTP API.

The client is **generated** from the OpenAPI specification
(`forge-go/api/openapi/`) with [OpenAPI Generator](https://openapi-generator.tech)
(`typescript-axios`). Do not edit `src/` by hand — it is overwritten on every
regeneration.

## Install

```bash
npm install @rustic-ai/api-client axios
```

`axios` is a runtime dependency and is installed automatically.

## Usage

```ts
import { Configuration, GuildsApi, CatalogApi } from '@rustic-ai/api-client'

const config = new Configuration({ basePath: 'http://localhost:8880' })

const guilds = new GuildsApi(config)
const { data } = await guilds.getGuildDetailsById({ guildId: 'my-guild' })
```

Model interfaces (`BlueprintCreate`, `AgentSpecInput`, `GuildSpec`, …) are exported
from the package root:

```ts
import type { BlueprintDetailsResponse, GuildStatus } from '@rustic-ai/api-client'
```

## Regenerating

The client is generated directly from the OpenAPI 3.1 document
(`forge-go/api/openapi/openapi.json`).

```bash
npm install        # once
npm run generate   # regenerate src/ from the spec
npm run build      # type-check and emit dist/
```

Generator settings live in `openapitools.json` (pinned generator version, input
spec, options). Files that must not be produced inside `src/` are listed in
`src/.openapi-generator-ignore`.

