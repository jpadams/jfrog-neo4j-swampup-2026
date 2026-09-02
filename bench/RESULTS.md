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
this directly: nine targets on a warm engine cost the same ~0.9s as one.

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
| `docs/README.md` | 785ms | 767ms | 867ms |
| `docs/.markdownlint.jsonc` | 785ms | 783ms | 778ms |
| `infra/main.tf` | 881ms | 853ms | 868ms |
| `services/billing/main.go` | 781ms | 804ms | 796ms |
| `libs/ui/src/index.ts` | 1001ms | 956ms | 811ms |
| `libs/core/src/index.ts` | 786ms | 848ms | 794ms |
| `proto/user.proto` | 780ms | 847ms | 796ms |
| `tsconfig.base.json` | 788ms | 781ms | 790ms |

**On a warm engine, selection buys nothing measurable.** Every arm lands at
~0.8–1.0s whether it selects 1 target or 9. Fixed overhead — engine connect,
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

Recorded twice, because one measurement per prune is not enough to tell a real
effect from a slow afternoon. The two runs agree within ~8%.

| Arm | Targets | Cold wall, run 1 | Cold wall, run 2 | vs straight |
|---|---|---|---|---|
| graph (`docs/README.md`) | 1 | 34,033ms | 30,405ms | **3.3× / 3.9× faster** |
| graph (`libs/core/src/index.ts`) | 4 | 88,872ms | 87,853ms | 1.25× / 1.34× faster |
| graph (`proto/user.proto`) | 7 | 83,397ms | 89,761ms | 1.33× / 1.31× faster |
| `straight` (all) | 9 | 111,319ms | 117,753ms | — |

**On a cold cache, selection is worth ~3.5× for a narrow change**, and the benefit
decays as the change touches more of the graph — roughly tracking the phase-1
counts.

Three things to notice:

- **Cost tracks what kind of target you select, not just how many.** The
  4-target row is all TypeScript and costs about the same as the 7-target row,
  which is mostly Go. Averaged end to end it is ~11s per additional target, but
  that average hides the composition and should not be extrapolated.
- **There is a large fixed floor.** Even selecting a single target costs ~30s, of
  which most is engine bootstrap, module build and source upload. The graph cannot
  optimise that away, so a one-target run is not 1/9th of a nine-target run.
- **Do not compare these absolutes against an earlier recording.** They roughly
  doubled from the figures this file carried before, and both runs agree, so it
  is systematic rather than noise — but the engine moved from `v1.0.0-beta.9` to
  `v1.0.0-beta.11` and the orchestrator's layer structure changed in between, on
  a differently loaded machine. The *ratio* is the durable finding; the
  milliseconds are only comparable within a single run.

## The two regimes are the actual answer

| | Warm engine | Cold engine (fresh runner) |
|---|---|---|
| Selection's wall-clock value | **none** | **~3.5×** |
| What still justifies the graph | safety, derivation, content-keyed reuse | all of that, plus real speed |

Neither number alone is honest. Ephemeral runners are the common case, which
favours the graph; a persistent engine or remote cache is the standard
optimisation, which erases its speed advantage. Anyone quoting "3.5× faster CI"
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
