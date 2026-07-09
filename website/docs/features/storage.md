# Storage & Files

Forge splits persistence into two concerns: a relational **metastore** for guild, agent, route, and blueprint records, and a blob-backed **file store** for the documents your guilds and agents produce and consume. Both are rooted under a single configurable Forge home directory.

## Two stores, one home

| Concern | Package | Backing | Default location |
|---|---|---|---|
| Metastore | `guild/store` | SQLite (default) or Postgres via GORM | `<forge-home>/data/forge.db` |
| File store | `filesystem` | `gocloud.dev/blob` — local disk, S3, or GCS | `<forge-home>/data/workspaces/` |

Everything anchors on `forgepath.ForgeHome()`, which resolves with this precedence:

1. `--forge-home` CLI flag (via `forgepath.SetHome`)
2. `FORGE_HOME` environment variable
3. `~/.forge` (falling back to `os.TempDir()/.forge` if the user home can't be determined)

```go
switch {
case override != "":            // set by SetHome(--forge-home)
    cached = expandHome(override)
case os.Getenv("FORGE_HOME") != "":
    cached = expandHome(os.Getenv("FORGE_HOME"))
default:
    home, _ := os.UserHomeDir()
    if home != "" {
        cached = filepath.Join(home, ".forge")
    } else {
        cached = filepath.Join(os.TempDir(), ".forge")
    }
}
```

`forgepath.Resolve(sub)` is just `filepath.Join(ForgeHome(), sub)`, and it's the basis for both the default DB path and the default filesystem root:

```go
db := serverDB
if db == "" {
    db = "sqlite://" + forgepath.Resolve("data/forge.db")
}
dataDir := serverDataDir
if dataDir == "" {
    dataDir = forgepath.Resolve("data")
}
```

The default local layout looks like this:

```text
~/.forge/                         # ForgeHome() default
  data/
    forge.db                      # SQLite metastore (default --db)
    workspaces/                   # filesystem base (FORGE_FILESYSTEM_GLOBAL_ROOT)
      <orgID>/<guildID>/GUILD_GLOBAL/<file>       # guild-wide files
      <orgID>/<guildID>/GUILD_GLOBAL/.<file>.meta # JSON sidecar
      <orgID>/<guildID>/<agentID>/<file>          # agent-scoped files
```

!!! note "The top-level `store/` package is not the metastore"
    `store/package.go` is a two-line placeholder — `package store` with a doc comment. It contains no metastore code. The real implementation lives at `guild/store` (import path `github.com/rustic-ai/forge/forge-go/guild/store`). If you're looking for `Store`, `NewGormStore`, or `ResolveDriverAndDSN`, that's where they are.

## The GORM metastore

`guild/store/gorm.go` defines `gormStore`, which implements the `Store` interface (guild/agent/route/board CRUD, `ProcessHeartbeatStatus`, `Close`) plus a `CatalogStore` interface for blueprint records. `NewGormStore(driverName, dsn)` opens the underlying `*gorm.DB` and `AutoMigrate`s roughly twenty models: `guilds`, `agents`, `guild_routes`, `guilds_relaunch`, `board`, `board_message`, and the blueprint catalog tables (`blueprint(s)`, `blueprintsharedwithorganization`, `blueprint_command`, `blueprint_starter_prompt`, `tags`, `blueprint_tag`, `blueprint_category`, `blueprint_reviews`, `agent_entry`, `blueprint_agent_link`, `blueprint_guild`, `user_guild`, agent icons).

Two drivers are supported:

| Driver | Constant | Notes |
|---|---|---|
| SQLite | `store.DriverSQLite` (`"sqlite"`) | Pure-Go `modernc.org/sqlite` driver — no CGO required |
| Postgres | `store.DriverPostgres` (`"postgres"`) | `gorm.io/driver/postgres` |

SQLite is registered as driver name `"sqlite"` specifically so Forge binaries built with `CGO_ENABLED=0` still run; `github.com/mattn/go-sqlite3` is only an indirect dependency.

!!! tip "Schema parity with Python"
    When the open driver is Postgres, `NewGormStore` also runs `runSchemaParityMigrations` so a Forge-Go server and the Python side (`forge-python` / `rustic-ai` SQLModel) can share one Postgres metastore byte-compatibly. These migrations rename tables (`blueprint_shared_with_organization` → `blueprintsharedwithorganization`, `blueprint_review` → `blueprint_reviews`), rename review columns, force a composite primary key `(id, guild_id)` on `agents`, and drop Go-only legacy columns. They run only when `db.Name() == "postgres"`.

JSONB-typed columns (`DependencyMap`, `Properties`, `BackendConfig`, route transformers, and similar) are stored via custom `Scan`/`Value` implementations in `guild/store/jsonb.go` (`RawJSON`, `JSONB`, `JSONBList`, `JSONBStringList`), tagged `type:jsonb` in GORM.

### `--db` DSN resolution and normalization

The `server` command's `--db` flag takes a raw DSN and defaults to `sqlite://<forge-home>/data/forge.db`. `store.ResolveDriverAndDSN` picks the driver by inspecting the DSN prefix:

```go
func ResolveDriverAndDSN(rawDSN string) (driverName string, dsn string) {
    trimmed := strings.TrimSpace(rawDSN)
    if isPostgresDSN(trimmed) { // postgres:// postgresql:// postgresql+psycopg://...
        return DriverPostgres, normalizePostgresDSN(trimmed)
    }
    return DriverSQLite, normalizeSQLiteDSN(trimmed) // strips sqlite://, expands ~
}
```

`isPostgresDSN` matches `postgres://`, `postgresql://`, `postgres:`, `postgresql+psycopg://`, and `postgresql+psycopg2://`. Anything else is treated as SQLite. `normalizePostgresDSN` rewrites psycopg-style URLs to plain `postgres://`. `normalizeSQLiteDSN` strips a leading `sqlite://` and expands a leading `~` or `~/` to the user home. `ensureSQLiteDir` creates the parent directory for file-based SQLite DSNs, skipping `:memory:` and `file:` DSNs.

```bash
# All valid --db values
--db sqlite:///home/me/.forge/data/forge.db
--db "sqlite://~/forge/forge.db"
--db :memory:
--db postgres://user:pass@host:5432/forge
--db "postgresql+psycopg://user:pass@host:5432/forge"
```

### SQLite durability tuning

Because SQLite is meant for an embedded, single-writer desktop deployment (not high concurrency), `configureSQLite` pins the connection pool to a single connection and applies pragmas tuned for crash safety over raw write throughput:

```go
sqlDB.SetMaxOpenConns(1)
sqlDB.SetMaxIdleConns(1)
sqlDB.SetConnMaxLifetime(0)
// busy_timeout always; WAL + NORMAL only for real files
db.Exec(`PRAGMA busy_timeout = 5000;`)
if isSQLiteFileDSN(dsn) {
    db.Exec(`PRAGMA journal_mode = WAL;`)
    db.Exec(`PRAGMA synchronous = NORMAL;`)
}
```

`busy_timeout = 5000` is always set. `journal_mode = WAL` and `synchronous = NORMAL` are applied only for real file-backed DSNs — `isSQLiteFileDSN` excludes `:memory:` and `mode=memory` DSNs, where WAL doesn't apply.

## The blob-backed file store

The `filesystem` package is the guild/agent workspace — a durable, scoped store for uploaded files, backed by [`gocloud.dev/blob`](https://gocloud.dev/howto/blob/) so the same code path works over local disk, S3, or GCS.

### Scoping: org / guild / agent

`filesystem/resolver.go` turns `(orgID, guildID, agentID)` into a `Scope{Protocol, BucketURL, ObjectPath, LocalRoot}`. The object path is always `path.Join(orgID, guildID, agentID)`:

- If `agentID` is empty, it becomes the sentinel constant `GuildGlobalScope = "GUILD_GLOBAL"` — guild-wide files that aren't tied to a specific agent.
- If `orgID` is empty, it falls back to `guildID`, so single-tenant setups still get a valid path.

`ResolveScope` dispatches by protocol: `file` (default), `s3`, `gcs`/`gs` — anything else returns `"unsupported filesystem protocol"`.

```go
const GuildGlobalScope = "GUILD_GLOBAL"

func resolveFileScope(base, orgID, guildID, agentID string) Scope {
    root := filepath.Clean(strings.TrimPrefix(base, "file://"))
    // no_tmp_dir=1: fileblob writes temp files inside the bucket dir,
    // avoiding cross-device link errors across /home vs /tmp mounts.
    u := url.URL{Scheme: "file", Path: root, RawQuery: "no_tmp_dir=1"}
    return Scope{
        Protocol:   "file",
        BucketURL:  u.String(),
        ObjectPath: path.Join(orgID, guildID, agentID),
        LocalRoot:  root,
    }
}
```

The `no_tmp_dir=1` query parameter is a deliberate durability fix: it forces `gocloud.dev` to create temp files inside the bucket directory instead of `os.TempDir()`, avoiding "invalid cross-device link" write failures when the data directory (e.g. `/home`) and `TMPDIR` (`/tmp`) sit on different mounts.

For S3 and GCS, `storage_options` on a guild's `DependencyConfig` map onto the bucket URL query string — `region`/`region_name`/`aws_region`, `endpoint`/`endpoint_url`, `force_path_style`, `disable_ssl` for S3; `project_id`, `credentials_file` for GCS.

### `LocalFileStore`

`filesystem/storage.go` wraps `gocloud.dev/blob` with a per-bucket cache (`map[BucketURL]*blob.Bucket` guarded by a mutex). Its methods — `Upload`, `Exists`, `Read`, `List`, `Delete` — all take a `DependencyConfig` plus `(orgID, guildID, agentID, filename)`.

Every stored file gets a JSON sidecar metadata object, `.{filename}.meta`, holding `content_length`, `content_type`, `uploaded_at` (RFC3339Nano UTC), and any user metadata. `List` hides dotfiles, the `.meta` sidecars, and any object containing a nested `/`. `Upload` is **create-only**: it checks `Exists` first and returns `ErrFileAlreadyExists` (HTTP 409) rather than silently overwriting. `Delete` removes both the file and its sidecar, and errors if the sidecar is missing.

Filenames are sanitized before any object key is built — `filesystem.SanitizeFilename` rejects any name containing `/`, `\`, or `..`, preventing directory traversal. It's applied on `Upload`, `Read`, `Delete`, and `Exists`.

### `DependencyConfig`: per-guild filesystem binding

```go
type DependencyConfig struct {
    ClassName      string
    PathBase       string
    Protocol       string
    StorageOptions map[string]any
}
```

This is built per-request in `api/filesystem_dependency.go` from a guild's `DependencyMap["filesystem"]` spec (`path_base`, `protocol`, `storage_options` properties). `Protocol` defaults to `"file"`. This is how an individual guild can be pointed at S3 or GCS instead of the shared local disk root.

## How it's wired together

Both stores are assembled once, in `agent/server.go`'s `StartServer`, and handed to `api.NewServer`:

```mermaid
flowchart TD
    A["--db DSN\n(or sqlite://<home>/data/forge.db)"] --> B["store.ResolveDriverAndDSN"]
    B --> C["store.NewGormStore"]
    C --> D["guild/store.Store\n(guild, agent, route, board records)"]

    E["--data-dir\n(or <home>/data)"] --> F["<data-dir>/workspaces\nFORGE_FILESYSTEM_GLOBAL_ROOT"]
    F --> G["filesystem.NewFileSystemResolver"]
    G --> H["filesystem.NewLocalFileStore"]

    D --> I["api.NewServer"]
    H --> I
    I --> J["HTTP file routes\n(api/guild_files.go)"]
    D -->|"resolveFilesystemDependency reads\nGuildModel.DependencyMap['filesystem']\n+ OrganizationID"| J
```

The relational metastore drives *where* each guild's files physically go: the HTTP file handlers look up the target guild's `DependencyMap["filesystem"]` and `OrganizationID` in the metastore to derive the `orgID` and `DependencyConfig` before touching the blob store.

## HTTP file routes

Both guild-scoped and agent-scoped routes exist, with matching verbs (native paths, also available under `/rustic/*` for compatibility):

| Scope | Method | Path | Notes |
|---|---|---|---|
| Guild | `GET` | `/api/guilds/{id}/files/` | List files in the guild's `GUILD_GLOBAL` scope |
| Guild | `POST` | `/api/guilds/{id}/files/` | Upload — multipart field `file`, optional `file_meta` JSON |
| Guild | `GET` | `/api/guilds/{id}/files/{filename}` | Download; `?download=true` forces `Content-Disposition: attachment` |
| Guild | `DELETE` | `/api/guilds/{id}/files/{filename}` | Delete file + sidecar |
| Agent | `GET` | `/api/guilds/{id}/agents/{agent_id}/files/` | List files scoped to the agent |
| Agent | `POST` | `/api/guilds/{id}/agents/{agent_id}/files/` | Upload, scoped to the agent |
| Agent | `GET` | `/api/guilds/{id}/agents/{agent_id}/files/{filename}` | Download |
| Agent | `DELETE` | `/api/guilds/{id}/agents/{agent_id}/files/{filename}` | Delete |

!!! warning "Uploads are create-only"
    `POST` never overwrites an existing file. A second upload of the same filename in the same scope returns `ErrFileAlreadyExists` (HTTP 409). Delete the file first if you need to replace it.

## Related environment variables and flags

| Name | Kind | Default | Purpose |
|---|---|---|---|
| `--db` | flag (`server`) | `sqlite://<forge-home>/data/forge.db` | Metastore DSN |
| `--data-dir` | flag (`server`) | `<forge-home>/data` | Base path for file storage; files land under `<data-dir>/workspaces/` |
| `--forge-home` | global flag | — | Overrides `forgepath.ForgeHome()` |
| `FORGE_HOME` | env | — | Overrides Forge home if `--forge-home` isn't set |
| `FORGE_FILESYSTEM_GLOBAL_ROOT` | env | `<data-dir>/workspaces` | Set by the server if unset; consumed by the filesystem resolver |
| `FORGE_DEPENDENCY_CONFIG` | env | `conf/agent-dependencies.yaml` | Agent dependency config path |

!!! note "Telemetry and OAuth use separate stores"
    The `--otel-sqlite-db-path` / `--otel-sqlite-port` / `--otel-sqlite-binary` flags configure a distinct sqlite-otel database for telemetry — not the metastore. Similarly, `--oauth-token-store` (`memory` or `keychain`) and `FORGE_KEYCHAIN_SERVICE` govern OAuth token storage via the OS keychain, independent of both the metastore and the file store. See [Telemetry](telemetry/) and [Authentication](secrets-oauth/) for details.

## See also

- [Quickstart](../getting-started/quickstart/) for first-run setup of `FORGE_HOME` and the default SQLite database.
- [Guilds](../concepts/guild-model/) for how `DependencyMap["filesystem"]` is defined on a guild.
- [Deploying with Postgres](../reference/configuration/) for running the metastore against a shared Postgres instance alongside the Python control plane.
