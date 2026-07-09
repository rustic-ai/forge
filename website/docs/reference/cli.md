# CLI Reference

The `forge` binary is a single Cobra CLI with three subcommands: `server`, `client`, and `version`. This page documents every flag it accepts and what it defaults to when compiled from source.

!!! note "Scope"
    This reference covers the `forge` binary built from `forge-go/main.go` (module `github.com/rustic-ai/forge/forge-go`). For a narrative walkthrough of running it, see [Quickstart](../getting-started/quickstart/).

## Persistent flags

These flags are accepted by every subcommand (`server`, `client`, `version`).

| Flag | Default | Values | Description |
|---|---|---|---|
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` | Minimum log severity emitted. |
| `--log-format` | `text` | `text`, `json` | Log line encoding. |
| `--forge-home` | `~/.forge` | any path | Base data directory. See [Forge home resolution](#forge-home-resolution) below. |

### Forge home resolution

The `forgepath` package resolves the data root with this precedence:

1. `--forge-home` flag
2. `FORGE_HOME` environment variable
3. `~/.forge` (falling back to a temp directory if the home directory can't be determined)

Every derived default path — the SQLite metastore, the data directory, the dependency config, the OTel sqlite DB — is computed as `forgepath.Resolve(sub)`, i.e. `<forge-home>/<sub>`.

## forge server

Starts the central control plane: HTTP API, metastore, scheduler, node registry, placement map, and reconciler. Internally, flags are parsed into an `agent.ServerConfig` and handed to `agent.StartServer(ctx, cfg)`.

### Storage and broker flags

| Flag | Default | Description |
|---|---|---|
| `--db` | `sqlite://<forge-home>/data/forge.db` | Metastore DSN. |
| `--redis` | *(unset — embedded miniredis)* | Redis address for the message broker. |
| `--nats` | *(unset)* | NATS server URL, used when `--backend nats`. |
| `--backend` | `redis` | Messaging backend: `redis` or `nats`. |
| `--embedded-redis-addr` | `127.0.0.1:6379` | Bind address for the embedded miniredis instance, used when `--redis` is omitted. |
| `--embedded-nats-addr` | *(ephemeral port)* | Bind address for the embedded NATS instance, used when `--nats` is omitted and `--backend nats`. |
| `--listen` | `:9090` | HTTP API listen address. |
| `--manager-api-base-url` | *(unset)* | Base URL for the internal manager API surface. |
| `--data-dir` | `<forge-home>/data` | Directory for central file storage. |
| `--dependency-config` | *(default dependency map under `conf/`)* | Path to the dependency configuration file. |
| `--state-store` | *(in-memory)* | State store backend; `diskcache` enables an on-disk cache. |

!!! warning "Compiled defaults vs. runbook examples"
    The compiled defaults are `--listen :9090` and `sqlite://<forge-home>/data/forge.db`. The README's single-process example explicitly overrides these with `--listen :3001` and `--db sqlite:////tmp/forge-local.db` (note the four slashes for an absolute SQLite path) — those are overrides, not what you get if you omit the flags.

#### forge server flag table

```bash
forge server \
  --listen :3001 \
  --db sqlite:////tmp/forge-local.db \
  --redis 127.0.0.1:6379 \
  --backend redis \
  --data-dir /tmp/forge-data \
  --dependency-config ./conf/dependency-map.yaml \
  --state-store diskcache
```

### In-process client flags (`--with-client` family)

`--with-client` starts an in-process compute node alongside the server — the basis of single-process mode. The `--client-*` flags configure that embedded node; they mirror the standalone `forge client` flags but are namespaced to avoid collision.

| Flag | Default | Description |
|---|---|---|
| `--with-client` | `false` | Start an in-process client/node alongside the server. |
| `--client-node-id` | machine hostname | Node ID for the embedded client. |
| `--client-metrics-addr` | `:9091` | Bind address for the embedded client's metrics/health server. |
| `--client-cpus` | *(detected)* | CPU capacity advertised for the embedded node. |
| `--client-memory` | *(detected)* | Memory capacity in MB advertised for the embedded node. |
| `--client-gpus` | *(detected)* | GPU capacity advertised for the embedded node. |
| `--client-default-supervisor` | *(unset)* | Default process supervisor: `docker` or `bwrap`. |
| `--client-default-agent-transport` | `direct` | Agent transport: `direct` or `supervisor-zmq`. |
| `--client-attach-process-tree` | `false` | Ties spawned agent processes to the server process tree so they exit with the server. |
| `--client-zmq-bridge-mode` | `ipc` | ZMQ bridge transport: `ipc` or `tcp`. |

### Telemetry flags

