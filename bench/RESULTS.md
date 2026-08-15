# Graph-driven CI vs straight Dagger vs Jenkins

Reproduce with `./bench/graph-vs-straight.sh` (add `--counts` for phase 1, which
needs no container runtime).

## The four arms

| Arm | Selection | Content cache | Safe? |
|---|---|---|---|
| `jenkins-all` | every target, every commit | none | yes |
| `jenkins-paths` | `when { changeset "thatdir/**" }` per stage | none | **no** |
| `straight` | every target, every run, hand-maintained list | yes — Dagger replays unchanged work | yes |
| `graph` / `graph+reuse` | derived affected set; `+reuse` also skips content that already passed | yes | yes |

`straight` and both graph arms share the same execution machinery
(`withGeneratedCode` / `prepared` / `testOne` in `ci/orchestrator-dang`), so the
only variable between them is selection. The Jenkins arms are modelled, not run:
they describe what a declarative pipeline would select, and their defining
property is the *absence* of an engine-level content-addressed cache.

`straight` is modelled on how Dagger itself is built — modules with `@check`
functions and hand-curated selection, as in `toolchains/test-split`.

## Phase 1a — targets selected

| Change | jenkins-all | jenkins-paths | straight | graph |
|---|---|---|---|---|
| `docs/README.md` | 9 | 1 | 9 | 1 |
| `docs/.markdownlint.jsonc` | 9 | 1 | 9 | 1 |
| `infra/main.tf` | 9 | 1 | 9 | 1 |
| `services/billing/main.go` | 9 | 1 | 9 | 1 |
| `libs/ui/src/index.ts` | 9 | 1 | 9 | 3 |
| `libs/core/src/index.ts` | 9 | 1 | 9 | 4 |
| `proto/user.proto` | 9 | **0** | 9 | 7 |
| `tsconfig.base.json` | 9 | **0** | 9 | 7 |
| **Total target runs** | **72** | **6** | **72** | **25** |

## Phase 1b — safety: what `jenkins-paths` silently skips

| Change | Missed | Dependents never tested |
|---|---|---|
| `libs/ui/src/index.ts` | 2 | `apps/admin`, `apps/web` |
| `libs/core/src/index.ts` | 3 | `apps/admin`, `apps/web`, `libs/ui` |
| `proto/user.proto` | 7 | all Go and TS consumers |
| `tsconfig.base.json` | 7 | all compiled targets |
| **Total** | **19** | |

`jenkins-all`, `straight` and both graph arms miss **nothing**.

The two zero rows are the worst outcome in the whole comparison. A change to the
shared IDL, or to the root `tsconfig`, fires **no stage at all** — because
neither directory owns a test — while seven targets are genuinely affected. The
build goes green having tested nothing that the change could break. That is not a
slow pipeline, it is a false pass.

