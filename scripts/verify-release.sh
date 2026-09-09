#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
expected_version="${1:-}"
verification_mode="${2:-all}"
cd "$repo_root"

case "$verification_mode" in
  all | checks | package) ;;
  *)
    echo "Expected verification mode all, checks, or package; got: $verification_mode" >&2
    exit 1
    ;;
esac

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required release tool is unavailable: $1" >&2
    exit 1
  fi
}

require_command go

if [[ "$verification_mode" == "all" || "$verification_mode" == "checks" ]]; then
  for command_name in golangci-lint node npm python uv; do
    require_command "$command_name"
  done
fi

if [[ "$verification_mode" == "all" || "$verification_mode" == "package" ]]; then
  require_command goreleaser
fi

expected_go_version="$(awk '$1 == "go" { print $2; exit }' "$repo_root/forge-go/go.mod")"
actual_go_version="$(go env GOVERSION)"
actual_go_version="${actual_go_version#go}"
if [[ "$actual_go_version" != "$expected_go_version" ]]; then
  echo "Expected Go $expected_go_version, found $actual_go_version" >&2
  exit 1
fi

go_project_version="$(sed -nE 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"([^"]+)"/\1/p' "$repo_root/forge-go/version/version.go")"

if [[ -z "$expected_version" ]]; then
  expected_version="$go_project_version"
fi

if [[ ! "$expected_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
  echo "Expected a semantic release version, got: $expected_version" >&2
  exit 1
fi

version_entries=("forge-go:$go_project_version")

if [[ "$verification_mode" == "all" || "$verification_mode" == "checks" ]]; then
  uv_version="$(uv --version | awk '{print $2}')"
  if [[ "$uv_version" != "0.12.9" ]]; then
    echo "Expected uv 0.12.9, found $uv_version" >&2
    exit 1
  fi

  golangci_version="$(golangci-lint --version)"
  if [[ "$golangci_version" != *"version 2.13.1"* ]]; then
    echo "Expected golangci-lint 2.13.1, found: $golangci_version" >&2
    exit 1
  fi

  python_version="$(python -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')"
  if [[ "$python_version" != "3.13" ]]; then
    echo "Expected Python 3.13, found $python_version" >&2
    exit 1
  fi

  node_major="$(node -p 'process.versions.node.split(".")[0]')"
  if [[ "$node_major" != "24" ]]; then
    echo "Expected Node.js 24, found $(node --version)" >&2
    exit 1
  fi

  python_project_version="$(python -c 'import tomllib; from pathlib import Path; print(tomllib.load(Path("forge-python/pyproject.toml").open("rb"))["project"]["version"])' 2>/dev/null || true)"
  typescript_project_version="$(node -p "require('$repo_root/clients/typescript/package.json').version")"
  typescript_lock_version="$(node -p "require('$repo_root/clients/typescript/package-lock.json').packages[''].version")"
  version_entries+=(
    "forge-python:$python_project_version"
    "typescript-package:$typescript_project_version"
    "typescript-lock:$typescript_lock_version"
  )
fi

if [[ "$verification_mode" == "all" || "$verification_mode" == "package" ]]; then
  goreleaser_version="$(goreleaser --version)"
  if [[ "$goreleaser_version" != *"v2.14.3"* ]]; then
    echo "Expected GoReleaser 2.14.3" >&2
    echo "$goreleaser_version" >&2
    exit 1
  fi
fi

for version_entry in "${version_entries[@]}"; do
  component="${version_entry%%:*}"
  component_version="${version_entry#*:}"
  if [[ "$component_version" != "$expected_version" ]]; then
    echo "$component version $component_version does not match release $expected_version" >&2
    exit 1
  fi
done

echo "Verifying Forge release $expected_version ($verification_mode)"

if [[ "$verification_mode" == "all" || "$verification_mode" == "checks" ]]; then
  (
    cd "$repo_root/forge-go"
    go generate ./api/contract/...
  )
  git -C "$repo_root" diff --exit-code -- forge-go/api/contract/gen.go

  (
    cd "$repo_root/forge-go"
    golangci-lint run --timeout=5m
    go test -v ./...
  )

  (
    cd "$repo_root/forge-python"
    uv sync --frozen --group dev
    uv run --frozen ruff check src tests
    uv run --frozen python -m compileall src tests
    uv build
    uv run --frozen pytest
  )

  (
    cd "$repo_root/clients/typescript"
    npm ci
    npm run generate
    if [[ -n "$(git status --porcelain -- src)" ]]; then
      echo "clients/typescript/src is out of date with openapi.json." >&2
      echo "Run 'npm run generate' in clients/typescript and commit the result." >&2
      git --no-pager diff -- src >&2
      exit 1
    fi
    npm run build
    npm pack --dry-run
  )
fi

if [[ "$verification_mode" == "all" || "$verification_mode" == "package" ]]; then
  (
    cd "$repo_root/forge-go"
    goreleaser check --config .goreleaser.yaml
    goreleaser release --snapshot --clean --config .goreleaser.yaml
  )
fi

if [[ -n "$(git -C "$repo_root" status --porcelain --untracked-files=no)" ]]; then
  echo "Release verification modified tracked files:" >&2
  git -C "$repo_root" status --short >&2
  exit 1
fi

echo "Forge release $expected_version verified successfully ($verification_mode)"
