# ADR-002: Go SDK vs Dang for the CI orchestration layer

- **Status**: Decided — **standardised on Dang**; the Go SDK orchestrator was removed
- **Date**: 2026-08-04 (superseded the same day; see *Revision* below)

## Decision

Orchestration is written in **Dang** only. `ci/orchestrator-go` was deleted once
the comparison below was complete and its one genuine functional advantage
turned out to be surmountable.

The Go implementation is recoverable from git at `4696da9:ci/orchestrator-go/`:

```bash
git show 4696da9:ci/orchestrator-go/main.go
git checkout 4696da9 -- ci/orchestrator-go   # to resurrect it
```

## Revision: why this changed from "keep both"

This ADR originally kept both orchestrators, on the grounds that per-target
timing was impossible in Dang. That was **wrong** — see the retraction under
*Where the Go SDK won*. Once timing was solved, the remaining case for keeping
the Go module was a differential test and a two-SDK stage demo, against these
recurring costs:

- 20,352 generated lines committed to the repo.
- `dagger generate` required before the module will even load, and re-required
  on every engine bump — precisely the beta churn accepted in ADR-001. A
  "frozen" module is therefore not free.
- Every new toolchain `kind` implemented twice, in two languages. This was the
  real driver: the planned `produces` + topological-DAG work for protoc would
  otherwise have been built twice.

What was given up, stated plainly: `bench/compare.sh`'s cross-implementation
check (monograph's Cypher-vs-in-memory cross-check remains and is the more
valuable of the two), and the option of demoing both SDKs live.

The escape-hatch argument carried no weight: `tools/monograph` is already a Go
binary invoked as a container step, so library access never required a Go
*Dagger module*.

## What was held constant

The comparison is only meaningful because everything except the SDK is shared:

- Same monorepo (`monorepo/`), same 7 affected targets.
- Same input contract: `affected.json` from `monograph affected`.
- Same output contract: a run report ingested by `monograph record`.
- Same selection rule: `runnable && !reusable`, applied inside each module so
  neither can cheat by filtering earlier.
- Same cache strategy: volumes keyed by toolchain, not by target.
- `--no-reuse` when benchmarking, so the second orchestrator cannot trivially
  reuse the first one's results and report a fake speedup.

## Measured

Measured on 7 targets (4 TypeScript, 3 Go) before the Go module was removed:

| | Dang | Go SDK |
|---|---|---|
| Hand-written lines | **114** | 136 |
| Generated lines that must be committed | **0** | 20,352 |
| Files in the module | **2** (`main.dang`, `dagger-module.toml`) | 7 (`main.go`, `dagger.gen.go`, `internal/`, `go.mod`, `go.sum`, `.gitignore`, `.gitattributes`) |
| Codegen step before first run | **none** | `dagger generate` required |
| Verdicts on 7 targets | identical | identical |

**Timings are not reported as a result.** Observed wall times ranged roughly
2.7–3.2s for both on a cold-ish cache, but one warm Dang run came in at 8.9s —
slower than its own cold run. At this scale the numbers are dominated by engine
scheduling and image-layer noise, not by the SDK. Drawing a performance
conclusion from them would be dishonest; a real answer needs many more targets
and repeated trials.

## Where Dang won

- **No codegen, nothing generated to commit.** The Go module needs
  `dagger generate` before it will even load, and 20k generated lines live in
  the repo. Dang has two files and no build step. For a module whose whole job
  is composing Dagger API calls, that is a large difference in friction.
- **The plan reads as the pipeline.** `p.targets.filter { … }.map { … }` is the
  orchestration, with no client boilerplate between intent and API call.
- **Type-driven `JSON.decode` is genuinely good.** Declaring `type Plan { pub
  targets: [Target!]! }` and writing `let p: Plan! = JSON.decode(plan.contents)`
  gets a validated, typed value with no unmarshalling code.
- **Its strictness caught a real bug in our contract.** Dang rejected the plan
  with `p.targets[5].testCmd: missing required field`, because Go's
  `omitempty` dropped `testCmd` for non-runnable targets like `proto`. The Go
  orchestrator would have silently seen `""`. We fixed the contract to always
  emit the key. A stricter consumer found a latent flaw in the producer.

## Where the Go SDK won

