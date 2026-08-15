#!/usr/bin/env bash
# A live demo in four beats: naive CI vs graph-selected CI.
#
#   ./bench/demo.sh                # warm engine. Shows WHAT RUNS. ~1 min.
#   ./bench/demo.sh --fresh        # destroys the engine container before each
#                                  # run: a real fresh CI runner. USE THIS to
#                                  # present wall clock. ~3 min.
#   ./bench/demo.sh --cold         # prunes the engine cache instead. Faster, but
#                                  # see the warning below -- it flatters us.
#   ./bench/demo.sh --no-pause     # no "press Enter" between beats
#
# --fresh VERSUS --cold, MEASURED
#
# `--cold` prunes Dagger's cache but leaves the engine's image store, so the
# docs lint really does re-execute -- but images do not re-pull. That shrinks the
# fixed floor, and since the ratio is (floor + 9*work)/(floor + work), a smaller
# floor INFLATES the advantage. Prune is therefore not the conservative choice:
#
#   regime                      1 target   9 targets   ratio
#   --cold (prune, warm images)   6-7s      32-42s     5-6x
#   --fresh (engine destroyed)    11-12s    47-48s     4.1x
#   bench/RESULTS.md phase 3      14.0s     51.5s      3.7x
#
# --fresh was also markedly more reproducible across trials (47/11 then 48/12,
# versus --cold swinging 42s then 32s on the nine-target arm). So --fresh is both
# the honest regime and the stable one. Quote 4x, not 6x.
#
# THE CHANGES HERE ARE REAL
#
# An earlier version of this script passed --changed=docs/README.md without
# editing anything. That was dishonest in a way worth recording, because the flaw
# is easy to reintroduce:
#
#   * Selection does not depend on file content -- it walks path->target
#     ownership and target->target edges -- so "if X changed, what breaks?"
#     answers correctly whether or not X really changed.
#   * targetHash and `reusable` DO depend on content. With nothing edited, every
#     target was already reusable before the demo began. The old beat 4 showed
#     "4 targets ran, then 0 ran" and invited the audience to infer that beat 3
#     caused the reuse. It did not. Beat 3 only executed because --no-reuse
#     forced it to redo work it could correctly have skipped.
#
# So this script edits real files, commits them, and derives the changed-path list
# from `git diff --name-only` -- the same way .github/workflows/ci.yml does. That
# removes the "you just told it what changed" objection, and it means --no-reuse
# is not needed anywhere: every beat earns its result.
#
# Edits happen in a throwaway clone, so the real repository is untouched. Each
# run stamps a unique nonce into the edits, so hashes are fresh every time and
# the demo behaves identically on its tenth rehearsal as on its first.
#
# WHY THE DEFAULT DOES NOT LEAD WITH WALL CLOCK
#
# On a warm engine every arm lands within noise of every other: fixed overhead
# (engine connect, source upload, module load) dominates and target work replays
# from cache to near-zero. Measured on this repo: naive 9 targets 16.4s, graph 1
# target 12.5s, graph 4 targets 16.6s -- the 4-target graph run was SLOWER than
# running everything. Presenting that as a speed win would be dishonest and an
# audience would catch it. The honest headline is work SELECTED: 9 versus 1.
# Pass --cold for the fresh-runner regime, where selection is worth ~3.7x.
# See bench/RESULTS.md, which measures both.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MONOGRAPH="$ROOT/.bin/monograph"
DAGGER="$ROOT/.bin/dagger"

# shellcheck source=graph/neo4j-env.sh
. "$ROOT/graph/neo4j-env.sh"

COLD=0
FRESH=0
PAUSE=1
for arg in "$@"; do
  case "$arg" in
    --cold)     COLD=1 ;;
    --fresh)    FRESH=1 ;;
    --no-pause) PAUSE=0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done
[ "$FRESH" = 1 ] && COLD=0   # --fresh supersedes --cold; never do both

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Unique per run, so a rehearsal never collides with recorded history and
# accidentally reports "nothing to run" for a change that is meant to execute.
NONCE="$$-$(date +%s)"

bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
beat() { printf '\n\033[1;36m%s\033[0m\n' "$*"; }
hold() { [ "$PAUSE" = 1 ] && { printf '\n\033[2m    -- Enter to continue --\033[0m'; read -r _; } || true; }

# show_diff <path...> -> the actual red/green edit for this PR's commit, against
# main. This is what makes "the changes here are real" (see header) visible
# instead of asserted: the audience sees the line that changed, not just its
# path. File-header noise (diff --git/index/---/+++) is stripped -- on a
# one-line demo edit it is pure clutter -- but the @@ hunk marker stays, so a
# multi-hunk diff still reads as located, not just "some green text appeared".
show_diff() {
  git -c color.diff=always diff -U1 main...HEAD -- "$@" \
    | grep -Ev 'diff --git a/|index [0-9a-f]{6,40}\.\.[0-9a-f]{6,40}|--- a/|\+\+\+ b/' \
    | sed 's/^/    /'
}

