#!/usr/bin/env bash
# Benchmark graph-driven CI against straight Dagger.
#
#   ./bench/graph-vs-straight.sh            # phases 1 and 2
#   ./bench/graph-vs-straight.sh --counts   # phase 1 only (no containers needed)
#   ./bench/graph-vs-straight.sh --cold     # phases 1, 2 and 3
#
# --cold PRUNES the engine cache before each timed run. That is the regime a
# fresh CI runner is in, and the only one where selection can show up in wall
# clock. It also means your next Dagger runs anywhere re-download layers.
#
# Four arms, so neither the mechanisms nor the failure modes get conflated:
#
#   jenkins-all    every target on every commit, keyed by commit SHA, with no
#                  content-addressed cache to replay anything. Safe, maximal cost.
#   jenkins-paths  `when { changeset "thatdir/**" }` per stage. Cheap, and UNSAFE:
#                  a path filter sees the directory that changed, never what
#                  depends on it.
#   straight       every target, every time, from a hand-maintained list -- what a
#                  Dagger-only setup looks like. Safe, and Dagger's cache replays
#                  unchanged work, so far cheaper than jenkins-all.
#   graph          graph selection, cross-run reuse DISABLED. Isolates the value
#                  of knowing what a change affects.
#   graph+reuse    selection plus the history lookup that skips content which has
#                  already passed. The full system.
#
# All three share the same execution machinery (withGeneratedCode / prepared /
# testOne in ci/orchestrator-dang), so the only variable is target selection.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
MONOGRAPH="$ROOT/.bin/monograph"
DAGGER="$ROOT/.bin/dagger"
TRIALS="${TRIALS:-3}"

# One connection-resolution path for the whole repo. The previous inline loader
# here used `set -a` + `.`, which let .env CLOBBER an already-exported NEO4J_URI —
# the opposite precedence to graph/neo4j-env.sh, so the same .env produced
# different targets depending on which script you ran.
# shellcheck source=graph/neo4j-env.sh
. "$ROOT/graph/neo4j-env.sh"

GRAPH="$(mktemp)"; trap 'rm -f "$GRAPH" /tmp/bvs-plan-*.json' EXIT
"$MONOGRAPH" extract --repo=monorepo > "$GRAPH" 2>/dev/null || { echo "extract failed" >&2; exit 1; }

SCENARIOS=(
  "docs/README.md"
  "docs/.markdownlint.jsonc"
  "infra/main.tf"
  "services/billing/main.go"
  "libs/ui/src/index.ts"
  "libs/core/src/index.ts"
  "proto/user.proto"
  "tsconfig.base.json"
)

# ---------------------------------------------------------------- phase 1
# Deterministic. No containers, no timing noise, no cache state to control.
echo "=============================================================="
echo "PHASE 1  Targets selected (deterministic)"
echo "=============================================================="

ALL_COUNT="$(python3 - "$GRAPH" <<'PY'
import json, sys
g = json.load(open(sys.argv[1]))
print(len([t for t in g["targets"] if t.get("testCmd")]))
PY
)"

python3 - "$GRAPH" "$MONOGRAPH" "${SCENARIOS[@]}" <<'PY'
import json, subprocess, sys
graph_file, monograph = sys.argv[1], sys.argv[2]
scenarios = sys.argv[3:]
g = json.load(open(graph_file))
runnable = {t["name"] for t in g["targets"] if t.get("testCmd")}
total = len(runnable)

rows = []
for path in scenarios:
    out = subprocess.run([monograph, "affected", "--in", graph_file,
                          "--changed", path, "--no-reuse"],
                         capture_output=True, text=True).stdout
    p = json.loads(out)
    affected = {t["name"] for t in p["targets"] if t["runnable"]}
    # A path filter fires only for the directory that changed: no closure.
    direct = set(p["changedTargets"]) & runnable
    rows.append((path, total, len(direct), len(affected), sorted(affected - direct)))

print()
print(f"{'change':28}{'jenkins-all':>12}{'jenkins-paths':>15}{'straight':>10}{'graph':>7}")
print("-" * 72)
for path, tot, direct, aff, _ in rows:
    print(f"{path:28}{tot:>12}{direct:>15}{tot:>10}{aff:>7}")
print("-" * 72)
sums = [sum(r[1] for r in rows), sum(r[2] for r in rows),
        sum(r[1] for r in rows), sum(r[3] for r in rows)]
print(f"{'TOTAL target runs':28}{sums[0]:>12}{sums[1]:>15}{sums[2]:>10}{sums[3]:>7}")

print()
print("SAFETY -- dependent targets a path filter would silently skip")
print("-" * 72)
missed_total = 0
for path, _, _, _, missed in rows:
    missed_total += len(missed)
    print(f"{path:28}{len(missed):>4}  {','.join(missed) or '-'}")
print("-" * 72)
print(f"{'TOTAL missed':28}{missed_total:>4}")
if missed_total:
    print()
    print("  jenkins-all, straight and graph all miss NOTHING. Only jenkins-paths")
    print("  under-selects, and it is worst exactly where it matters most: a change")
    print("  to the shared IDL or the root tsconfig fires no stage at all, because")
    print("  neither directory owns a test, while seven targets are truly affected.")
PY

