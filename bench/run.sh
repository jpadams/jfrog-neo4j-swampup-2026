#!/usr/bin/env bash
# Select affected targets from the graph and run them, cold then warm.
#
# Replaces the old bench/compare.sh, which diffed the Go SDK orchestrator
# against the Dang one. The Go variant was removed when the project standardised
# on Dang (see docs/adr-002-go-vs-dang.md); it is recoverable from git at
# 4696da9:ci/orchestrator-go/.
#
# Usage: bench/run.sh [changed-path ...]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DAGGER="$ROOT/.bin/dagger"
MONOGRAPH="$ROOT/.bin/monograph"
OUT="$(mktemp -d)"
trap 'rm -rf "$OUT"' EXIT

CHANGED="${*:-proto/user.proto}"
CHANGED_CSV="$(printf '%s,' $CHANGED | sed 's/,$//')"

echo "==> extracting graph"
"$MONOGRAPH" extract --repo=monorepo > "$OUT/graph.json"

echo "==> selecting affected targets for: $CHANGED_CSV"
# --no-reuse so the targets genuinely execute; otherwise a previously recorded
# run would mark them reusable and the timings would be a lie.
"$MONOGRAPH" affected --in "$OUT/graph.json" --changed="$CHANGED_CSV" --no-reuse > "$OUT/plan.json"

python3 - "$OUT/plan.json" <<'PY'
import json, sys
p = json.load(open(sys.argv[1]))
run = [t["name"] for t in p["targets"] if t["runnable"] and not t["reusable"]]
skip = [t["name"] for t in p["targets"] if not t["runnable"]]
print(f"    affected: {len(p['targets'])}   will run: {len(run)}   nothing to run: {len(skip)}")
for n in run:
    print(f"      run  {n}")
for n in skip:
    print(f"      skip {n} (no test command)")
PY

run_once() {
  local label="$1" runid="$2"
  local start end
  start=$(date +%s)
  "$DAGGER" call orchestrator-dang run --plan="$OUT/plan.json" --run-id="$runid" 2>/dev/null \
    | grep -E '^\{' > "$OUT/report-$label.json"
  end=$(date +%s)
  echo "    wall: $(( end - start ))s"
}

echo "==> cold run"
run_once cold bench-cold
echo "==> warm run (Dagger's content-addressed cache should absorb most of it)"
run_once warm bench-warm

echo "==> results"
python3 - "$OUT" <<'PY'
import json, os, sys
out = sys.argv[1]

def load(label):
    with open(os.path.join(out, f"report-{label}.json")) as fh:
        return {r["target"]: r for r in json.load(fh)["results"]}

cold, warm = load("cold"), load("warm")
print(f"    {'target':24}{'status':9}{'cold ms':>9}{'warm ms':>9}")
failed = []
for t in sorted(cold):
    c, w = cold[t], warm.get(t, {})
    print(f"    {t:24}{c['status']:9}{c['durationMs']:>9}{w.get('durationMs', '-'):>9}")
    if c["status"] != "PASSED":
        failed.append(t)

# Two honesty notes on these numbers:
#
# 1. Per-target durations measure the test command alone, excluding image pull
#    and dependency install, so they will not sum to the wall time above.
# 2. The "warm ms" column is NOT a second measurement. When Dagger replays a
#    cached exec it replays the recorded results file too, so warm figures are
#    the cold run's numbers verbatim - which is why they match exactly. Wall time
#    is the only trustworthy warm signal here. `monograph record` stores null
#    rather than these values for a replayed run; see docs/adr-002.
print(f"\n    per-target total (cold, real work): "
      f"{sum(r['durationMs'] for r in cold.values())}ms")
print("    warm per-target figures are replayed, not re-measured; compare wall time instead")

if failed:
    print(f"    FAIL: {', '.join(failed)}")
    sys.exit(1)
print(f"    OK: {len(cold)} targets passed")
PY

echo "==> orchestrator size"
printf '    %-34s %s hand-written lines, %s files, 0 generated\n' \
  "ci/orchestrator-dang" \
  "$(grep -vcE '^\s*$|^\s*#' ci/orchestrator-dang/main.dang)" \
  "$(find ci/orchestrator-dang -type f | wc -l | tr -d ' ')"
