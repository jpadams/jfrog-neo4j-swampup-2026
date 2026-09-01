# ADR-003: How this repo integrates with JFrog — signed Evidence, and nothing else

- **Status**: Partly implemented — the cost argument (Acts 1–3) is **built and
  measured**. Act 4 is now split: the predicate is **built** (`monograph
  evidence`, read back from the graph, in both demo drivers); **signing and
  upload are not** — nothing calls `jf`.
- **Date**: 2026-08-10, Act 4 status revised 2026-08-15

## Decision

The JFrog integration is **one signed attestation per CI run**, created with
`jf evd create` and bound to a subject in Artifactory. Nothing else is adopted.

Of that, the predicate is built and the signing is not: `monograph evidence`
produces the document from the graph, and no code in this repo invokes `jf`. The
decision is unchanged; the implementation stops one step short of signed, on
purpose (see *The predicate*).

Specifically **not** adopted, each for a stated reason further down: Artifactory
as a dependency-data source, Artifactory as a Dagger remote cache, Xray-seeded
selection, `:Artifact` nodes, and AppTrust Rego gates.

And the direction matters more than the scope. The first design pointed the arrow
**JFrog → graph**: pull build-info in to make selection more precise. That is
backwards for 2026. JFrog has exited CI orchestration (below), so the valuable
direction is **graph → JFrog**: the graph computes a fact about a change that
JFrog's control plane structurally cannot compute, and hands it over signed.

## Why now: the talk this repo is arguing with