cat <<'NOTE'

  Read the counts as work SELECTED, not time. jenkins-all and straight select the
  same 9, but only straight has a content-addressed cache to replay the unchanged
  ones -- so most of the speed people chase with "affected target selection" is
  already available from caching alone, with no graph.

  The graph's own contributions are narrower and worth stating plainly: it avoids
  even considering unaffected targets, and it is safe where path filters are not.

  Counts are an unweighted average over hand-picked scenarios, not a prediction
  for any real repository's change distribution.
NOTE

[ "${1:-}" = "--counts" ] && exit 0

# ---------------------------------------------------------------- phase 2
echo
echo "=============================================================="
echo "PHASE 2  Wall-clock (requires a container runtime)"
echo "=============================================================="

if ! docker ps >/dev/null 2>&1; then
  cat >&2 <<'ERR'

  SKIPPED: no usable container runtime.

  Dagger needs one. If Docker Desktop is asking you to sign in, do that first,
  then re-run. Phase 1 above is unaffected.
ERR
  exit 2
fi

# time_cmd <label> <command...> -> prints median ms of $TRIALS runs
time_ms() {
  local label="$1"; shift
  local times=() t0 t1
  for _ in $(seq "$TRIALS"); do
    t0=$(python3 -c 'import time;print(int(time.time()*1000))')
    "$@" >/dev/null 2>&1
    t1=$(python3 -c 'import time;print(int(time.time()*1000))')
    times+=( $(( t1 - t0 )) )
  done
  printf '%s\n' "${times[@]}" | sort -n | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}'
}

# Warm-up. Without this the first scenario's `straight` run pays all the cold
# costs and warms the cache for the arms measured after it, which would make the
# graph arms look artificially fast. One full pass first puts every arm in the
# same regime.
echo
echo "  warming the engine (one full straight pass, untimed)..."
"$DAGGER" call orchestrator-dang straight --run-id=bench-warmup >/dev/null 2>&1

printf '\n%-28s %10s %10s %12s\n' "change" "straight" "graph" "graph+reuse"
printf '%s\n' "--------------------------------------------------------------------"
for path in "${SCENARIOS[@]}"; do
  slug="$(printf '%s' "$path" | tr '/.' '__')"
  "$MONOGRAPH" affected --in "$GRAPH" --changed="$path" --no-reuse > "/tmp/bvs-plan-$slug.json" 2>/dev/null
  "$MONOGRAPH" affected --in "$GRAPH" --changed="$path"            > "/tmp/bvs-reuse-$slug.json" 2>/dev/null

  s_ms="$(time_ms straight "$DAGGER" call orchestrator-dang straight --run-id=bench-straight)"
  g_ms="$(time_ms graph    "$DAGGER" call orchestrator-dang run --plan="/tmp/bvs-plan-$slug.json" --run-id=bench-graph)"
  r_ms="$(time_ms reuse    "$DAGGER" call orchestrator-dang run --plan="/tmp/bvs-reuse-$slug.json" --run-id=bench-reuse)"

  printf '%-28s %9sms %9sms %11sms\n' "$path" "$s_ms" "$g_ms" "$r_ms"
done
printf '%s\n' "--------------------------------------------------------------------"

cat <<'NOTE'

  Medians of TRIALS trials on a WARMED engine (one untimed straight pass runs
  first, so no arm benefits from another's cold work). Noisy at this size --
  engine scheduling and image-layer effects dominate -- so treat large ratios
  with suspicion and small ones as indistinguishable. The deterministic counts in
  phase 1 are the trustworthy comparison.

  Cache regime: whatever state the engine was already in. A fresh CI runner has
  an empty Dagger cache, which is the regime where selection matters most and is
  NOT what this measures.
NOTE

# ---------------------------------------------------------------- phase 3
[ "${1:-}" = "--cold" ] || exit 0

echo
echo "=============================================================="
echo "PHASE 3  Cold cache (engine pruned before every run)"
echo "=============================================================="
cat <<'NOTE'

  A fresh CI runner has an empty Dagger cache. This is the regime where
  selection can actually show up in wall clock, because no work can be replayed.

  Single measurement per row: each needs its own prune, so repeated trials are
  impractical. Read these as orders of magnitude, not precise figures.
NOTE

prune_cache() {
  printf '{ engine { localCache { prune } } }' | "$DAGGER" -M query >/dev/null 2>&1
}

cold_run() {
  local label="$1" targets="$2"; shift 2
  prune_cache
  local t0 t1
  t0=$(python3 -c 'import time;print(int(time.time()*1000))')
  "$@" >/dev/null 2>&1
  t1=$(python3 -c 'import time;print(int(time.time()*1000))')
  printf '%-34s %8s %12sms\n' "$label" "$targets" "$(( t1 - t0 ))"
}

printf '\n%-34s %8s %14s\n' "arm" "targets" "cold wall"
printf '%s\n' "--------------------------------------------------------------"
for spec in "docs/README.md:1" "libs/core/src/index.ts:4" "proto/user.proto:7"; do
  path="${spec%%:*}"; n="${spec##*:}"
  slug="$(printf '%s' "$path" | tr '/.' '__')"
  "$MONOGRAPH" affected --in "$GRAPH" --changed="$path" --no-reuse > "/tmp/bvs-cold-$slug.json" 2>/dev/null
  cold_run "graph  ($path)" "$n" \
    "$DAGGER" call orchestrator-dang run --plan="/tmp/bvs-cold-$slug.json" --run-id=cold-graph
done
cold_run "straight (all targets)" "9" \
  "$DAGGER" call orchestrator-dang straight --run-id=cold-straight
printf '%s\n' "--------------------------------------------------------------"
