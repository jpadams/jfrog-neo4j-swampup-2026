#!/bin/sh
# Generate language bindings from the proto contract.
#
# This script IS the proto target's codegen step: `monograph.toml` names it in
# `codegenCmd`, so it runs inside the pipeline, and because it lives inside the
# target it is part of the target's content hash — editing the generator
# correctly invalidates every consumer.
#
# It assumes protoc, protoc-gen-go and protoc-gen-ts_proto are already on PATH;
# the orchestrator installs them as separate, separately-cached container steps.
#
# Run from the monorepo root.
set -eu

HERE="$(dirname "$0")"

# The .version files beside this script are the single source of truth for these
# pins, read by both the orchestrator and scripts/generate-local.sh. Verify
# rather than trust: if an installed plugin drifts from its pin, host and
# pipeline would silently generate different code.
WANT_GO="$(tr -d ' \t\n\r' < "$HERE/protoc-gen-go.version")"
WANT_TS="$(tr -d ' \t\n\r' < "$HERE/ts-proto.version")"

need() {
  command -v "$1" >/dev/null || { echo "codegen: $1 not on PATH"; exit 1; }
}
need protoc
need protoc-gen-go
need protoc-gen-ts_proto

GOT_GO="$(protoc-gen-go --version 2>&1 | awk '{print $NF}')"
if [ "$GOT_GO" != "$WANT_GO" ]; then
  echo "codegen: protoc-gen-go is $GOT_GO but proto/protoc-gen-go.version pins $WANT_GO"
  exit 1
fi

# ts-proto has no --version flag; ask npm what it installed.
GOT_TS="$(npm ls -g --depth 0 ts-proto 2>/dev/null | sed -n 's/.*ts-proto@//p' | tr -d ' \t\n\r')"
if [ "$GOT_TS" != "$WANT_TS" ]; then
  echo "codegen: ts-proto is ${GOT_TS:-absent} but proto/ts-proto.version pins $WANT_TS"
  exit 1
fi

# --- Go -------------------------------------------------------------------
# --go_opt=module=<path> strips the module prefix from the go_package option in
# user.proto, so output lands at proto/gen/go/userpb/ rather than under a
# github.com/... directory tree.
protoc \
  --go_out=. \
  --go_opt=module=github.com/jpadams/jfrog-2026/monorepo \
  proto/user.proto

# --- TypeScript -----------------------------------------------------------
# -I proto so the output path mirrors `user.proto` and lands directly in src/,
# rather than nesting a redundant proto/ directory.
#
# The options are load-bearing, not cosmetic:
#   onlyTypes=true        no encode/decode helpers, so the generated package has
#                         zero runtime dependencies
#   enumsAsLiterals=true  emits `const Role = {...} as const` instead of a TS
#                         `enum`. Enums are not erasable syntax, so this is what
#                         keeps `node --test src/*.test.ts` able to run
#                         TypeScript directly via type stripping, with no build
#                         step before tests.
#   stringEnums=true      string values rather than ordinals, so a wire value is
#                         readable in a failure message
# protoc will not create the output root itself, and it is gitignored, so it is
# absent on a fresh clone.
mkdir -p proto/gen/ts/src

protoc \
  -I proto \
  --ts_proto_out=proto/gen/ts/src \
  --ts_proto_opt=onlyTypes=true,enumsAsLiterals=true,stringEnums=true \
  user.proto

echo "codegen: protoc-gen-go $GOT_GO -> $(find proto/gen/go -name '*.pb.go' | wc -l | tr -d ' ') Go file(s)"
echo "codegen: ts-proto $GOT_TS -> $(find proto/gen/ts/src -name '*.ts' | wc -l | tr -d ' ') TS file(s)"
