# Monorepo context graph + Dagger

Answering two questions separately, because they are separate questions:

1. **What actually needs to run?** A dependency graph in Neo4j, queried with
   Cypher. Not path globs.
2. **Has this exact work already been done?** A Merkle content hash per target,
   not a commit SHA. This is why a rebase onto an unrelated `main` costs nothing.

Neither question is new, and neither is most of the answer — Bazel, Nx and
Turborepo all select from a graph and key reuse on content. [Prior
art](#prior-art) sets this against them, and says which part of it is actually
different.

The part that is different is what happens to the answer afterwards: the
selection and its rationale are recorded, so they can be handed to someone who
does not trust the machine that produced them. Both demo drivers end there — the
same run read back out of the graph as a JFrog Evidence predicate, in
[Two systems of record](#two-systems-of-record).

Orchestration is written in **Dang**, Dagger's native DSL. It was measured
against a Go SDK implementation first; that comparison and the reasons for
standardising on Dang are in
[docs/adr-002-go-vs-dang.md](docs/adr-002-go-vs-dang.md).

## Layout

| Path | What it is |
|---|---|
| `monorepo/` | The subject under test: a synthetic polyglot monorepo (4 TS packages, 3 Go packages, a proto contract generating both, infra, docs) |
| `tools/monograph/` | Go binary: extract the graph, load it, hash targets, select affected targets, record run results, emit the JFrog Evidence predicate |
| `ci/orchestrator-dang/` | Dagger module, Dang — the only orchestrator |
| `graph/` | Neo4j compose file, schema, apply script, and `queries/` |
| `bench/demo.sh` | Four-beat live demo: naive CI runs 9 targets for a docs typo, the graph runs 1, a shared-lib change runs 4, a repeat runs 0 — then reads that last run back as a JFrog Evidence predicate |
| `bench/demotui/` | The same four beats as a Bubble Tea program: a persistent neon pipeline diagram, live elapsed timer, colorized diffs, Dagger Cloud trace links (`w` opens the latest, or `--web` lets dagger open each one). Own Go module — run from inside it: `cd bench/demotui && go run .`. `c` shows the Cypher the current stage runs, read from `monograph queries` so it cannot drift from what executes; `e` shows the Evidence predicate the final step generated and the `jf evd create` command that would upload it. |
| `bench/run.sh` | Selects affected targets and runs them cold then warm |
| `bench/rebase-scenario.sh` | Proves a rebase onto an unrelated `main` costs nothing, and that a *relevant* change still re-runs |
| `bench/graph-vs-straight.sh` | Benchmarks graph-driven selection against straight Dagger; results in `bench/RESULTS.md` |
| `scripts/generate-local.sh` | Bootstrap gitignored generated code for host-side development |
| `.github/workflows/ci.yml` | Thin trigger: installs pinned Dagger and calls one function |
| `docs/adr-*.md` | Decisions and findings |

Dependencies are **derived, never declared**. A target's `monograph.toml` says
only how to build and test it; declaring `deps` there is a hard error. Edges come
from real Go imports, `package.json` workspace deps, `tsconfig` references and
`extends`, and `.proto` imports.

A manifest may declare `produces` — its *outputs*, not its inputs. That is what
lets generated code be produced in the pipeline instead of committed:

```toml
name = "proto"
image = "golang:1.26-alpine"
produces = ["proto/gen/go/**", "proto/gen/ts/src/**"]
codegenCmd = "sh ./proto/codegen.sh"
```

Files matching a `produces` glob are excluded from that target's content hash —
they are derivatives of its inputs, so hashing them would fold the output back
into its own cache key. The hash is therefore identical whether or not generated
code is present on disk.

### Pinned tool versions live inside the target

`monorepo/proto/protoc-gen-go.version` and `monorepo/proto/ts-proto.version` are
the single source of truth for those pins, read by both the orchestrator and
`scripts/generate-local.sh`. Neither restates them. Two properties follow from
putting them *inside* the target rather than in tooling config:

- Bumping the pin changes `proto`'s `targetHash` and therefore every consumer's
  — a toolchain upgrade correctly invalidates the code it generates. Verified.
- `codegen.sh` verifies each installed plugin matches its pin and exits 1 on
  mismatch, so host and pipeline cannot silently generate different code.

## Setup

Dagger is pinned to `v1.0.0-beta.11` in a repo-local `.bin/` — the system
`dagger` is left alone. See [docs/adr-001-dagger-version.md](docs/adr-001-dagger-version.md).

```bash
curl -fsSL https://dl.dagger.io/dagger/install.sh \
  | BIN_DIR="$PWD/.bin" DAGGER_VERSION=1.0.0-beta.11 sh

(cd tools/monograph && go build -o ../../.bin/monograph .)

docker compose -f graph/docker-compose.yml up -d
./graph/apply-schema.sh

# Generated code is gitignored and normally produced in-pipeline. Create it on
# the host so `go build ./...` and `go test ./...` work outside Dagger.
./scripts/generate-local.sh
```

## Use it

```bash
# Build the graph and load it
./.bin/monograph extract --repo=monorepo > graph.json
./.bin/monograph load --in graph.json

# What does a change actually affect?
./.bin/monograph affected --in graph.json --changed=libs/core/src/index.ts

# ...cross-checked against the Cypher query, which must agree
./.bin/monograph affected --in graph.json --changed=proto/user.proto --cross-check

# Deleting a path needs the tree from BEFORE the change, because nothing in the
# current tree can own a file that is gone. Record the index once per commit...
./.bin/monograph load --in graph.json --sha "$(git rev-parse HEAD)"
# ...then resolve deletions against it, with no second extract:
./.bin/monograph affected --in graph.json --base-sha "$BASE_SHA" --changed=pnpm-lock.yaml
# Or carry a real graph extracted at the base commit, which also has its edges
# and so handles a deleted *package*. See "Deletions need the base graph".
./.bin/monograph affected --in graph.json --base-in base.json --changed=pnpm-lock.yaml

# Run only what's affected. Any codegen the plan requires runs first, in
# dependency order, and its output is threaded into the test containers.
./.bin/monograph affected --in graph.json --changed=proto/user.proto > affected.json
./.bin/dagger call orchestrator-dang run --plan=affected.json --run-id=run-1

# What codegen does this plan require, and why?
./.bin/dagger call orchestrator-dang codegen-steps --plan=affected.json

# Export the tree with all generated code produced, without running any tests
./.bin/dagger call orchestrator-dang generated --plan=affected.json export --path=/tmp/generated

# Feed results back so the next selection can reuse them
./.bin/dagger call orchestrator-dang run --plan=affected.json --run-id=run-3 \
  | grep -E '^\{' > report.json
./.bin/monograph record --in report.json

# Re-select the same change: everything is now reusable, nothing runs
./.bin/monograph affected --in graph.json --changed=proto/user.proto

# Read that run back out of the graph as a JFrog Evidence predicate, and print
# the command that would sign and upload it. monograph does not run that command.
./.bin/monograph evidence --run-id run-3 > evidence.json
./.bin/monograph evidence --run-id run-3 --command --predicate-file evidence.json
```

## Running it as CI

The pipeline is a Dagger function, not provider YAML. `dagger call
orchestrator-dang ci` builds `monograph` from source in a container, extracts the
graph, loads it, selects, runs codegen and tests, and records the outcome — the
same command on a laptop and on a runner:

```bash
dagger call orchestrator-dang ci \
  --changed=libs/core/src/index.ts \
  --graph-uri="$NEO4J_URI" \
  --graph-password=env:NEO4J_PASSWORD

# Selection only, nothing executed or recorded
dagger call orchestrator-dang ci-plan --changed=... --graph-uri=... --graph-password=env:... contents
```

`.github/workflows/ci.yml` therefore only installs a pinned Dagger and computes
the changed-path list. That list is the one input that genuinely belongs to the
provider — a runner is the thing holding the git refs.

`ci` **exits non-zero when any target fails**, and records the outcome *before*
raising: a red run is history too, and dropping it would leave the graph unable
to answer flakiness questions about failures.

### The graph must be persistent

Reuse is a function of history: `reusable` is true only when a prior run recorded
the same `targetHash`. An ephemeral Neo4j service container per workflow run
would start empty every time, so nothing would ever be reusable and half the
design would silently stop working. Hence Aura.

Required repository secrets:

| Secret | Value |
|---|---|
| `NEO4J_URI` | `neo4j+s://<id>.databases.neo4j.io` |
| `NEO4J_PASSWORD` | instance password |

Locally, copy `.env.example` to `.env` (gitignored). With no `.env`, every script
falls back to the local Docker container, so `graph/apply-schema.sh` and
`graph/query.sh` work unchanged against either target. Every script resolves the
connection through one place, `graph/neo4j-env.sh`, whose precedence is real
environment > `.env` > local container.

`monograph` itself reads only the environment, not `.env` — a binary that parsed
dotfiles from its working directory would be a worse tool. So a bare invocation
that needs the graph (anything resolving `reusable`, and `record`) wants the
variables exported first:

```bash
set -a && . ./.env && set +a
```

Without that it falls back to `neo4j://localhost:7687` **and says so on stderr**.
It has to say so: if an unrelated Neo4j is listening on 7687, `reusable` would be
answered from a foreign database, and work would be skipped on false evidence.
`--no-reuse` needs no graph at all.

The password is passed as a Dagger `Secret` (`--graph-password=env:...`), so it
is never baked into a container's configuration, a cache key, or a trace.

## Ask the history layer questions

Recording runs is only worth it if the history is queryable. The queries are
plain `.cypher` files, so a new question is something you read and edit rather
than a code change:

```bash
./graph/query.sh              # list available queries
./graph/query.sh slowest      # scheduling: which targets are the long poles
./graph/query.sh flaky        # non-determinism: identical content, different verdicts
./graph/query.sh why          # the selection rationale, per run
./graph/query.sh coverage     # selected targets that neither ran nor were proven reusable
```

`flaky` is only definable because work is keyed by content hash. The usual proxy
— "this test name fails sometimes" — cannot separate a flaky test from one that
legitimately broke when the code changed. Grouping by `targetHash` controls for
that completely: every run in a group built byte-identical inputs with an
identical toolchain, so any disagreement is non-determinism.

`slowest` and `flaky` exclude cached runs. A replayed exec did not execute, so it
is neither evidence about determinism nor a real duration measurement.

### The selection is recorded, not just the outcome

`monograph record --plan <plan.json>` writes **why** each target was selected,
which the run report alone cannot say. Without it the graph knows what happened
and not what it was for: the plan's `resolutions` array — the audit trail
selection deliberately carries — lived in an ephemeral file that CI discarded, so
"why did `apps/admin` run in that run?" became unanswerable the moment the run
exited.

```bash
./.bin/monograph record --in report.json --plan affected.json --sha "$(git rev-parse HEAD)"
```

Three relationships, each answering a question the outcome data cannot:

| Relationship | Answers |
|---|---|
| `(CIRun)-[:SELECTED {reason, executed}]->(Target)` | the affected set, with `reason` separating a target whose own files changed from one reached transitively |
| `(CIRun)-[:CHANGED_PATH {path, how}]->(Target)` | which changed path caused it, and how that path classified |
| `(CIRun)-[:PROVEN_BY {targetHash}]->(TargetRun)` | for a skipped target, the earlier PASSED run that justifies the skip |

`PROVEN_BY` is the load-bearing one. It is a citation for work that did **not**
happen, which turns "we skipped it because it was already green" from an
assertion by the tool into a fact a third party can check.

Which means the skip has to actually be **recorded**, and for a while it was not:
both demo drivers recorded only the run that executed work, so a graph full of
demo history contained zero `PROVEN_BY` edges and could not evidence the reuse
its own beat 4 had just demonstrated on screen. Beat 4 now records its selection
with a report carrying no results — which is what "nothing executed" honestly
looks like — so the skip lands in the graph citing the run that proved it. Runs
recorded this way are `trigger: "selection-only"`, distinguishing a recorded
selection from a build.

Every `CIRun` also carries `createdAt`, stamped server-side on insert, which is
what makes "the latest run" askable. It replaced a `startedAt` copied out of the
run report that no orchestrator ever emitted: every run in the graph held
`startedAt: ""`, with an index on it advertising an ordering the data could not
provide. For an existing database, `graph/backfill-cirun-createdat.cypher`
recovers the timestamp from the epoch embedded in older run ids, and
`graph/prune-untimestamped-runs.cypher` removes the fossils it cannot — a NULL
here sorts **first** under `ORDER BY createdAt DESC`, so one missing timestamp
silently makes "latest" mean "oldest".

`why` reads all three relationships:

```text
run, target, reason, executed, viaPaths, reuseProvenBy
"demo-core-…", "apps/admin", "dependent", TRUE, [], NULL
"demo-core-…", "libs/core",  "changed",   TRUE, ["libs/core/src/index.ts"], NULL
```

`coverage` then checks the safety property this whole tool rests on, as a set
relation rather than an assurance:

```text
affected  ⊆  executed  ∪  proven-reusable
```

Every row it returns is a target the graph said was affected, which nothing ran,
and for which no earlier PASSED run with the same `targetHash` exists to justify
the skip — unverified work under a green build. It should normally be empty,
which is exactly why it is worth running. `TestRecordSelectionProvesSkips` pins
**both** halves: that a legitimate skip gets a citation, and that a skip on
content nothing ever built gets none. An empty `coverage` result only means
something if the query can detect a real violation.

This is also the shape [ADR-003](docs/adr-003-jfrog-integration.md) attests to,
and that constraint — a predicate must be a serialisation of this result rather
than a separately assembled claim, so the two cannot disagree — is why
`monograph evidence` reads the graph instead of the plan and report files it
already has on disk. See *Two systems of record* below.

### Two systems of record

The last step of both demo drivers reads a recorded run back out of the graph as
a JFrog Evidence predicate:

```bash
./.bin/monograph evidence --run-id <a recorded run>
```

RECORD therefore has two destinations, and they are not the same kind of thing:

| | Neo4j | JFrog Evidence |
|---|---|---|
| Holds | the whole history, all runs | one run, one document |
| Shape | mutable, cross-run, analytical | per-version, immutable, signed |
| Answers | `flaky`, `slowest`, "is this reusable?" | "did CI cover what this change touched?" |
| Audience | this pipeline | someone who does not trust this pipeline |

The predicate's load-bearing field is `coverageGaps`, which is `coverage.cypher`
carried inside the document: empty means `affected ⊆ executed ∪ proven-reusable`
held for that run. A skip with no citation appears in `skipped` with
`provenBy: null` *and* in `coverageGaps` — an attestation that quietly dropped
its own violations would be worse than none at all.

An AppTrust gate can verify that test evidence *exists*. It cannot verify the
tests that ran were *sufficient* for what changed, because at scale CI runs a
selected subset — the evidence is true and incomplete, and the gate cannot tell
the difference. That question needs a dependency graph of first-party source
targets, which is exactly what dies at the repo boundary.

**Not built: signing and upload.** `monograph evidence --command` prints the
`jf evd create` invocation and nothing runs it — the subject must be an artifact
in Artifactory and signing needs a key, and ADR-003 lists tier availability under
*not verified, therefore not designed on*. The command text comes from the tool
rather than from the demo, for the same reason `monograph queries` exists.

### What the graph does *not* model

`graph/schema.cypher` declares constraints only for labels the loader actually
creates: `Repo`, `Commit`, `File`, `Target`, `Team`, `CIRun`, `TargetRun`. Three
were removed from it because nothing populated them, and a schema that promises
structure it does not have is worse than one that admits the gap:

| Absent | What it would need |
|---|---|
| `:Test` / `[:COVERS]` | test-to-target mapping — either a per-package convention (cheap, coarse) or coverage instrumentation (accurate, much more work) |
| `:Artifact` | published build outputs; `produces` globs describe generated *source*, not published artifacts |
| `:Concept` / `:Semantic` | the graphify layer, which is not built — see *Not built* below |

So `tests-for-affected` is not implemented, and the plan's mention of it was
ahead of the code. Re-add each constraint in the same commit that starts writing
its nodes.

## Verify

```bash
# Golden affected-set tests, plus the Cypher/in-memory cross-check
(cd tools/monograph && go test ./...)

# Select and run, cold then warm, with per-target durations
./bench/run.sh proto/user.proto

# Hermetic: every image and git ref resolves from dagger.lock. Since
# v1.0.0-beta.10 locking is pinned by default and there is no --lock flag; a run
# that had to resolve something live records it, so a clean lockfile is the
# check. It has to be ./.bin/dagger — a beta.10 client does not recognise
# beta.11's `oci-sha`/`git-sha` entries, so it resolves live and appends its own
# `container.from` line, which looks identical to a clean run until you diff
./.bin/dagger call orchestrator-dang run --plan=affected.json
git diff --exit-code dagger.lock

# The headline claim, demonstrated end to end against real git history
./bench/rebase-scenario.sh

# The attestation is a serialisation of the graph, so it can be checked against
# the query it claims to carry: both should agree on the gaps.
./graph/query.sh coverage
./.bin/monograph evidence --run-id "$(./graph/query.sh why | sed -n 2p | cut -d'"' -f2)" | jq .coverageGaps
```

### Running the tests

Most of `tools/monograph`'s tests are pure — in-memory graphs, no I/O — and just
run. A minority need a real Neo4j to talk to, because they pin behaviour that
only exists as a round trip through the database: `RecordCommitFiles` /
`BaseGraphAtCommit` (the `--base-sha` file index), `RecordRun` / `RecordSelection`
(cache-hit derivation, selection provenance), and the CLI tests in
`basesha_cli_test.go` that drive `cmdLoad` / `cmdAffected` end to end.

```bash
# With a real Neo4j (Aura or local docker via `docker compose -f graph/docker-compose.yml up -d`):
set -a && . ./.env && set +a
(cd tools/monograph && go test ./... -v)

# Without one: DB-dependent tests skip themselves (t.Skipf("Neo4j unavailable"))
# rather than failing, so `go test ./...` is still safe to run with no setup.
(cd tools/monograph && go test ./...)

# Force the skip explicitly, e.g. in an environment where a *stray* local Neo4j
# on :7687 would otherwise get used by mistake:
MONOGRAPH_SKIP_NEO4J=1 go test ./...
```

Every DB-dependent test creates data under a name keyed to its own PID
(`test-hash-<pid>`, `testsha-cli-<pid>`, …) and deletes it in `t.Cleanup`, so
tests are safe to run concurrently against the same shared instance and leave
nothing behind on success. A failed cleanup logs rather than fails the test —
check `t.Logf` output if the graph accumulates stray `Commit`/`TargetRun` nodes
over time.

Run one scenario's tests by name instead of the whole suite:

```bash
go test ./... -run TestGoldenAffectedSets -v        # the golden affected-set table
go test ./... -run TestPathResolution -v            # changed-path classification
go test ./... -run 'Base|CommitFileIndex|Vanished'  # --base-in / --base-sha, see below
go test ./... -run TestRecordSelectionProvesSkips   # the coverage safety property
```

### The rebase scenario

`bench/rebase-scenario.sh` clones the repo to a throwaway workspace (real
history untouched) and runs two scenarios with **opposite** expected outcomes,
because a demo that only ever answers "reuse" would pass trivially:

| | Scenario 1 | Scenario 2 (control) |
|---|---|---|
| `main` moves with | an unrelated docs typo | a shared `tsconfig.base.json` change |
| Commit SHA after rebase | changes | changes |
| Target hashes | **all identical** except `docs` | 9 targets move, incl. `libs/core` |
| SHA-keyed CI would run | 4 targets | 4 targets |
| Content-keyed CI runs | **nothing** | 4 targets |

The script fails loudly if an unexpected hash moves, if a required one does not,
or if the rebase somehow left the SHA unchanged.

> The difference is not the rebase. It is what the cache key is made of.

### Changed-path resolution is strict

`--changed` accepts paths **relative to the monorepo root**, and every path is
classified rather than guessed at. The plan carries a `resolutions` array so a
selection is auditable:

| Input | Resolution | Selects |
|---|---|---|
| `libs/core/src/index.ts` | `file` | `libs/core` |
| `docs` | `directory` | `docs` |
| `libs` | `directory` | `libs/core`, `libs/ui`, `libs/authz` |
| `libs/core/` | `directory` | `libs/core` — a trailing slash is cleaned, not guessed at |
| `libs/core/src/gone.ts` | `deleted` | `libs/core` — a specific target root prefixes it |
| `docs/../libs/core/src/index.ts` | `file` | `libs/core` — paths are `path.Clean`ed before matching |
| `apps/web/tsconfig.tsbuildinfo` | `ignored` | nothing — build sidecars never trigger CI |
| `libs/coer/src/index.ts` | `unresolved` | **error** |
| `monorepo/libs/core/src/index.ts` | `unresolved` | **error** |
| `REDME.md` | `unresolved` | **error** — see *no top-level catch-all* below |
| `../etc/passwd`, `/libs/core/x.ts`, `.` | `unresolved` | **error** |

An unresolvable path is a hard error (`--allow-unknown-paths` downgrades it to a
warning). This is deliberate: the earlier implementation had a single prefix
fallback that quietly resolved *anything* unrecognised to the root `workspace`
target, so a typo — or the repo-root-relative paths `git diff --name-only` emits
without `--relative` — selected all 7 targets while looking like a successful
narrow run. Silently selecting everything defeats the tool, and it fails unsafely
in the other direction too: you could believe a narrow test ran.

That bug was live in `--sha`, which is why `gitChangedFiles` now passes
`--relative`. It also passes **`--no-renames`** and **`-z`**, both for the same
class of reason:

- With rename detection on, `git diff --name-only` prints only the
  *destination* of a rename. Moving a file between targets therefore dropped the
  **source** target from the plan while its content hash had moved — broken code,
  never tested, exit 0. `--no-renames` reports the delete/add pair instead.
- `-z` NUL-terminates paths and disables git's path munging. Without it a
  filename containing a comma was shredded by the comma-separated split, and a
  non-ASCII filename arrived C-quoted (`"\303\251..."`) and matched nothing,
  failing the whole selection over one file.

Symlinks are hashed by their destination path, the same content git stores for
them. Skipping them left a repointed symlink with an identical `targetHash`, so
the target was reported `reusable` and never ran.

#### There is no top-level catch-all

Rule 3 (`deleted`) resolves a path only when a *specific* target root prefixes
it. It used to also resolve any path with no slash in it to the root `workspace`
target, reasoning that a root-level file genuinely does belong to `workspace`.
The reasoning was sound; the consequence was not. A bare typo (`REDME.md`) is
indistinguishable from a real root-level file, so it resolved to `workspace` and
fanned out to every compiled target — exit 0, no warning, a plan that reads like
a narrow run. That is the original bug this classification exists to prevent,
surviving in the one place the catch-all still lived.

A root-level file that really exists is caught by rule 1, because `extract`
indexes it — `tsconfig.base.json`, `pnpm-lock.yaml` and `go.mod` all still
resolve to `workspace` and select every compiled target.

### Deletions need the base graph: `--base-in`

Rules 0–3 read the graph extracted from the **post-change** tree. That is correct
for additions and structurally cannot work for deletions:

- **Adding** a top-level file needs no special handling. `extract` runs after the
  change, so the new file is indexed and rule 1 owns it. The only way to break
  this is reusing a stale `graph.json` from before the file existed.
- **Deleting** a top-level file leaves nothing to attribute. It is in no index
  and under no surviving target root, so it is `unresolved` — a hard error, or
  with `--allow-unknown-paths` a plan that selects **nothing** while
  `pnpm-lock.yaml` disappearing genuinely affects every compiled target.

So additions need the HEAD graph, deletions need the base graph, and one
`extract` cannot serve both. There are two ways to supply the second one.

**`--base-sha` — ask the graph.** If `load --sha` ran at the base commit, its
file index is already recorded and no second extract is needed:

```bash
# once, at the base commit (CI does this on every merge to main)
./.bin/monograph load --in graph.json --sha "$(git rev-parse HEAD)"

# later, on a branch that deletes something
./.bin/monograph affected --in graph.json --base-sha "$BASE_SHA" --changed=pnpm-lock.yaml
```

This is the version stored on `:FileVersion`, a label deliberately distinct from
`:File`. `:File` is the *current* snapshot that `load` deletes and rewrites, so it
stays one node per path — which is what keeps `MATCH (f:File {repo, path})` in
`AffectedViaCypher` single-valued. Folding history into it would have broken the
one query `TestCypherMatchesInMemory` guards, so history lives beside it:

```text
(Commit)-[:CONTAINS {targetName}]->(FileVersion {path, sha256})
```

Nodes dedupe on `(path, sha256)`, so growth tracks churn rather than
commits × files. `targetName` is on the relationship because ownership can change
without content changing — a new nested `monograph.toml` re-parents files whose
bytes never moved.

**`--base-sha` resolves a deleted file but refuses a deleted package.** Only the
file index is versioned, not the dependency edges, so it can attribute
`libs/ui/src/index.ts` to `libs/ui` but cannot reach `apps/web` and `apps/admin`,
which consumed it and are now broken. Left alone that produces an *empty* plan —
broken consumers reported as nothing to do — so it errors instead and names the
alternative. `--allow-unknown-paths` downgrades it, and `--base-in` handles the
case properly.

**`--base-in` — carry a real extracted graph.** Slower, and complete: it has the
edges, so a deleted package resolves to its surviving consumers.

```bash
# Graph as of the base commit, in a throwaway worktree so HEAD is untouched.
git worktree add -q /tmp/base "$BASE_SHA"
./.bin/monograph extract --repo=/tmp/base/monorepo > base.json
git worktree remove /tmp/base

# ...and the ordinary HEAD graph.
./.bin/monograph extract --repo=monorepo > graph.json

./.bin/monograph affected --in graph.json --base-in base.json \
  --changed=pnpm-lock.yaml
```

| Change | No base | `--base-sha` | `--base-in` |
|---|---|---|---|
| Delete top-level `pnpm-lock.yaml` | **error**, or 0 targets with `--allow-unknown-paths` | **9 targets**, `how: deleted` | **9 targets** |
| Delete the whole `libs/ui` target | **error** / 0 targets | **error** — edges not versioned | `apps/admin`, `apps/web` |
| Typo `REDME.md` | **error** | **error** | **error** |
| Base commit never recorded | — | **error**, names the fix | n/a |
| Ordinary add or modify | 4 / 1 / 8 for core / docs / proto | identical | identical |

The property that makes this safe rather than a re-introduced catch-all: **a real
deletion appears in the base index; a typo appears in neither.** So `--base-in`
buys deletion support without giving back rule 3's strictness.

Each row above is pinned by a test, at two levels: the library function directly,
and — because the flag validation and error text live in `commands.go`, not in
the functions underneath — the CLI entry point itself.

| Row | Library-level test | CLI-level test |
|---|---|---|
| Delete top-level file, no base | `TestBaseGraphResolvesDeletions` | `TestCmdLoadShaThenAffectedBaseShaResolvesDeletion` (the "without" half) |
| Delete top-level file, `--base-sha` | `TestCommitFileIndexRoundTrip` (write/read) | `TestCmdLoadShaThenAffectedBaseShaResolvesDeletion` |
| Delete top-level file, `--base-in` | `TestBaseGraphResolvesDeletions` | — (no CLI test yet; the plumbing is identical to `--base-sha` past `BuildPlanWithBase`) |
| Delete whole package, `--base-sha` (refuses) | `TestVanishedTargetsDetectsDeletedPackage` | `TestCmdAffectedBaseShaRefusesDeletedPackage` (incl. `--allow-unknown-paths`) |
| Delete whole package, `--base-in` (resolves) | `TestVanishedTargetsDetectsDeletedPackage` | — |
| Typo, with or without a base | `TestPathResolution`, `TestBaseGraphResolvesDeletions` | — |
| Base commit never recorded | `TestCommitFileIndexRoundTrip` (`ok=false`) | `TestCmdAffectedBaseShaUnrecorded` |
| `--base-in` and `--base-sha` together | — | `TestCmdAffectedRejectsBothBaseFlags` |
| `load --sha` actually reaches Neo4j | — | `TestCmdLoadShaRecordsFileIndex` |
| Ordinary add/modify, identical with a base present | — | `TestOrdinaryChangeIdenticalWithOrWithoutBase` |

Run just this feature's tests:

```bash
set -a && . ./.env && set +a   # live Neo4j required — see "Running the tests" below
(cd tools/monograph && go test ./... -run 'Base|CommitFileIndex|VanishedTargets' -v)
```

Two consequences worth stating:

- The dependents walk unions HEAD's edges with the base graph's. Without that,
  deleting `libs/ui` leaves HEAD with no edges mentioning it, so nothing
  propagates and `apps/web` — now broken — is never selected. The union
  over-approximates: an edge deliberately removed in HEAD still propagates for
  that run. Safe direction, and in practice the dependent's own source had to
  change for the import to go away, so it would be selected anyway.
- A selected target that no longer exists in HEAD is **omitted from the plan**,
  not zero-valued. It has no image, command or hash, and per
  [ADR-002](docs/adr-002-go-vs-dang.md) Dang rejects a target with a missing
  required field. It still appears in `changedTargets` for the audit trail.

### The golden cases

Pinned in `tools/monograph/affected_test.go`, correct by construction from the
monorepo's shape:

| Change | Runs | Proves |
|---|---|---|
| `docs/README.md` | the docs markdown lint, nothing else (~0.7s) | precision: the one relevant check, not 7 test suites and not nothing |
| `docs/.markdownlint.jsonc` | the docs markdown lint | build configuration is content — editing lint rules re-lints |
| `libs/core/**` | `libs/core, libs/ui, apps/web, apps/admin` | transitive fan-out, incl. `apps/admin` which reaches core only via `ui` |
| `proto/user.proto` | 3 Go + 4 TS targets | one IDL change crosses the language boundary |
| `services/billing/**` | `services/billing` only | no false positives on a leaf |
| `infra/main.tf` | `infra` only | the extractor invents no edges into the code graph |
| `tsconfig.base.json` | every compiled target, but **not** `docs` or `infra` | shared toolchain config is reached through real `tsconfig`/`go.mod` references |

Two properties of the model are also pinned: `proto` appears in an affected set
while contributing no work (affected and *runnable* are different questions), and
a deleted file still selects its target via prefix fallback.

The docs lint is a real check, not a placeholder — a malformed heading in
`docs/README.md` makes the `docs` target report `FAILED`.

## Prior art

Graph-derived selection and content-addressed reuse are table stakes. Bazel has
done both since 2015; Nx and Turborepo ship them as their headline feature.
Stating that first is not modesty — anyone evaluating this already has one of
those tools in mind, and a claim of novelty where there is none would put every
other claim here in doubt.

| Family | Examples | Dependencies | Granularity | Reuse key |
|---|---|---|---|---|
| Hermetic build systems | Bazel, Buck2, Pants, Please, Mill | declared in a build dialect; Pants infers them | action / source file | content hash of inputs + toolchain, remote cache |
| Diff-to-target determinators | [`target-determinator`](https://github.com/bazel-contrib/target-determinator), `bazel-diff`, [Aspect Workflows](https://docs.aspect.build/workflows/features/delivery/) | inherited from the build system | target | a pair of git revisions, cached across runs |
| JS/TS task runners | [Nx](https://nx.dev/docs/guides/comparisons/nx-vs-turborepo), Turborepo, moon, Rush, Lage | inferred from imports, `package.json`, `tsconfig` | package | hash of declared task inputs, remote cache |
| Path filters | `dorny/paths-filter`, CircleCI path filtering, Buildkite `monorepo-diff` | none — globs | whatever the glob says | none |
| Probabilistic test selection | [Develocity PTS](https://docs.gradle.com/develocity/2026.1/using-develocity/predictive-test-selection/), [Launchable](https://help.launchableinc.com/features/predictive-test-selection/), [Datadog Test Optimization](https://www.datadoghq.com/about/latest-news/press-releases/datadog-introduces-intelligent-test-runner-to-help-developers-reduce-the-time-to-deploy-application-changes/) | learned from run history | individual test | none — it predicts, it does not prove |

**Hermetic build systems** are strictly finer-grained than this repo. Bazel and
Buck2 model dependencies per action; Pants infers most of them by static analysis
so BUILD files stay near-empty, which is the same argument `manifest.go` makes by
rejecting a `deps` key. Pants is therefore the closest prior art for the
*derived, never declared* claim specifically, and the honest reading is that this
repo reproduces that principle for a polyglot tree without a build dialect — not
that it invented it.

**Determinators** exist because Bazel itself has no built-in affected query, so
`bazel query rdeps` gets wrapped. `target-determinator` takes a before-revision
and lists what changed since; it caches its cquery results across runs so a
repeated commit skips the hashing entirely, which is structurally the same move
as `MarkReusableQuery` here. It is also known to be slow, which is the cost of
computing the answer from two full graph evaluations rather than from a stored
one. `bazel-diff` hashes the graph at both commits and diffs the hashes — the
same Merkle scheme as `hash.go`, over Bazel's graph instead of a derived one.

**JS/TS task runners** model dependencies at the **package** level, which is
exactly the granularity here — see *Known over-approximation* above. Nx's
`affected` and this repo's `affected` answer the same question the same way. The
positioning that survives contact with Nx is "polyglot and queryable", not "more
precise".

**Path filters** are the approach the first line of this README rejects. They are
also, by a wide margin, what most monorepos on GitHub Actions actually run.

**Probabilistic test selection** answers a different question: not *what does
this change reach* but *which tests are likely to fail*, learned from history.
Develocity offers Conservative/Standard/Fast profiles that trade savings against
the chance of missing a real failure; Datadog collects per-test coverage and
skips tests no changed line touches. Google's TAP, Meta's internal systems,
Uber's SubmitQueue and Azure DevOps Test Impact Analysis are the in-house
versions. This is the category `:Test`/`[:COVERS]` would enter, and the reason it
is deliberately unpopulated (*What the graph does not model*): reachability is a
safety property, and a prediction is not. Adding a probabilistic layer on top of
a proven one is defensible; replacing the proven one with it is not.

**Adjacent, same idea in another domain:** dbt's `state:modified+` is reverse
reachability over a data DAG, and is the clearest evidence that this shape
generalises beyond code.

### What is actually different here

Three things, and only the third is unusual.

1. **Derived, polyglot, no build dialect.** Go imports, pnpm workspaces,
   `tsconfig` references and `.proto` imports, with `deps` in a manifest a hard
   error. Pants does this; Bazel and Buck2 do not. Not unique — but it is the
   harder half of that trade-off, and the reason adopting this costs no BUILD
   file migration.
2. **Deletion treated as a distinct failure mode.** `--base-in` vs `--base-sha`,
   and `VanishedTargets` **refusing** rather than emitting an empty plan when a
   whole package is gone. Determinators get this free because they evaluate two
   revisions anyway; the package-level runners largely under-select and stay
   quiet about it.
3. **The selection rationale is persisted and queryable.** Nx Cloud, Develocity
   and BuildBuddy all store run history — as a product surface, behind their own
   dashboards and query languages. None of them will hand over
   `(CIRun)-[:PROVEN_BY]->(TargetRun)` as a citation for work that did *not*
   happen, or let a third party write `coverage.cypher` and check
   `affected ⊆ executed ∪ proven-reusable` as a set relation — or hand that
   relation over as a signable document (*Two systems of record* above). `flaky`
   keyed on `targetHash` rather than test name is the same category of thing, and
   is definable only because work is content-addressed.

So the claim worth making is not *this selects better*. Selection is table
stakes, and every tool above treats **why it selected** as ephemeral telemetry.
This treats it as evidence — which is the whole reason
[ADR-003](docs/adr-003-jfrog-integration.md) can propose attesting to it.

## Status

Working and verified: the graph, selection (both in-memory and via Cypher, with
a cross-check test), Merkle target hashing, the Dang orchestrator with
per-target durations and in-pipeline codegen, run recording, reuse,
pinned lockfile resolution, and the rebase scenario. Plus the JFrog Evidence
predicate (`monograph evidence`), **generation only** — signing and upload are
not built, and nothing in this repo runs `jf`.

On durations: `monograph record` derives `cacheHit` from the graph rather than
trusting the orchestrator, which cannot know whether the engine replayed its
exec. A cached target is recorded with `cacheHit = true` and a **null** duration,
because a replayed exec's reported time is the earlier execution's number. Only
genuinely fresh work carries a measurement, so slow-test and flakiness analysis
can trust the field. See
[docs/adr-002-go-vs-dang.md](docs/adr-002-go-vs-dang.md).

Expect `cacheHit` to be **false on every row**, and read that as the system
working rather than as the field being broken. It is true only when the same
`targetHash` is executed and recorded twice — which is precisely what selection
exists to prevent, since a target whose content already passed is skipped before
it can run. So it reads as a "reuse leaked" detector, not a cache-hit rate; the
way to see a `true` is `affected --no-reuse`, which is how ADR-002 demonstrated
it. The number people usually want instead — how much work was skipped — is on
`SELECTED {executed: false}` and `PROVEN_BY`.

## Not built

Each item below is **absent from this repo**. Where a design is sketched, treat
it as intent, not as a description of code that exists.

- **graphify semantic layer — NOT BUILT.** No `graphify-out/`, no `:Semantic` or
  `:Concept` nodes, no code that creates them. Deliberately skipped. *If* added,
  it would go on distinct labels with `provenance = 'graphify'`, so inferred
  edges could never be mistaken for parsed ones.
- **`tests-for-affected` query — NOT BUILT.** Needs the test-to-target mapping
  described under *What the graph does not model*. The other two history queries
  (`flaky`, `slowest`) **are** built and working.
- **Splitting `proto` into three targets — NOT BUILT.** Would close the last of
  the target-granularity over-approximation described above. Optional; current
  behaviour is safe, just coarse.
- **`Toolchain` interface — NOT BUILT.** Toolchain setup is a 6-arm `case` switch
  in the orchestrator. Worth extracting only once toolchains outgrow it.
- **`--base-in` wired into the pipeline — NOT BUILT.** The flag exists and is
  tested; nothing calls it. See the sketch immediately below.
- **Signing and uploading the Evidence predicate — NOT BUILT.** The predicate
  itself *is* built (`monograph evidence`, and both demo drivers end on it). What
  is absent is any call to `jf`: the subject must be an artifact in Artifactory
  and signing needs a key, and [ADR-003](docs/adr-003-jfrog-integration.md) lists
  tier availability under *not verified, therefore not designed on*. A branch
  that uploaded when `jf` happened to be on PATH would be designing on it.
- **The Dagger Cloud trace URL inside the predicate — NOT BUILT.** It would tie
  this attestation to JFrog's own [Dagger
  integration](https://jfrog.com/integrations/dagger/), which attests *how* an
  artifact was built while this attests *what was covered*. Needs
  `CIRun.traceUrl`, so a trace field through the orchestrator's report contract;
  `bench/demotui` already parses the URL, so the gap is the report.

### Sketch: `--base-in` in `ci.yml` and the Dang `ci` function

**Not built.** `--base-in` is available on the CLI only, so a pull request that
deletes a top-level file (or a whole package) currently fails CI with an
unresolved-path error. Below is what wiring it would look like, recorded as
intent so the shape is agreed before the code moves.

The runner already has what is needed. `actions/checkout` uses `fetch-depth: 0`
and the workflow already computes `$BASE`, so a second worktree at that SHA costs
a checkout, not a clone:

```yaml
- name: Graph at the base commit
  if: steps.changed.outputs.changed != ''
  run: |
    git worktree add -q /tmp/base "${{ steps.changed.outputs.base }}"
```

On the Dang side the work is confined to one helper. `graphTool` already holds the
monorepo and the credentials, and `planFor` already runs `extract`/`affected`
inside it; the change is a second extract and one more flag:

```dang
let planFor(tool: Container!, changed: String!, baseSource: Directory!): File! {
  tool
    # The base tree arrives as a second Directory, mounted alongside the
    # working one. Two extracts, one selection.
    .withMountedDirectory("/src/base", baseSource)
    .withExec(["sh", "-c", "monograph extract --repo=/src/base/monorepo > base.json"])
    .withExec(["sh", "-c", "monograph extract --repo=monorepo > graph.json"])
    .withExec(["sh", "-c",
      `monograph affected --in graph.json --base-in base.json --changed="$1" --cross-check > plan.json`,
      "monograph-ci", changed])
    .file("plan.json")
}
```

Three things to decide before building it, none of them cosmetic:

- **Cost.** A second `extract` per run, plus uploading a second copy of the
  monorepo into the engine. On this repo that is cheap; on a large monorepo the
  source upload is the expensive part, and `--base-in` would pay it twice on
  *every* run to serve the minority of runs that delete something. Making the
  base extract conditional on the plan actually failing to resolve would avoid
  that, at the cost of a two-pass pipeline.
- **`ci` currently takes `--changed`, not a base ref.** Threading a base through
  means either a new `baseSha` argument or having `ci` do its own `git diff`,
  which would move provider-specific work into the orchestrator — precisely what
  `.github/workflows/ci.yml` exists to keep out. The changed-path list belongs to
  the runner; a base *tree* is a different kind of input and arguably belongs to
  the module.
- **The cheaper alternative is the graph itself.** The previous run already
  loaded its `File` nodes into Neo4j, and *The graph must be persistent* above
  argues that store has to exist anyway. A `--base-from-graph` reading the last
  recorded file index would need no second extract and no second upload — but it
  makes correctness depend on Aura being populated for the base commit, which is
  not true for a first run or a fresh branch. `--base-in` was built first
  deliberately: it is testable offline and has no such dependency.

### Known over-approximation: target-level granularity

Selection resolves a changed *file* to its owning *target*, then walks target
edges. It does not track file-to-file dependencies, and one `monograph.toml` at
`monorepo/proto/` owns the IDL and both generated trees.

**The main case is fixed.** Both languages' bindings are now generated inside the
pipeline and gitignored, so there is no committed generated source to edit
spuriously at all. `git ls-files monorepo/proto/gen/` returns only
`package.json` and `tsconfig.json` — workspace-membership declarations, which
genuinely *should* affect consumers.

**A smaller case remains**, and it is worth stating rather than glossing:

```text
edit proto/ts-proto.version       -> also selects libs/authz, services/api, services/billing
edit proto/protoc-gen-go.version  -> also selects libs/core, libs/ui, apps/web, apps/admin
```

Per-language config inside the proto target fans out to *both* languages, because
everything under `proto/` is one target. Bumping the TypeScript generator
therefore re-runs the Go services. Closing this would mean splitting `proto` into
three targets — IDL, gen-go, gen-ts — with the generated targets depending on the
IDL. That is a real design change, not a tweak, and the current behaviour still
**over**-approximates, which is the safe direction for CI: it cannot miss a
dependent.