prune_if_cold() {
  if [ "$FRESH" = 1 ]; then
    # Destroy the engine outright. Dagger recreates it on the next call, with an
    # empty cache AND an empty image store -- the state a CI runner boots into.
    local engine
    engine="$(docker ps -a --filter name=dagger-engine --format '{{.Names}}' | head -1)"
    if [ -n "$engine" ]; then
      note "destroying engine container $engine (real fresh runner; images re-pull)"
      docker rm -f "$engine" >/dev/null 2>&1 || true
      for _ in $(seq 1 20); do
        docker inspect "$engine" >/dev/null 2>&1 || break
      done
    fi
    return 0
  fi
  [ "$COLD" = 1 ] || return 0
  note "pruning the engine cache (images stay warm -- see --fresh in the header)"
  printf '{ engine { localCache { prune } } }' | "$DAGGER" -M query >/dev/null 2>&1 || true
}

# changed_files -> paths changed on this branch vs main, relative to monorepo/.
# Exactly what ci.yml computes; --relative there, prefix-stripped here.
changed_files() {
  git diff --name-only main...HEAD -- monorepo | sed 's|^monorepo/||' | paste -sd, -
}

# show_selection <changed-csv> -> writes $WORK/plan.json, prints the selection
show_selection() {
  "$MONOGRAPH" affected --in "$WORK/graph.json" --changed="$1" \
    > "$WORK/plan.json" 2>"$WORK/err" || { sed 's/^/    /' "$WORK/err" >&2; return 1; }
  python3 - "$WORK/plan.json" <<'PY'
import json, sys
p = json.load(open(sys.argv[1]))
for r in p.get("resolutions", []):
    print(f"    {r['path']}  ->  {r['how']}  ->  owned by {','.join(r['targets'])}")
runnable = [t["name"] for t in p["targets"] if t["runnable"] and not t["reusable"]]
reused = [t["name"] for t in p["targets"] if t["runnable"] and t["reusable"]]
print(f"    graph selects {len(runnable)} target(s) to run: {','.join(runnable) or '(nothing)'}")
if reused:
    print(f"    already passed on this exact content, skipped: {','.join(reused)}")
if p.get("codegen") and runnable:
    print(f"    codegen first: {','.join(c['name'] for c in p['codegen'])}")
PY
}

# run_plan <label> -> executes $WORK/plan.json, prints verdicts and wall time
run_plan() {
  local start end
  prune_if_cold
  start=$(date +%s)
  "$DAGGER" call orchestrator-dang run --plan="$WORK/plan.json" --run-id="demo-$1-$NONCE" \
    2>/dev/null | grep -E '^\{' > "$WORK/report.json" || { echo "    run failed" >&2; return 1; }
  end=$(date +%s)
  python3 - "$WORK/report.json" "$((end - start))" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
for r in sorted(d["results"], key=lambda r: r["target"]):
    print(f"    {r['target']:<18} {r['status']:<7} {r.get('durationMs')}ms")
print(f"    {len(d['results'])} target(s) executed, {sys.argv[2]}s wall")
PY
}

bold "==> cloning to a throwaway workspace (the real repository is untouched)"
git clone --quiet "$ROOT" "$WORK/repo"
cd "$WORK/repo"
git config user.email "demo@example.com"
git config user.name "CI Demo"
# Every beat below branches off `main` BY NAME, and a plain clone only creates a
# local branch matching whatever ROOT currently has checked out. Presenting from
# a feature branch therefore produced a clone with no local `main` at all, and the
# demo died on its first edit with "'main' is not a commit and a branch 'pr-docs'
# cannot be created from it" -- one line into the script, in front of the
# audience. origin/main is present regardless (clone fetches every branch's
# objects and sets up remote-tracking refs for all of them, not just HEAD's), so
# create the local branch from that while the remote still exists.
git rev-parse --verify --quiet main >/dev/null || git branch main origin/main

# Cloning from a local path leaves origin set to that path, which is not a valid
# git endpoint, so Dagger logs `WARN failed to parse git remote URL` on every
# call. Harmless -- it just cannot attach VCS metadata to the trace -- but on a
# projector a warning naming your own home directory reads as a broken demo.
# Nothing here uses the remote: all branching is local.
git remote remove origin
note "clone HEAD $(git rev-parse --short HEAD)"
note "graph:      $(describe_target)"
if [ "$FRESH" = 1 ]; then
  note "FRESH mode: engine container destroyed before each run (~4x, quotable)"