| Flag | Default | Description |
|---|---|---|
| `--otel-enabled` | `false` | Enable OpenTelemetry export. |
| `--otel-mode` | `desktop_sqlite` | `desktop_sqlite` or `external_otlp`. |
| `--otel-endpoint` | *(empty)* | OTLP/HTTP endpoint URL; required for `external_otlp`. |
| `--otel-service-name` | `forge-server` | `service.name` resource attribute. |
| `--otel-sqlite-binary` | *(empty)* | Path to the `sqlite-otel` sidecar binary; required for `desktop_sqlite`. |
| `--otel-sqlite-db-path` | `<forge-home>/telemetry/sqlite-otel.db` | SQLite span-store file path. |
| `--otel-sqlite-port` | `4318` | OTLP/HTTP listener port for the sqlite-otel sidecar. |

See [Telemetry](../features/telemetry/) for the metrics this emits and how the sidecar behaves.

### Auth flags

| Flag | Default | Description |
|---|---|---|
| `--oauth-token-store` | `memory` | Where OAuth tokens are persisted: `memory` or `keychain`. |

## forge client

Starts a worker/compute-node daemon: it detects local hardware, registers with a server, and runs agent processes via a supervisor. Flags are parsed into an `agent.ClientConfig` and handed to `agent.StartClient(ctx, cfg)`.

| Flag | Default | Description |
|---|---|---|
| `--server` | `http://localhost:9090` | Control-plane server URL to register and heartbeat against. |
| `--redis` | *(unset)* | Redis address; must match the server's broker in distributed mode. |
| `--nats` | *(unset)* | NATS server URL; must match the server's broker when using the NATS backend. |
| `--data-dir` | *(forge-home derived)* | Local data directory for this node. |
| `--cpus` | *(detected)* | CPU capacity to advertise. |
| `--memory` | *(detected)* | Memory capacity in MB to advertise. |
| `--gpus` | *(detected)* | GPU capacity to advertise. |
| `--node-id` | machine hostname | Unique node identifier. |
| `--metrics-addr` | `:9091` | Bind address for this node's metrics/health server (`/healthz`, `/readyz`, `/metrics`). |
| `--default-supervisor` | *(unset)* | Default process supervisor: `docker` or `bwrap`. |
| `--default-agent-transport` | `direct` | Agent transport: `direct` or `supervisor-zmq`. |
| `--zmq-bridge-mode` | `ipc` | ZMQ bridge transport: `ipc` or `tcp`. |

#### forge client flag table

```bash
FORGE_PYTHON_PKG=./forge-python \
forge client \
  --server http://127.0.0.1:3001 \
  --redis 127.0.0.1:6379 \
  --node-id worker-1 \
  --metrics-addr :9091 \
  --cpus 4 \
  --memory 8192 \
  --default-supervisor docker \
  --default-agent-transport direct \
  --zmq-bridge-mode ipc
```

!!! tip "Shared broker requirement"
    Every `forge client` in a cluster must point `--redis` (or `--nats`) at the same broker instance the `forge server` uses. Mismatched brokers mean the client never sees spawn requests on `forge:control:node:<node_id>`.

## forge version

Prints build metadata:

```
Forge Version: <version>
Git Commit:    <commit>
Build Date:    <date>
Go Version:    <go version>
OS/Arch:       <os>/<arch>
```

The underlying fields live in `forge-go/version/version.go` (`Version`, `GitCommit`, `BuildDate`), which default to `0.4.2` / `none` / `unknown` in source and are overridden at build time via `-ldflags -X`. `make build` stamps them from `git describe --tags --always --dirty`, `git rev-parse --short HEAD`, and a UTC timestamp:

```makefile
VERSION ?= $(shell git describe --tags --always --dirty)
COMMIT  ?= $(shell git rev-parse --short HEAD)
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-s -w \
  -X github.com/rustic-ai/forge/forge-go/version.Version=$(VERSION) \
  -X github.com/rustic-ai/forge/forge-go/version.GitCommit=$(COMMIT) \
  -X github.com/rustic-ai/forge/forge-go/version.BuildDate=$(DATE)"
```

## Compiled defaults vs. documented overrides

It's easy to conflate the examples in the README and runbooks with what you get from a bare invocation. The table below disambiguates:

| Setting | Compiled default | Common runbook override |
|---|---|---|
| `--listen` | `:9090` | `:3001` (README/LOCAL_DEBUG single-process example) |
| `--client-metrics-addr` / `--metrics-addr` | `:9091` | `127.0.0.1:19091` (LOCAL_DEBUG, to avoid clashing with a second node) |
| `--db` | `sqlite://<forge-home>/data/forge.db` | `sqlite:////tmp/forge-local.db` (absolute path, four slashes) |
| `--embedded-redis-addr` | `127.0.0.1:6379` | Usually left at default; both server and client must agree on it |

Running `forge server` with no flags at all gives you `:9090` and the forge-home-relative SQLite path — not the `:3001` / `/tmp` values seen in examples throughout the docs.

## Related pages

- [Quickstart](../getting-started/quickstart/) — single-process and distributed run examples using these flags.
- [Telemetry](../features/telemetry/) — metric names and OTel export configuration behind `--otel-*`.
- [Scheduler & Reconciliation](../internals/scheduler-placement/) — how `--client-cpus`/`--client-memory`/`--client-gpus` feed node placement.