- ~~**Timing.**~~ **Retracted — this was wrong.** The original claim was that
  Dang's lack of an ambient clock made per-target `durationMs` impossible
  without losing granularity. It is not: let the container time itself and
  report the result as data. Verified working for passing commands, failing
  commands, and commands containing `;`, `&&`, and `exit`:

  ```dang
  # The user command arrives as a positional arg and runs via a nested
  # `sh -c "$1"`, so it cannot hijack the wrapper's control flow.
  let wrapped = `now(){ set -- $(tr "." " " < /proc/uptime); echo $(( $1 * 100 + 10#$2 )); }; start=$(now); sh -c "$1" > /tmp/out 2>&1; rc=$?; end=$(now); printf '{"rc":%d,"ms":%d}' "$rc" "$(( (end - start) * 10 ))" > /tmp/result.json; exit 0`

  let outcome: Outcome! = JSON.decode(
    container.from(image)
      .withExec(["sh", "-c", wrapped, "monograph-timing", cmd])
      .file("/tmp/result.json").contents
  )
  ```

  Three things had to be discovered the hard way, and each is a general Dagger
  lesson rather than a Dang one:

  1. **A failed exec's filesystem is discarded.** A results file written just
     before a non-zero exit is unreadable. So always `exit 0` and carry the real
     exit code inside the file — which also removes the need for `expect: ANY`.
  2. **BusyBox `date` silently ignores `%N`.** `date +%s%N` returns whole
     seconds, so millisecond arithmetic collapses to `0`. `/proc/uptime` gives
     centiseconds and works on every image here (alpine, golang:alpine,
     node:alpine, markdownlint-cli2).
  3. **`10#$2` forces base 10**, or a centisecond field like `08` is read as
     invalid octal.

  Arguably this is *better* than the Go SDK's `time.Since`, which measures image
  pull and `pnpm install` alongside the test. The container-internal number is
  the test command alone. Capturing both is the honest option.

  There is no String→Int cast in Dang (`::` is a type hint, not a parser);
  type-driven `JSON.decode` is the way to parse.
- **Ecosystem access.** Anything needing a library (a Neo4j driver, a parser, a
  metrics client) cannot live in Dang at all. This is why `tools/monograph` is a
  Go binary invoked as a container step rather than part of the module — and it
  is the structural reason the extractor could never have been written in Dang.
  Note this did *not* argue for keeping a Go Dagger module: a plain binary in a
  container serves the same purpose.
- **Familiar debugging.** Standard Go tooling, editor support, and a large body
  of existing knowledge. Dang has an LSP, but the ecosystem is new.

## Gotchas that cost real time

Both are recorded here because neither is obvious from the docs:

1. **`sh -lc` breaks toolchains.** A login shell re-reads `/etc/profile` and
   clobbers the image's `PATH`, so `go: not found` inside `golang:1.26-alpine`.
   Use `sh -c`. This bit both orchestrators identically.
2. **pnpm aborts without a TTY.** `ERR_PNPM_ABORTED_REMOVE_MODULES_DIR_NO_TTY`
   until `CI=true` is set — and the host's `node_modules` must be excluded from
   the uploaded source, or pnpm tries to purge a foreign modules directory.
3. **A `Void @check` needs a trailing bare `null`** in Dang, because `.sync`
   returns `Container!`. See ADR-001.

## Resolved: stale durations and the fictional `cacheHit`

**The problem.** A cached exec reported a stale duration. When Dagger replays a
`withExec` it replays the recorded results file too, so `durationMs` was the
*original* execution's number. Observed directly in `bench/run.sh`: wall time
halved between cold and warm runs while every per-target duration stayed
byte-identical. `cacheHit` was hardcoded `false`, so a replayed target claimed to
have executed in 250ms when it had not executed at all.

**The fix was to move the question, not to answer it better.** A Dang module
cannot know whether the engine replayed its exec — that is not a value to compute
more cleverly, it is a value the wrong component was producing. So:

- `cacheHit` was **removed from the orchestrator's contract entirely**, in both
  the Dang `Result` type and monograph's `TargetResult`.
- `monograph record` now **derives** it: if any earlier `TargetRun` already
  recorded this exact `targetHash`, the execution cannot have been fresh work.
- When it was cached, `durationMs` is stored as **null**. An unknown duration is
  honest; a stale one silently corrupts anything built on it.

Verified by running identical content twice under `--no-reuse`:

```text
run                cacheHit  durationMs
cachehit-demo-1    FALSE     40
cachehit-demo-2    TRUE      NULL
```

Pinned by `TestRecordDerivesCacheHit`. `bench/run.sh` now labels its warm
per-target column as replayed rather than re-measured, and directs the reader to
wall time instead.

**The underlying tension remains worth understanding**, even though the symptom
is gone: monograph's `targetHash` provides cross-run reuse via the graph's
history, while Dagger's content-addressed cache provides intra-run replay. Two
layers keyed on similar inputs for different purposes. The graph now records
which layer satisfied a given target instead of pretending neither did.

## Consequence

The split in this repo is not a compromise, it is the shape the tools imply:

- **Extraction and graph queries → Go** (`tools/monograph`), because they need
  libraries Dang cannot reach.
- **Container orchestration → either**, and Dang is the better fit for it.

For orchestration alone, Dang wins, and that is what shipped.