elif [ "$COLD" = 1 ]; then
  note "COLD mode:  cache pruned, images stay warm (ratio is inflated; see header)"
else
  note "WARM engine: read the TARGET COUNTS, not the wall clock (see script header)"
fi
hold

# ---------------------------------------------------------------- beats 1 and 2
# One real docs change, put through two different CI approaches.
# The BEAT heading comes BEFORE the edit it is about: announcing the beat only
# after the diff had scrolled past meant the audience saw an unexplained change,
# then a target list, and only then what they had been looking at.
beat "BEAT 1 -- NAIVE CI: the docs typo rebuilds everything"
bold "==> a pull request that fixes a typo in the docs"
git checkout -q -b pr-docs main
printf '\n<!-- demo %s: fixed a stray double  space -->\n' "$NONCE" >> monorepo/docs/README.md
git commit -qam "docs: fix a stray double space"
CHANGED_DOCS="$(changed_files)"
note "commit $(git rev-parse --short HEAD)"
note "git diff --name-only says: $CHANGED_DOCS"
note ""
show_diff monorepo/docs/README.md
note ""
note "one markdown file, one line. Nothing compiled, nothing shipped."
hold

note "The target list a Dagger-only setup maintains by hand"
note "(ci/orchestrator-dang, allTargets). It never consults the change:"
"$DAGGER" call orchestrator-dang straight-selected 2>/dev/null \
  | grep -v '^[[:space:]]*$' | sed 's/^/      /' || true
hold
prune_if_cold
_start=$(date +%s)
"$DAGGER" call orchestrator-dang straight --run-id="demo-naive-$NONCE" 2>/dev/null \
  | grep -E '^\{' > "$WORK/naive.json"
_end=$(date +%s)
python3 - "$WORK/naive.json" "$((_end - _start))" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
for r in sorted(d["results"], key=lambda r: r["target"]):
    print(f"    {r['target']:<18} {r['status']:<7} {r.get('durationMs')}ms")
print(f"    {len(d['results'])} targets executed, {sys.argv[2]}s wall")
PY
note ""
note "9 targets ran. Go services were compiled and tested to check a typo in a"
note "markdown file. Scale that to 85 checks and you have the PR from the talk."
hold

beat "BEAT 2 -- THE SAME COMMIT, GRAPH-SELECTED"
"$MONOGRAPH" extract --repo=monorepo 2>/dev/null > "$WORK/graph.json"
note "extracted from the edited tree, so docs now carries a new content hash"
show_selection "$CHANGED_DOCS"
note ""
note "Not a path glob: the changed file resolved to the target that OWNS it, and"
note "no target depends on docs, so the walk stops there."
hold
run_plan docs
note ""
note "8 of 9 targets were never considered -- no image pull, no source upload,"
note "no cache lookup. Not skipped cheaply: never reached."
hold

# ---------------------------------------------------------------- beats 3 and 4
beat "BEAT 3 -- A REAL CHANGE: scoped, not trivial"
bold "==> a second pull request, this time changing a shared library"
git checkout -q -b pr-core main
cat >> monorepo/libs/core/src/index.ts <<EOF

/** Added by the demo ($NONCE): a real change to a shared library. */
export function isPrivileged(user: { role: string }): boolean {
  return user.role === "ROLE_ADMIN";
}
EOF
git commit -qam "feat(core): add isPrivileged"
CHANGED_CORE="$(changed_files)"
note "commit $(git rev-parse --short HEAD)"
note "git diff --name-only says: $CHANGED_CORE"
note ""
show_diff monorepo/libs/core/src/index.ts
hold

note "A demo where the graph always says 'run one thing' would prove nothing."
"$MONOGRAPH" extract --repo=monorepo 2>/dev/null > "$WORK/graph.json"
show_selection "$CHANGED_CORE"
note ""
note "4 targets, derived from real imports. Note apps/admin, which does NOT"
note "import libs/core -- it is reached transitively via libs/ui. A path filter"
note "on libs/core/** would silently never test it."
hold
run_plan core
# --plan records WHY each target was selected, not just what ran. Without it the
# graph can answer "what happened" but not "what was it for".
"$MONOGRAPH" record --in "$WORK/report.json" --plan "$WORK/plan.json" \
  --sha "$(git rev-parse HEAD)" 2>&1 | sed 's/^/    /'
note ""
note "Cost is proportional to the change: 1 target for a typo, 4 for a shared lib."
hold

beat "BEAT 4 -- REUSE: ask again about the same content"
note "Nothing edited, nothing rebased. Re-selecting the identical commit:"
show_selection "$CHANGED_CORE"