*"You're Overpaying for CI"* — Kyle Penfound, Dagger, KubeCon NA, November 2024
([video](https://www.youtube.com/watch?v=tsH3eTo2GLI)).

The anchor example, at 5:00: a Next.js PR removing **redundant double spaces**
from a CSS file. Diff `+3/−3`. Result: **85 checks, almost 7 hours of CI
compute.** His framing — *"sometimes you need the most extreme thing to show the
problem so you realize oh yeah, on a smaller scale this is a problem too."*

His cost frame, from Dagger's own repo in October 2024: **36,000 Actions runs in
one month** (~1,200/day; Next.js was ~150,000), a 13-minute pipeline costing
**~$4,000/month** on GitHub-hosted runners, versus **2:42 uncached** on his 2021
M1 MacBook and 11.8s once cached. His prescription is three phases: run the
runner on developer machines; make the pipeline portable with Dagger so local and
CI are the same; then drop pre-merge CI entirely and let the reviewer run
`dagger -m <PR ref> call test`.

Two moments in that talk are the whole reason this ADR exists.

### The question from the floor is this repo's thesis

At 28:43 an audience member asks, unprompted:

> *"We struggle with monorepos where we have repositories that produce a lot of
> different artifacts depending on what part of the repo is being changed — for
> instance a lot of needless container images are produced by documentation
> changes. Is it possible to use dagger to filter out what is being changed?"*

The answer offered was two options: **(a)** Dagger's content-addressed cache
handles it, or **(b)** *"we have logic in some of our own pipelines in the dagger
repo where we look at the actual diff."*

This repo has now measured both, and neither is wrong — they are incomplete:

| Answer | Verdict from `bench/RESULTS.md` |
|---|---|
| (a) the cache handles it | **True warm, false cold.** 9 targets cost the same ~1.7s as 1 on a warm engine. A fresh runner pays 51,495ms vs 13,986ms — and hosted CI is always a fresh runner. |
| (b) hand-rolled diff logic | **That is the `straight` arm.** Nine hand-written `allTargets` entries in `main.dang`, modelled on Dagger's own `toolchains/test-split`. It works; the cost is that adding a target requires someone to remember, or coverage silently stops. |

There was a third answer to that question. This repo is it, and the honest
contribution is not "Dagger is wrong" — it is the qualification (a) needs and the
maintenance cost (b) carries.

### The talk ends on an unsolved trust problem, which is where JFrog fits

Phase 3 ("no CI") is blocked, by the speaker's own admission at 23:44:

> *"It's a new trust model... we need some way to make sure that I actually did
> that"* — best available answer being DHH's bash script posting a check via the
> GitHub API. *"Something to solve for."*

And at 30:41, Jerome from Guidewire asks whether a secure software supply chain
can be trusted on a developer machine rather than a controlled environment. The
answer: *"right now I would probably say keep your production artifact building in
some controlled system and not developer machines."*

So the talk's conclusion is: **local-first CI is cheaper and faster, but we do not
know how to trust it.** That is the hole. Signed Evidence fills it, and filling it
is one command.

A historical note, since it is too apt to omit: this talk argued for getting rid
of your CI orchestrator in November 2024. JFrog Pipelines hit feature freeze on
**1 November 2024**. Both sides of the industry concluded the orchestrator is not
the valuable layer, in the same month.

## The three tiers of overpaying

The story this repo tells, with only the third tier being ours:

| # | What you overpay for | Why | Fixed by | Measured here |
|---|---|---|---|---|
| 1 | Re-running **byte-identical** work | no content cache | Dagger's cache — the talk's thesis | warm engine: 9 targets ≈ 1 target ≈ ~1.7s |
| 2 | Running work the change **could not affect** | CI has no dependency knowledge | the graph | cold runner **3.7×** for a narrow change; warm engine **nothing** |
| 3 | Re-running work whose **inputs never changed**, because the SHA moved | the cache key is wrong | Merkle `targetHash` + recorded history | rebase onto unrelated `main`: SHA-keyed CI runs 4 targets, this runs **zero** |

Tier 1 belongs to Dagger and must be credited as such. Tier 2 is real but our own
benchmark limits the claim — worth 3.7× on a fresh runner and *nothing* warm.

**Tier 3 is the one to lead with**, because it is the only tier where a warm cache
does not save you: when the SHA moves, every cache key moves with it, so the work
is re-executed rather than replayed. It also escalates the talk's own example
rather than repeating it — the Next.js PR wastes 7 hours because CI ran *too
much*; tier 3 wastes it because CI correctly re-ran *the right things for no
reason*. `bench/rebase-scenario.sh` proves it in both directions, including the
control where a real `tsconfig.base.json` change on `main` correctly re-runs 4
targets.

> The difference is not the rebase. It is what the cache key is made of.

## Why the graph is the piece JFrog cannot supply

An AppTrust gate can verify that test evidence **exists**. It cannot verify that
the tests which ran were **sufficient for what changed**, because at any real
scale CI runs a selected subset. The evidence is true and incomplete, and the gate
cannot tell the difference.

This repo has already measured what that produces: `jenkins-paths` fires **zero
stages** for a `proto/user.proto` change while seven targets are affected. Put
that under a governance gate and the chain is — green run → passing test evidence
→ gate satisfied → promoted on a **false pass**.

JFrog cannot close this itself, and the reason is structural rather than a product
gap: answering it requires a dependency graph of **first-party source targets**.
Artifactory knows binaries and their external dependencies. Nothing in a package
records that `apps/admin` reaches `libs/core` only via `libs/ui` — that fact lives
in TypeScript project references in the source tree and dies at the repo boundary.

## The predicate

**Built.** `monograph evidence --run-id <id>` emits it; `tools/monograph/evidence.go`
and `EvidenceQuery` in `queries.go` are the implementation.

A custom in-toto predicate, **read back out of the graph** rather than assembled
from the plan and report files:

```json
{ "predicateType": "https://jfrog.com/evidence/monograph/ci-coverage/v1",
  "runId": "demo-reuse-1234", "repo": "monorepo", "sha": "36fb8b5",
  "trigger": "selection-only", "createdAt": "2026-08-15T02:36:36Z",
  "resolutions": [{"path":"libs/core/src/index.ts","how":"file","target":"libs/core"}],
  "affected": [{"target":"libs/ui","reason":"dependent","executed":false,
                "runnable":true,"targetHash":"5069da…"}],
  "executed": [],
  "skipped":  [{"target":"libs/ui","targetHash":"5069da…","reason":"dependent",
                "provenBy":{"ciRun":"demo-core-1234",
                            "targetRun":"demo-core-1234:libs/ui","verdict":"PASSED"}}],
  "coverageGaps": [],
  "unresolvedPaths": [] }
```

That the source is the graph and not the files is the load-bearing detail, and it
is what `queries/coverage.cypher` and the README already required: a predicate
must be *a serialisation of what was recorded*, "not a separately assembled
claim — the two must not be able to disagree". A document built from the plan and
report the tool itself just wrote is only ever as good as the tool, which is the
question Evidence exists to answer.

`coverageGaps` is that query's own result, carried inside the predicate. It is
the field a gate should key on: empty means `affected ⊆ executed ∪
proven-reusable` held for this run. A skip with no citation appears in `skipped`
with `provenBy: null` **and** in `coverageGaps` — an attestation that dropped its
own violations would be worse than none.

The example above is the real shape of the demo's last run: nothing executed,
every target skipped with a citation. Coverage proven with no work done.

```bash
dagger call orchestrator-dang ci --changed=...     # on the laptop: cheap, fast
monograph evidence --run-id <run> > evidence.json  # built
monograph evidence --run-id <run> --command        # prints the line below; does not run it

jf evd create --predicate evidence.json \
  --predicate-type https://jfrog.com/evidence/monograph/ci-coverage/v1 \
  --subject-repo-path <path> --subject-sha256 <digest> --key <key> --key-alias <alias>
```

**The upload is not built and is not attempted.** Whether Evidence signing is
available on a given tier is listed below under *not verified, and therefore not
designed on*; a branch that shelled out to `jf` when it happened to be on PATH
would be designing on it. The command is rendered by the tool rather than written
into the demo, for the same reason `monograph queries` exists — it goes on a
screen in front of an audience, and a copy kept in the demo is a copy that can
stop matching what CI would run.

The trust question moves from **"was this machine blessed?"** to **"is this
signature valid, and does the evidence cover what changed?"** — checkable by
someone who does not trust the laptop at all. That answers both audience
questions with one mechanism.

`targetHash` goes **inside** the predicate, not as the subject: `jf evd create`
identifies its subject by Artifactory repo path plus sha256, so a synthetic
content hash is not addressable as a subject.

### Relationship to the existing JFrog–Dagger integration

[jfrog.com/integrations/dagger](https://jfrog.com/integrations/dagger/) already
exists, and it is worth being precise about the overlap, because there is none.
That integration puts a **signed link to the Dagger Cloud trace** into Evidence:
it attests *how* an artifact was built, with the execution telemetry to back it.

This predicate attests *what was selected, and why the selection was sufficient
for what changed*. A trace cannot contain that, however complete it is: a trace
records what ran, and the claim under test is about what did **not** run. The two
are different predicates about the same subject, and a promotion gate wants both
— "this was built by a pipeline you can inspect" and "the pipeline covered the
change".

**Not built:** carrying the run's Dagger Cloud trace URL inside this predicate,
which would tie the two together explicitly. It needs `CIRun.traceUrl`, and
therefore a trace field through the orchestrator's report contract, which no
component emits today. `bench/demotui` already parses the URL out of dagger's
output for its "w" key, so the gap is the report rather than the capture.

## Verified about the JFrog portfolio, so nobody re-litigates it

Everything below was checked against JFrog documentation on 2026-08-10, because
training data presented at least one dead product as current.

| Claim | Status |
|---|---|
| **Pipelines** feature freeze 2024-11-01, **EOL 2026-05-01** | Verified. Docs say *"migrate your CI/CD workflows to an alternative solution"* and **name no replacement** — *"choose the replacement that best fits your existing toolchain."* JFrog is deliberately CI-agnostic, so Dagger is a first-class choice here, not one to defend. |
| **RLM** feature development ended **2026-07-31**, EOL 2028-01-31 | Verified. **Do not design against RLM.** AppTrust is the live governance product. |
| Evidence accepts **custom predicate types** | Verified. `jf evd create --predicate <json> --predicate-type <uri>`, in-toto + DSSE. |
| AppTrust gates are **OPA/Rego**, bring-your-own-policy | Verified. |
| Rego can read **nested predicate fields**, not just assert a type exists | Verified against JFrog's own NIST SSDF example, which reads `predicate.predicate.buildDefinition.externalParameters.workflow.ref`. |
| build-info carries per-dependency `sha256`, `scopes`, and **`requestedBy`** (the ancestor chain that pulled a dependency in) | Verified in `buildinfo-schema.json`. |
| AQL has `build` / `module` / `dependency` domains with cross-domain queries | Verified. JFrog already has a queryable dependency graph; we are not inventing traversal. |

**Not verified, and therefore not designed on:**

- Whether Dagger `v1.0.0-beta.11` can export its cache to an OCI registry. This
  gates Artifactory-as-remote-cache entirely.
- Whether Evidence signing and AppTrust are available on a free or trial tier.
- Whether build-info module properties survive an AQL round-trip.

## Deliberately not built

- **Artifactory as a dependency-data source — NOT BUILT.** This was the original
  recommendation and it is demoted, not rejected. It attacks a real and *measured*
  over-approximation: today **every lockfile touch selects 9 targets**, because
  `pnpm-lock.yaml`, `package.json`, `go.mod` and `go.sum` all resolve to the root
  `workspace` target. A **Go** dependency bump therefore re-runs all four
  TypeScript targets, and a **TypeScript** bump re-runs all three Go targets —
  the same cross-language false fan-out documented for `proto/ts-proto.version`,
  triggered by the single most frequent commit type in a real monorepo. build-info
  would fix it *and* preserves "derived, never declared", since resolution-time
  facts are more derived than a parsed manifest. Two reasons to defer: it is
  achievable without JFrog by parsing lockfiles, and `monorepo/` has only three
  external dependencies, so there is nothing yet to measure.
- **Xray-seeded selection — NOT BUILT.** A CVE as just another changed input,
  reusing the existing walk with no new selection code. Blocked on the above.
- **`:Artifact` nodes — NOT BUILT.** Still the cheapest way to close the gap the
  README admits under *What the graph does not model*.
- **AppTrust Rego gate — NOT BUILT**, but no longer for the reason first given
  here. That reason was that the rule which matters,
  `affected ⊆ (executed ∪ skipped)`, is not expressible against anything in the
  portfolio. Since the predicate now carries `coverageGaps` — that relation,
  already evaluated, as a field — the rule is
  `count(input.predicate.coverageGaps) == 0`, and Rego reading nested predicate
  fields is verified above. What remains is an entitlement and the fact that
  writing the gate turns a cost demo into a governance demo. The blocker moved
  from "cannot be expressed" to "not our demo", which is a much weaker one and
  should be recorded as such.
- **Artifactory as Dagger remote cache — NOT BUILT**, and unproven (above). It is
  the largest prize, because it attacks tier 2's cold-runner weakness directly,
  and it is the same experiment as the untested remote regime in `RESULTS.md`.

## Honesty constraints on the pitch

Three ways this story could be told dishonestly, recorded so it is not:

1. **Do not claim 85 checks become 1.** Those 85 include matrix variants, deploy
   previews and bots, not 85 target suites. Quote the *category* of waste, then
   this repo's measured spread: 88% of target runs avoided for a docs typo, 22%
   for a root `tsconfig` change. Quoting only the docs row is the cherry-pick
   `RESULTS.md` already warns against.
2. **Date the dollar figures.** $4,000/month is 2024 GitHub/CircleCI pricing.
   Re-derive or attribute it.
3. **Do not claim foresight.** This reframing — the graph's value being safety,
   provenance and content-keyed reuse rather than wall clock — was reached *after*
   the benchmark showed selection buys nothing on a warm engine. The strategy
   followed the measurement. Presenting it as a prediction would be the one
   overclaim that undercuts the rest.

## Consequences

- The demo is **local-first**: `dagger call ci` on a laptop, then one signed
  attestation. That is the talk's phase 2/3 with the trust gap closed, not a new
  CI product. On screen the demo stops one step short of signed — it produces the
  document and shows the command.
- Neo4j and Evidence keep distinct jobs and neither absorbs the other: the graph
  **computes** the decision (cross-run, analytical, mutable — `flaky`, `slowest`,
  reuse); Evidence **records** it (per-version, signed, immutable, third-party
  verifiable). Attestations do not go in Neo4j, and `monograph evidence` writes
  nothing: it is a read.
- Both demo drivers show that split as a fork above RECORD. It appears at the
  step that produces a predicate and not before — the same rule the reuse loop
  follows — and the JFrog arm is drawn dashed, because a solid arrow would claim
  an upload this repo does not perform.
- Acts 1–3 required **no new code**; Act 4 required one read path and a
  subcommand. The narrative is still a reframing of `bench/rebase-scenario.sh`
  and `bench/RESULTS.md`.
