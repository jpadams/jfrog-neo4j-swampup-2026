#!/usr/bin/env bash
# Demonstrate why a rebase onto an unrelated main costs nothing.
#
# The complaint this whole repo answers: someone merges an unrelated change,
# your branch needs a rebase, your commit SHA changes, and CI re-runs
# everything. That happens because CI keys work by commit SHA. This keys work by
# content.
#
# Two scenarios run, with OPPOSITE expected outcomes, because a demo that only
# ever answers "reuse" would pass trivially and prove nothing:
#
#   1. Rebase onto an unrelated docs change  -> every targetHash identical,
#                                               nothing to run.
#   2. Rebase onto a shared tsconfig change  -> hashes change, work re-runs.
#
# Runs against a throwaway clone, so the real repository history is untouched.
#
# Note: every Python block here is a quoted heredoc rather than `python3 -c`.
# Inline scripts need both quote styles (f-strings over dict keys), and there is
# no way to nest those inside a single-quoted shell string.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MONOGRAPH="$ROOT/.bin/monograph"
DAGGER="$ROOT/.bin/dagger"

# This script asks for `reusable`, and `record` writes history, so both need the
# same graph the rest of the repo uses. Without this the binary's own default
# (localhost) applies and the scenario silently runs against the wrong database —
# or none at all, once .env points at Aura.
# shellcheck source=graph/neo4j-env.sh
. "$ROOT/graph/neo4j-env.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }

# hashes <repo-dir> -> "name=hash" lines for every target
hashes() {
  "$MONOGRAPH" extract --repo="$1/monorepo" 2>/dev/null > "$WORK/h.json"
  python3 - "$WORK/h.json" <<'PY'
import json, sys
g = json.load(open(sys.argv[1]))
for t in sorted(g["targets"], key=lambda t: t["name"]):
    print(f"{t['name']}={t['targetHash']}")
PY
}

# selected <repo-dir> <changed-csv> -> comma-joined names that would execute
selected() {
  "$MONOGRAPH" extract --repo="$1/monorepo" 2>/dev/null > "$WORK/g.json"
  # Not 2>/dev/null: this call needs the graph (it resolves `reusable`), so its
  # stderr is the only explanation of a connection failure. Suppressing it turned
  # an unreachable database into an unexplained JSON parse error downstream.
  if ! "$MONOGRAPH" affected --in "$WORK/g.json" --changed="$2" \
       > "$WORK/p.json" 2>"$WORK/p.err"; then
    sed 's/^/    /' "$WORK/p.err" >&2
    return 1
  fi
  python3 - "$WORK/p.json" <<'PY'
import json, sys
p = json.load(open(sys.argv[1]))
print(",".join(t["name"] for t in p["targets"] if t["runnable"] and not t["reusable"]))
PY
}

# changed_files <repo-dir> -> paths changed on this branch vs main, relative to monorepo/
changed_files() {
  git -C "$1" diff --name-only main...HEAD -- monorepo \
    | sed 's|^monorepo/||' | paste -sd, -
}

# compare <before-file> <after-file> <allowed-csv> <required-csv>
# Fails unless the set of targets whose hash moved is within `allowed` and
# contains everything in `required`. Either may be empty.
compare() {
  python3 - "$1" "$2" "$3" "$4" <<'PY'
import sys
before = dict(l.strip().split("=", 1) for l in open(sys.argv[1]) if l.strip())
after  = dict(l.strip().split("=", 1) for l in open(sys.argv[2]) if l.strip())
allowed  = {x for x in sys.argv[3].split(",") if x}
required = {x for x in sys.argv[4].split(",") if x}

moved = {n for n in before if before[n] != after.get(n)}
print(f"    targets whose hash changed: {sorted(moved) or 'none'}")

unexpected = moved - allowed
if unexpected:
    print(f"    FAIL: unexpected hash changes: {sorted(unexpected)}")
    sys.exit(1)
missing = required - moved
if missing:
    print(f"    FAIL: these should have changed but did not: {sorted(missing)}")
    sys.exit(1)
PY
}

say "==> cloning to a throwaway workspace (real history untouched)"
git clone --quiet "$ROOT" "$WORK/repo"
cd "$WORK/repo"
git config user.email "demo@example.com"
git config user.name "Rebase Demo"
# origin would point at the local path we cloned from, which Dagger cannot parse
# as a git URL ("WARN failed to parse git remote URL"). Nothing here fetches, and
# every rebase is onto a local main.
git remote remove origin
note "clone HEAD $(git rev-parse --short HEAD)"

# ---------------------------------------------------------------------------
# The feature branch: a real change to a shared TypeScript library.
# ---------------------------------------------------------------------------
say "==> creating a feature branch that changes libs/core"
git checkout -q -b feature
cat >> monorepo/libs/core/src/index.ts <<'EOF'