# Record the skip itself, not just the work. RecordSelection writes
# (CIRun)-[:PROVEN_BY {targetHash}]->(TargetRun) -- the earlier PASSED run that
# justifies each skip -- and until this call existed the demo proved reuse on
# screen and left the graph with zero of those edges, unable to evidence its own
# central claim. A report with no results is what "nothing executed" honestly
# looks like; `record` needs the CIRun to exist before it will attach a selection
# to it, and the empty report is what creates it. trigger/orchestrator say
# plainly that nothing ran, so this can never be counted as another build.
printf '{"id":"demo-reuse-%s","repo":"monorepo","trigger":"selection-only","orchestrator":"monograph","results":[]}\n' \
  "$NONCE" > "$WORK/reuse-report.json"
"$MONOGRAPH" record --in "$WORK/reuse-report.json" --plan "$WORK/plan.json" \
  --sha "$(git rev-parse HEAD)" 2>&1 | sed 's/^/    /'

note ""
note "Zero. Each of those four hashes now has a PASSED run recorded against it,"
note "from beat 3 -- which is why this is causal and not a trick: before beat 3"
note "the same query selected 4."
note ""
note "That is also why a rebase onto an unrelated main costs nothing. The commit"
note "SHA moves; the content hashes do not. For that proven end to end, with the"
note "control case where a real shared-config change DOES re-run work:"
note "    ./bench/rebase-scenario.sh"
# The last record of the demo deserves the same hold every other beat gets:
# without it the closing summary lands on top of the reuse result immediately.
hold

# The same run, read back out of the graph as a JFrog Evidence predicate.
#
# This step is in both drivers on purpose. The demotui and this script have
# diverged before -- neither recorded beat 4, so a graph full of demo history had
# zero PROVEN_BY edges and could not evidence the reuse its own beat 4 had just
# demonstrated. A step that exists in one driver only is that bug waiting to
# happen again.
#
# Generated for real, from the graph, and not uploaded: `jf evd create` needs a
# subject in Artifactory and a signing key. See docs/adr-003-jfrog-integration.md.
bold "==> RECORD writes to two systems of record"
note "→ Neo4j            CIRun, SELECTED, PROVEN_BY        [written]"
note "  the graph COMPUTES the decision -- mutable, cross-run"
note "→ JFrog Evidence   ci-coverage/v1 predicate           [emitted]"
note "  Evidence RECORDS it -- per-version, signed, immutable"
note ""
# stderr goes to its own file, never into the predicate: `evidence` prints a
# one-line summary there, and folding it into the JSON would corrupt the very
# document the command below names.
#
# Only the head of it is printed. The whole document is ~60 lines, and dumping
# it here scrolls the two-destinations framing above -- the actual point of the
# step -- off the top of a projector before anyone has read it. The demotui puts
# the full text behind "e"; this driver has no panel, so it points at the file.
if "$MONOGRAPH" evidence --run-id "demo-reuse-$NONCE" \
     > "$WORK/evidence.json" 2> "$WORK/evidence.err"; then
  head -20 "$WORK/evidence.json" | sed 's/^/    /'
  note "    ... $(($(wc -l < "$WORK/evidence.json") - 23)) more lines ..."
  # The last three lines are the punchline -- coverageGaps is the field a gate
  # would read -- so they are shown rather than trimmed away with the rest.
  tail -3 "$WORK/evidence.json" | sed 's/^/    /'
else
  note "(evidence unavailable: $(tr '\n' ' ' < "$WORK/evidence.err"))"
fi
note ""
# The command names the BASENAME, not $WORK/evidence.json: the work dir is a
# temp path that wraps across two lines on a projector, and the command is
# illustrative anyway -- its subject is a placeholder. The real path is printed
# below it, so nothing is hidden.
"$MONOGRAPH" evidence --run-id "demo-reuse-$NONCE" --command \
  --predicate-file evidence.json 2>/dev/null | sed 's/^/    /'
note ""
note "predicate written to $WORK/evidence.json"
note "Not run: the subject is an artifact in Artifactory and signing needs a key."
note "JFrog's own Dagger integration attests HOW this was built -- a signed link"
note "to the Cloud trace. This attests what a trace cannot: what was NOT run, and"
note "why that was safe."
hold

bold "==> demo complete"
note "naive: 9 targets, always."
note "graph: 1 for a docs typo, 4 for a shared lib, 0 for content already proven."
note "and that 0 leaves a document: what was skipped, and what proved it."
if [ "$FRESH" = 1 ]; then
  note ""
  note "Fresh-engine regime: measured 47-48s for nine targets against 11-12s for"
  note "one, about 4x. That is the number to quote -- it is the honest one and the"
  note "reproducible one."
elif [ "$COLD" = 0 ]; then
  note ""
  note "Wall clock did not separate the arms, as expected on a warm engine."
  note "Re-run with --fresh for the real fresh-runner regime, or quote RESULTS.md."
fi
