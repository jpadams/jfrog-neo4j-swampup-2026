# ADR-001: Pin Dagger to v1.0.0-beta.11 and author orchestrators in Dang

- **Status**: Accepted
- **Date**: 2026-08-04, amended 2026-08-22 (beta.9 → beta.10) and 2026-09-01
  (beta.10 → beta.11; see [Amendment](#amendment-2026-09-01-beta10--beta11))

## Decision

Target Dagger `v1.0.0-beta.11`, installed repo-locally at `.bin/dagger`, and write the
comparison orchestrators in both the Go SDK and Dang.

The original decision below was taken against `v1.0.0-beta.9` and is kept as written; the
two amendments record what changed on the way to beta.10 and then beta.11, and what was
re-verified at each step.

## Why the beta rather than stable v0.21.8

Two things I initially believed about the stable line turned out to be wrong, and both are
recorded here so nobody re-litigates them:

- **Dang does not require the beta.** `MinimumDangV2ModuleVersion = "v0.21.5"` in
  `engine/version.go`, so Dang v2 semantics run on stable.
- **`dagger check` and `dagger up` are not beta-only.** `cmd/dagger/checks.go` and
  `cmd/dagger/up.go` exist in v0.21.8; `dagger check` landed in v0.21.0. They are absent from
  `dagger --help` in a directory with no module because they register dynamically.

So the beta was chosen for reasons that survive those corrections:

- **Workspace `dagger.toml` with named modules** — the natural shape for a two-orchestrator
  comparison living in one repo.
- **`dagger.lock` + `--lock frozen`** — hermetic inputs, which is what makes cache reuse
  trustworthy rather than hopeful. (Superseded in beta.10: locking is pinned by default and
  the flag is gone. The property survives; the mechanism changed. See the amendment.)
- **1.0 alignment** — a 2026 talk demoing the 1.0 line ages forward.

Accepted cost: beta churn, `/next`-only docs, and beta tags that exist as git tags without
published GitHub releases.

## Install (the version must be pinned explicitly)

```bash
curl -fsSL https://dl.dagger.io/dagger/install.sh \
  | BIN_DIR="$PWD/.bin" DAGGER_VERSION=1.0.0-beta.11 sh
```

`dl.dagger.io/dagger/versions/latest` resolves to `0.21.8`, so an unpinned install gets stable.
The system-wide `dagger` (v0.21.7) is deliberately left in place; `.bin/` is gitignored.

## Verified in Phase 0

All checks below were run, not assumed — **on beta.9**. They are left as the beta.9 record;
each amendment lists what was re-run on the version it covers.

| Capability | Result |
|---|---|
| `dagger sdk install dang` | Works; creates `dagger.toml`, registers the SDK |
| `dagger module init dang <name>` | Works; scaffolds `.dagger/modules/<name>/` |
| `dagger call <mod> <fn>` | Works (`call` exists but is hidden from top-level help) |
| `JSON.decode` into nested typed records | Works — `Plan { targets: [Target!]! }` decoded from a `File!` |
| `.filter` / `.map` fan-out over decoded targets | Works; containers run per target |
| Skipping `reusable: true` targets | Confirmed — that target did not execute |
| `@check` + `dagger check` discovery | Works; `orch:smoke` discovered and passed |
| `dagger generate` | Auto-pinned `engineVersion` from `latest` → `v1.0.0-beta.9` |
| Agent-safety on changesets | `dagger generate` refuses to apply without `-y` or `--no-apply` |

### The v1 CLI surface differs from v0.21

Top-level commands are now `setup`, `activity`, `check`, `generate`, `up`, `install`,
`installed`, `search`, `settings`, `uninstall`, `update`, `api`, `cloud`, `module`, `sdk`,
`workspace`. There is no top-level `init`, `functions`, or `develop`; module authoring moved
under `dagger module` and SDK management under `dagger sdk`.

### Two Dang gotchas worth remembering

**A `Void` check needs a trailing bare `null`.** `.sync` returns `Container!`, so a body ending
in `.sync` fails typechecking against a declared `Void`. Dagger's own toolchains use:

```dang
smoke: Void @check {
  container.from("alpine:3").withExec(["true"]).sync

  null
}
```

**`engineVersion = "latest"`** is what `module init` writes, and the SDK's generate-check fails
until `dagger generate` pins it. Pin it in the repo; do not leave it floating.

**`module init --path` does not register the module.** `dagger module init dang <name>` with no
`--path` adds a `[modules.<name>]` entry to `dagger.toml`, but with `--path` it only records the
module under the SDK's `as-sdk.modules` list. The module is not callable until you also run:

```bash
./.bin/dagger install ci/orchestrator-dang
```

After that, `dagger installed` lists both and `dagger call orchestrator-dang <fn>` resolves.

## Consequences

- `.bin/dagger` is the only Dagger used by this repo. Scripts and docs must not call bare `dagger`.
- The verification block in the plan is re-run before any talk or demo, since the engine is beta.
- Fallback if the beta becomes untenable: v0.21.8 with `dagger.json` and
  `"sdk": {"source": "dang"}`. Verified viable; no other part of the design changes.

## Amendment (2026-08-22): beta.9 → beta.10

The pin moved to `v1.0.0-beta.10`. Everything this repo does works there, but one of the
three reasons above named a flag that no longer exists, so the rationale is restated rather
than patched.

### Locking is now pinned by default, and gated on `engineVersion`

From the beta.10 history — `feat: make workspace locking pinned by default` and
`feat: gate workspace locking by API version`:

> Write lockfiles as version 2 without per-entry policy. Keep reading version 1 so existing
> float entries refresh once before migration. […] Remove `dagger lock` and `--lock` from the
> CLI. Leave `dagger update` as the explicit refresh command.

> API views before v1.0.0-beta.10 now ignore lockfiles, while supported views always use
> pinned locking.

Four consequences, all verified here:

- **`--lock frozen` is gone.** `./.bin/dagger --lock frozen call …` now fails with
  `unknown flag: --lock`. The hermetic property is not lost — it is the default.
- **The gate is the client's API version, not the module's `engineVersion`.** The commit
  message reads as though a module pinned below beta.10 would ignore the lockfile; it does
  not. Tested by poisoning the `markdownlint-cli2` entry with another image's digest and
  running the docs plan: with the beta.10 CLI the run **fails**
  (`Container.from(address: "…@sha256:0178a6…"): not found`) whether the module declares
  `engineVersion` beta.9 or beta.10, so the lock is honoured either way. Run the same
  poisoned lock through the **beta.9 CLI** and it passes green — resolving the image live,
  ignoring the entry, and leaving the file untouched. That is the silent-unhermetic mode, and
  it is a property of which `dagger` binary you run. The module `engineVersion` bumps here are
  for module API semantics and the SDK's generate check; they are not what turns pinning on.
- **The lockfile migrates v1 → v2, one way.** `git.head` becomes `git.ref` with a structured
  `{"sha": …}` result, and the per-entry `"pin"` / `"float"` policies disappear. Recorded SHAs
  survive the migration unchanged. Afterwards, beta.9 without `--lock frozen` silently ignores
  the v2 file, and beta.9 *with* `--lock frozen` hard-errors:
  `unsupported lockfile version "2"`. Rolling back to beta.9's hermetic path therefore means
  reverting `dagger.lock` too.
- **Ordinary runs write the lockfile.** The first run that meets an image not yet recorded
  resolves it live and appends it — that is how `davidanson/markdownlint-cli2` entered the
  lock. So `git diff --exit-code dagger.lock` after a run is the real hermeticity check, and
  CI should expect a dirty tree the first time a new image appears. That check presumes the
  beta.10 CLI: an older client ignores the lockfile *and* declines to write it, so a clean
  `dagger.lock` proves hermeticity only for a binary that reads it. `dagger update` refreshes
  recorded entries and applies **immediately, without a changeset and without `-y`** — unlike
  `dagger generate`, whose agent-safety behaviour is unchanged.

### Re-verified on beta.10

| Capability | Result |
|---|---|
| `dagger query` against the engine | `v1.0.0-beta.10+0e19eba6` |
| `dagger call orchestrator-dang run` (docs plan) | Passed |
| Same, on a proto plan | 7 targets passed, codegen threaded in; `proto` skipped on `runnable: false` |
| `dagger call orchestrator-dang generated … export` | Exports the tree with `proto/gen/**` produced |
| `dagger call orchestrator-dang codegen-steps` | Passed |
| `dagger check` | Passed (`dagger-dang-sdk:generate`) |
| `dagger generate --no-apply` | No changes to apply after the `engineVersion` bump |
| Secret plumbing, `--graph-password=env:VAR` | `Address.secret → Secret!` resolves; only the deliberately unreachable Neo4j fails |

Not re-run on beta.10: `orchestrator-dang ci` end to end, which needs a Neo4j reachable from
inside a container rather than a host-local one.

### Still broken in beta.10

`mis-bin-repro/` reproduces unchanged — an argument whose name differs from another only by a
letter's case is unreachable, and the flag advertised for it silently fills the other one at
exit 0. Confirmed under both a beta.9 and a beta.10 module pin, so it is not an
`engineVersion` artefact. `verify.sh` still exits 0 when the bug is present, which makes it a
regression check against beta.11.

## Amendment (2026-09-01): beta.10 → beta.11

The pin moved to `v1.0.0-beta.11`. No repository code changed — `main.dang`, `dagger.toml`
and both `dagger-module.toml` files needed only the `engineVersion` bump. The lockfile did
change, again, and in a way that costs more than the beta.9 → beta.10 migration did.

### The lockfile keeps version 2 but changes its entry vocabulary

Same `[["version","2"]]` header, different call names and value shapes:

| beta.10 | beta.11 |
|---|---|
| `container.from`, keyed `[address, platform]` → digest string | `oci-sha`, keyed `[address]` → digest string. **No platform component.** |
| `git.ref`, keyed `[url, ref]` → `{"sha": …}` object | Two entries: `git-latest` `[url]` → ref name, and `git-sha` `[url, ref]` → sha **string** |

Because `version` did not change, nothing warns you. The failure is at parse time instead.

### beta.11 hard-errors on a beta.10 lockfile, and `dagger update` cannot migrate it

The first beta.11 run against the committed beta.10 lock fails:

```text
oci-sha lockfile: parsing lock: lockfile line 6: invalid value:
  json: cannot unmarshal object into Go value of type string
```

Line 6 is the first `git.ref` entry — the object-valued one. `dagger update`, which the
beta.10 amendment named as the explicit refresh command, fails on the same line
(`parse workspace lock: …`), so it cannot perform this migration. The only route is to
delete `dagger.lock` and let a run rebuild it.

That is loud rather than silent, which is the good half. The bad half is that rebuilding
re-resolves every git ref:

- `dang-sdk` moved `c724eec` → `29090a0`. The old SHA is **not preservable by hand**: beta.10
  keyed it under `HEAD`, beta.11 keys it under `refs/heads/main` and records the ref name in a
  separate `git-latest` entry. Upgrading the CLI therefore also bumps the SDK the module is
  built with, whether or not you wanted that. The root cause is that `dagger.toml` registers
  `dagger-dang-sdk` as a bare `github.com/dagger/dang-sdk` with no ref, so the lockfile is the
  only thing pinning it; pinning the source itself was not explored.
- The stale `go-sdk` entry disappeared, correctly — nothing in `dagger.toml` references it any
  more; it was left over from the ADR-002 comparison.

Image digests survive where the tag has not moved: `markdownlint-cli2:v0.23.2`
(`sha256:839558fd…`) and `alpine:3` (`sha256:28bd5fe8…`) are byte-identical to their beta.10
entries. `golang:1.26-alpine` and `node:24-alpine` changed because the tags themselves moved
between 2026-08-22 and 2026-09-01, not because of the format.

### Dropping the platform key is an improvement, and it was checked

Both the beta.10 (`[address, linux/arm64]`) and beta.11 (`[address]`) entries record a
**multi-platform index digest** — confirmed by fetching each digest from the registry and
reading its `mediaType` (`application/vnd.oci.image.index.v1+json`, 16 manifests). So the
platform component in beta.10's key never selected a per-platform manifest; it only meant an
amd64 CI runner would append a *second* entry for the same image.

Consequence for CI: `git diff --exit-code dagger.lock` gets **stronger**, not weaker. One
lockfile now serves an arm64 laptop and an amd64 GitHub runner.

### Pinning is still enforced

Re-run of the beta.10 poisoning test, with one correction to the method. Poisoning an entry
for an image the engine has already cached proves nothing — the whole `.run` short-circuits
as `CACHED` and the lock is never consulted. Poison an entry for an image the engine has not
seen (`busybox:1.37.0`, given `golang`'s digest) and the run fails:

```text
✘ Container.from(address: "docker.io/library/busybox:1.37.0@sha256:28d89ee9…"): Container!
! failed to resolve image … (platform: "linux/arm64"): … not found
```

beta.11 appends the locked digest to the address, exactly as beta.10 did.

### Rolling back is a one-way door again — and this time it is silent

Running the **beta.10** binary against a beta.11 lockfile does *not* error the way beta.9 did
against a v2 file. It passes green: it does not recognise `oci-sha`, resolves the image live
(`1 pull, 1 unpack`), and **appends its own `container.from` line**. That is the
silent-unhermetic mode, reached without any version bump to warn you.

The hybrid file that leaves behind was checked in both directions, because the obvious guess
is wrong. beta.11 does **not** choke on it: every value in a `container.from` entry is a
string, so it parses, and beta.11 simply uses its own `oci-sha` entries and carries the dead
line along untouched — the run passes and the stale entry is neither honoured nor pruned. So
the hazard is one-sided. A beta.10 client silently loses pinning; a beta.11 client is still
hermetic, just accumulating cruft that only a hand edit removes.

So the standing rule carries forward unchanged in force but changed in mechanism: **rolling
back to beta.10 means reverting `dagger.lock` too.**

### The `Workspace` breaking change does not apply here

beta.11 makes `Workspace.withNewDirectory` replace on every workspace kind, with a new
`Workspace.withDirectory` that merges. Every `withDirectory` call in `ci/orchestrator-dang`
is `Container.withDirectory`; the orchestrator never constructs a `Workspace`. No change
needed.

### Re-verified on beta.11

| Capability | Result |
|---|---|
| `dagger version` / engine | `v1.0.0-beta.11+a4e1e4ff` |
| `dagger call orchestrator-dang run` (docs plan) | Passed |
| Same, on a proto plan | 7 targets passed, codegen threaded in; `proto` skipped on `runnable: false` |
| `dagger call orchestrator-dang generated … export` | Exports the tree with `proto/gen/**` produced |
| `dagger call orchestrator-dang codegen-steps` | Passed |
| `dagger check` | Passed (`dagger-dang-sdk:generate`) |
| `dagger generate --no-apply` | No changes to apply after the `engineVersion` bump |
| Secret plumbing, `--graph-password=env:VAR` | `Address.secret → Secret!` resolves |
| **`orchestrator-dang ci` end to end** | **Passed** — 7 targets, recorded to the graph. Closes the row beta.10 left open |
| Evidence predicate read back from that run | `monograph evidence --run-id …` emits `ci-coverage/v1` |
| Hermeticity: repeat run, then diff the lock | Clean — nothing resolved live |
| `bench/demo.sh` four beats | 9 → 1 → 4 → 0 targets, Evidence predicate emitted; re-run unmodified once committed |
| `bench/run.sh proto/user.proto` | 7 run, 1 skipped (`proto`, no test command); cold and warm both green |
| `bench/rebase-scenario.sh` | Both scenarios passed — unrelated rebase reuses everything, shared-config rebase reruns 4 |
| `tools/monograph` unit + Neo4j tests | `ok` |

The beta.10 gap is closed because the graph now lives on Aura, which a container can reach;
it was only ever blocked by a host-local Neo4j. One flake worth knowing about: Aura dropped a
connection mid-scenario once (`Unable to retrieve routing table … EOF`) and the rerun was
clean. That is the database, not Dagger.

### The bench scripts read the lockfile from `git`, not from your working tree

`bench/demo.sh` and `bench/rebase-scenario.sh` both `git clone "$ROOT"` and `cd` into the
clone, so they see **committed** files only. While the beta.11 upgrade sits uncommitted, both
scripts clone a tree whose `dagger.lock` is still beta.10 and die on beat 1 with the
`oci-sha lockfile: parsing lock:` error above — the upgrade looks broken when it is merely
unstaged. While the upgrade was still uncommitted both were verified through a copy that
seeds the clone with the new `dagger.lock` and `dagger-module.toml`; `bench/run.sh` needed no
such treatment because it runs from `$ROOT`. Once the upgrade was committed, `bench/demo.sh`
was re-run **unmodified** and passed all four beats — that is the result the table records.

Nothing to fix, but it is a real ordering constraint: **commit the lockfile before demoing.**

### Still broken in beta.11

`mis-bin-repro/` reproduces unchanged, so the beta.10 amendment's forward-looking note is now
answered: beta.11 did **not** fix it. An argument whose name differs from another only by a
letter's case is still unreachable, and the flag advertised for it still fills the other one
at exit 0. Confirmed under a beta.11 module pin. `verify.sh` remains a regression check,
now against beta.12.