/** Added by the rebase scenario: a real change to a shared library. */
export function isPrivileged(user: { role: string }): boolean {
  return user.role === "ROLE_ADMIN";
}
EOF
git commit -qam "feature: add isPrivileged to libs/core"

SHA_BEFORE="$(git rev-parse HEAD)"
CHANGED="$(changed_files "$WORK/repo")"
note "changed vs main: $CHANGED"
note "commit SHA:      ${SHA_BEFORE:0:12}"

hashes "$WORK/repo" > "$WORK/hashes-before.txt"
BEFORE_SELECTED="$(selected "$WORK/repo" "$CHANGED")"
note "would run:       $BEFORE_SELECTED"

# ---------------------------------------------------------------------------
# Simulate CI having already run and passed on this branch.
# ---------------------------------------------------------------------------
say "==> running CI once on the feature branch, and recording it in the graph"
"$MONOGRAPH" extract --repo=monorepo 2>/dev/null > "$WORK/g-feature.json"
"$MONOGRAPH" affected --in "$WORK/g-feature.json" --changed="$CHANGED" --no-reuse \
  > "$WORK/plan-feature.json"
"$DAGGER" call orchestrator-dang run \
  --plan="$WORK/plan-feature.json" --run-id="rebase-demo-$$" 2>/dev/null \
  | grep -E '^\{' > "$WORK/report.json"

python3 - "$WORK/report.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
for x in sorted(r["results"], key=lambda x: x["target"]):
    print(f"    {x['target']:22}{x['status']}")
PY
"$MONOGRAPH" record --in "$WORK/report.json" 2>&1 | sed 's/^/    /'

# ---------------------------------------------------------------------------
# Scenario 1: someone merges something genuinely unrelated.
# ---------------------------------------------------------------------------
say "==> SCENARIO 1: main moves with an unrelated docs change, then rebase"
git checkout -q main
printf '\nA typo fix, unrelated to anything on the feature branch.\n' >> monorepo/docs/architecture.md
git commit -qam "docs: fix a typo"
note "main is now $(git rev-parse --short HEAD)"

git checkout -q feature
git rebase -q main
SHA_AFTER="$(git rev-parse HEAD)"
note "feature rebased; SHA ${SHA_BEFORE:0:12} -> ${SHA_AFTER:0:12}"

if [ "$SHA_BEFORE" = "$SHA_AFTER" ]; then
  echo "    FAIL: the rebase did not change the commit SHA; nothing is being tested"
  exit 1
fi

hashes "$WORK/repo" > "$WORK/hashes-after.txt"
AFTER_SELECTED="$(selected "$WORK/repo" "$(changed_files "$WORK/repo")")"

# docs may legitimately move — the typo landed in it. Nothing else may.
compare "$WORK/hashes-before.txt" "$WORK/hashes-after.txt" "docs" ""
note "OK: libs/core and its consumers kept identical hashes across the rebase"

note "SHA-keyed CI would rerun: $BEFORE_SELECTED"
note "content-keyed CI reruns:  ${AFTER_SELECTED:-(nothing - all reused)}"
if [ -n "$AFTER_SELECTED" ]; then
  echo "    FAIL: expected nothing to run after the rebase, got $AFTER_SELECTED"
  exit 1
fi

# ---------------------------------------------------------------------------
# Scenario 2: the control. An "unrelated" change that genuinely does matter.
# ---------------------------------------------------------------------------
say "==> SCENARIO 2 (control): main moves with a shared tsconfig change, then rebase"
git checkout -q main
python3 - <<'PY'
import json, pathlib
p = pathlib.Path("monorepo/tsconfig.base.json")
cfg = json.loads(p.read_text())
cfg["compilerOptions"]["noUnusedLocals"] = True
p.write_text(json.dumps(cfg, indent=2) + "\n")
PY
git commit -qam "build: tighten shared tsconfig"
note "main is now $(git rev-parse --short HEAD)"

git checkout -q feature
git rebase -q main
hashes "$WORK/repo" > "$WORK/hashes-control.txt"
CONTROL_SELECTED="$(selected "$WORK/repo" "$(changed_files "$WORK/repo")")"

# Everything that reaches the shared tsconfig must move, libs/core included.
compare "$WORK/hashes-after.txt" "$WORK/hashes-control.txt" \
  "workspace,proto,libs/core,libs/ui,apps/web,apps/admin,libs/authz,services/api,services/billing" \
  "workspace,libs/core"
note "OK: the shared config change propagated — this is not a blanket 'always reuse'"

note "content-keyed CI reruns:  ${CONTROL_SELECTED:-(nothing)}"
if [ -z "$CONTROL_SELECTED" ]; then
  echo "    FAIL: a shared config change must cause work to run"
  exit 1
fi

say "==> both scenarios passed"
note "1. unrelated change -> new SHA, identical hashes, zero work"
note "2. shared config    -> hashes move, work runs"
note ""
note "The difference is not the rebase. It is what the cache key is made of."