`jenkins-paths` is cheapest on paper (6 target runs versus the graph's 25) purely
because it is skipping work it should be doing. Comparing its cost against the
others without the safety table is meaningless.

A real Jenkinsfile can of course be written with broader filters — `libs/**`
triggering every library stage. That trades the correctness bug for the cost of
`jenkins-all`, and it has to be re-reasoned by hand every time the dependency
structure changes. Both failure modes are real; neither is fixed by trying harder
at globs.

## The finding that surprised me

**`jenkins-all` and `straight` select identically — 72 target runs each — yet
`straight` is far cheaper, with no graph involved.** The difference is entirely
Dagger's content-addressed cache replaying unchanged targets. Phase 2 measures
this directly: nine targets on a warm engine cost the same ~1.7s as one.

So most of the speed people chase with "affected target selection" is already
available from caching alone. If you are on Jenkins today, moving to plain Dagger
is the larger, simpler win, and it requires no dependency graph, no Neo4j, and no
new concepts.

That leaves the graph with a narrower and more honest claim. It contributes:

1. **Not even considering unaffected targets** — no source upload, no image pull,
   no cache lookup for the 8 targets a docs change cannot affect. Caching still
   pays those costs, just cheaply.
2. **Safety where path filters have none** — the 19 missed dependents above.
3. **Cross-run reuse keyed by content** rather than commit SHA, which is what
   makes a rebase free (see `bench/rebase-scenario.sh`).
4. **Derivation instead of maintenance** — see below.

## How to read the counts honestly

**They measure work selected, not time.** Phase 2 measures time.

**The spread matters more than any average.** The graph avoids 88% of target runs
for a docs typo and 22% for a root `tsconfig` change. A graph does not make CI
uniformly faster; it makes cost proportional to the change. Quoting only the
docs row would be cherry-picking.

**The scenario set is hand-picked and unweighted.** It is not a prediction for
any real repository's change distribution.

## Phase 2 — wall clock, WARM engine

Medians of 3 trials, after an untimed full `straight` pass so no arm benefits
from another's cold work.

| Change | straight | graph | graph+reuse |
|---|---|---|---|
| `docs/README.md` | 1651ms | 1828ms | 1780ms |
| `docs/.markdownlint.jsonc` | 1657ms | 1659ms | 1668ms |
| `infra/main.tf` | 1710ms | 1752ms | 1620ms |
| `services/billing/main.go` | 1560ms | 1638ms | 1577ms |
| `libs/ui/src/index.ts` | 1724ms | 1850ms | 1623ms |
| `libs/core/src/index.ts` | 1583ms | 1601ms | 1573ms |
| `proto/user.proto` | 1808ms | 1663ms | 1583ms |
| `tsconfig.base.json` | 1661ms | 1804ms | 1624ms |

**On a warm engine, selection buys nothing measurable.** Every arm lands at
~1.6–1.8s whether it selects 1 target or 9. Fixed overhead — engine connect,
workspace load, source upload, module load — dominates completely, and the target
work itself is replayed from cache to near-zero. Some graph rows are *slower*
than `straight`, which is noise, not a real regression.

This is the strongest evidence in the benchmark against the graph, and it is
worth stating first rather than burying: if your Dagger engine is persistent and
warm, a dependency graph will not make your CI faster.

## Phase 3 — wall clock, COLD engine

The engine cache is pruned before every run, which is the state a fresh CI runner
starts in. Single measurement per row (each needs its own prune), so read these as
orders of magnitude.

| Arm | Targets | Cold wall | vs straight |
|---|---|---|---|
| graph (`docs/README.md`) | 1 | 13,986ms | **3.7× faster** |
| graph (`libs/core/src/index.ts`) | 4 | 33,918ms | 1.5× faster |
| graph (`proto/user.proto`) | 7 | 39,877ms | 1.3× faster |
| `straight` (all) | 9 | 51,495ms | — |

**On a cold cache, selection is worth 3.7× for a narrow change**, and the benefit
decays as the change touches more of the graph — exactly tracking the phase-1
counts.

Two things to notice:

- **Cost scales with what you select**, roughly 4.7s per additional target beyond
  the floor. This is the graph's mechanism working as advertised.
- **There is a large fixed floor.** Even selecting a single target costs 14s, of
  which most is engine bootstrap, module build and source upload. The graph cannot
  optimise that away, so a one-target run is not 1/9th of a nine-target run.

## The two regimes are the actual answer

| | Warm engine | Cold engine (fresh runner) |
|---|---|---|
| Selection's wall-clock value | **none** | **up to 3.7×** |
| What still justifies the graph | safety, derivation, content-keyed reuse | all of that, plus real speed |

Neither number alone is honest. Ephemeral runners are the common case, which
favours the graph; a persistent engine or remote cache is the standard
optimisation, which erases its speed advantage. Anyone quoting "3.7× faster CI"
from this without naming the cache regime is misrepresenting it.

## The cost no timing can show

`allTargets` in `ci/orchestrator-dang/main.dang` is nine hand-written entries,
and a `jenkins-paths` setup is a hand-written filter per stage. Add a target to
the monorepo and both must be edited, or the target is silently never tested.
Nothing fails; coverage just quietly stops. `jenkins-paths` additionally needs
its filters re-reasoned whenever the dependency structure changes.

The graph derives the same list from real imports, so a new target is picked up
because it exists, not because someone remembered. That appears in no wall-clock
measurement, and for a monorepo that grows it is plausibly the larger effect.
