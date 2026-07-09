# Installation

Forge ships as source, not a package registry install. You clone `forge-go`, build the `forge` binary with `make`, and point it at a local `forge-python` checkout so it can spawn agent processes through `uv`/`uvx`. This page gets you from a clean checkout to a working binary with repo-local build caches and a validated toolchain.

## Prerequisites

| Requirement | Why |
|---|---|
| Go 1.25+ | Builds the `forge` binary (`go.mod` pins `go 1.25.0`). |
| Python 3.13 | Runtime for agent processes launched via `uvx`. |
| `uv` / `uvx` on `PATH` | Installs and runs `forge-python` from source without a separate `pip install` step. |
| Docker | Needed for some supervisor modes and integration/e2e tests. |
| `golangci-lint` | Required for `make lint` (invoked directly from `PATH`, not via `go run`). |
| `goreleaser` | Required for `make cross-compile`. |

!!! note "Two repos, one binary"
    `forge-go` builds the `forge` CLI. `forge-python` is the agent execution bridge that `forge` launches via `uvx`. You need both checked out locally, but only `forge-go` is compiled.

## Clone and build

Clone the repo and build from `forge-go`:

```bash
git clone <your-forge-repo-url> forge
cd forge/forge-go
make build
```

`make build` runs:

```bash
go build -trimpath $(LDFLAGS) -o bin/forge main.go
```

producing `forge-go/bin/forge`. `-trimpath` strips local filesystem paths from the binary so build output is reproducible across machines.

### Version stamping via ldflags

`make build` injects version metadata at link time rather than baking in a hardcoded string:

```makefile
VERSION ?= $(shell git describe --tags --always --dirty)
COMMIT  ?= $(shell git rev-parse --short HEAD)
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-s -w \
  -X github.com/rustic-ai/forge/forge-go/version.Version=$(VERSION) \
  -X github.com/rustic-ai/forge/forge-go/version.GitCommit=$(COMMIT) \
  -X github.com/rustic-ai/forge/forge-go/version.BuildDate=$(DATE)"
```

`-s -w` strips debug symbols to keep the binary small. Confirm the stamped values with:

```bash
./bin/forge version
```

This prints Forge Version, Git Commit, Build Date, Go Version, and OS/Arch — all sourced from `github.com/rustic-ai/forge/forge-go/version` fields set by the ldflags above (their unstamped defaults in source are `Version="0.4.2"`, `GitCommit="none"`, `BuildDate="unknown"`).

!!! tip "Dirty checkouts are visible"
    `git describe --tags --always --dirty` appends `-dirty` to `VERSION` if you have uncommitted changes, so `forge version` output doubles as a quick sanity check on what you actually built.

## Wire up the Python bridge

`forge` spawns agent processes through `uvx`, and it needs to know where your `forge-python` checkout lives:

```bash
export FORGE_PYTHON_PKG=/absolute/path/to/forge-python
```

Set this before running `forge server` or `forge client` for any workload that spawns agents — quick start, distributed clients, and e2e runs all require it. Without it, agent spawn requests have nothing to install and run.

## Repo-local Go caches

The Makefile pins Go's build caches under the repo instead of your global `$GOPATH`/`$GOCACHE`, so building Forge never pollutes (or is polluted by) other Go projects on your machine:

```makefile
export GOCACHE      := $(CURDIR)/.gocache
export GOMODCACHE   := $(CURDIR)/.gomodcache
export GOPATH       := $(CURDIR)/.gopath
export GOLANGCI_LINT_CACHE := $(CURDIR)/.golangci-cache
```

Every `make` target inherits these exports automatically — there's nothing to configure. If you invoke `go build`/`go test` directly outside `make`, set the same variables yourself to keep behavior consistent.

## Validate the build

Run the standard checks before you trust a build:

```bash
make test    # go test -v ./...
make lint    # golangci-lint run
make fmt     # gofmt/goimports formatting
make vet     # go vet ./...
```

`make test` runs the full unit/integration suite. `make lint` runs `golangci-lint` from `PATH` (not `go run`), so it must be installed separately. `make fmt` and `make vet` are cheap sanity passes worth running before every commit.

For a broader end-to-end pass, see [Quickstart](quickstart/), which walks through `make test-e2e-ladder` and a live single-process run.

### Cross-compiling

Cross-platform release builds go through `goreleaser`:

```bash
make cross-compile   # goreleaser build --snapshot --clean
```

This requires `goreleaser` installed and on `PATH`; it is not vendored or invoked via `go run`.

## The uv/permission gotcha

If `forge` fails to spawn an agent process with a permissions or cache-write error from `uv`, the cause is almost always `uv` trying to write to a cache or data directory it can't access (common in containers, CI, or restrictive home directories). Fix it by pointing `uv` and XDG paths at writable temp directories:

```bash
mkdir -p /tmp/forge-uv-cache /tmp/forge-xdg-cache /tmp/forge-xdg-data

export FORGE_UV_CACHE_DIR=/tmp/forge-uv-cache
export UV_CACHE_DIR=/tmp/forge-uv-cache
export XDG_CACHE_HOME=/tmp/forge-xdg-cache
export XDG_DATA_HOME=/tmp/forge-xdg-data
```

Set all four together — `FORGE_UV_CACHE_DIR` is Forge's own knob for where it tells `uv` to cache, while `UV_CACHE_DIR`, `XDG_CACHE_HOME`, and `XDG_DATA_HOME` are the underlying `uv`/XDG variables it can fall back to. This is exactly what Forge's own hermetic test harness (`e2e/main_test.go`) does: it builds a temp base directory and sets `HOME`, `XDG_CACHE_HOME`, `XDG_DATA_HOME`, `TMPDIR`, `FORGE_UV_CACHE_DIR`, and `UV_CACHE_DIR` into it before running anything.

!!! warning "Don't skip this in containers or CI"
    A read-only or unexpected `HOME` is the single most common reason a fresh checkout fails to spawn its first agent. If `forge server --with-client` hangs or errors during agent startup, check these four variables before anything else.

## Next steps

With a built binary, a wired-up `FORGE_PYTHON_PKG`, and a clean `make test`/`make lint` pass, you're ready to run something. Head to [Quickstart](quickstart/) to bring up a single-process server with an embedded Redis and SQLite metastore, or [Configuration](../reference/configuration/) to see the full set of environment variables and flags before you go distributed.
