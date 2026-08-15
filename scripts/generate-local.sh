#!/usr/bin/env bash
# Bootstrap generated code on the host, for local development.
#
# In CI the orchestrator generates these files inside the pipeline and they are
# never committed (see monorepo/proto/monograph.toml `produces`). But a
# developer running `go build ./...` or `go test ./...` directly on the host
# needs them present, so run this once after a fresh clone.
#
# Deliberately uses the same pinned toolchain versions as the orchestrator, so
# host and pipeline produce identical output.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Single source of truth, shared with the orchestrator. Never restate it here.
PROTOC_GEN_GO_VERSION="$(tr -d ' \t\n\r' < "$ROOT/monorepo/proto/protoc-gen-go.version")"
TS_PROTO_VERSION="$(tr -d ' \t\n\r' < "$ROOT/monorepo/proto/ts-proto.version")"
GO_IMAGE="golang:1.26-alpine"

echo "==> generating Go and TypeScript bindings from proto/user.proto"
docker run --rm \
  -v "$ROOT/monorepo:/src" \
  -v "$ROOT/.cache/gomod:/go/pkg/mod" \
  -w /src \
  "$GO_IMAGE" sh -c "
    set -eu
    apk add --no-cache protobuf nodejs npm >/dev/null
    go install google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}
    npm install -g ts-proto@${TS_PROTO_VERSION} >/dev/null
    sh ./proto/codegen.sh
  "

echo "==> tidying the monorepo module"
(cd "$ROOT/monorepo" && go mod tidy)

echo "done. generated files are gitignored; regenerate any time with this script."
