# ADR-001: Pin Dagger to v1.0.0-beta.10 and author orchestrators in Dang

- **Status**: Accepted
- **Date**: 2026-08-04, amended 2026-08-22 (beta.9 → beta.10; see [Amendment](#amendment-2026-08-22-beta9--beta10))

## Decision

Target Dagger `v1.0.0-beta.10`, installed repo-locally at `.bin/dagger`, and write the
comparison orchestrators in both the Go SDK and Dang.

The original decision below was taken against `v1.0.0-beta.9` and is kept as written; the
amendment records what changed on the way to beta.10 and what was re-verified.

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
  | BIN_DIR="$PWD/.bin" DAGGER_VERSION=1.0.0-beta.10 sh
```

`dl.dagger.io/dagger/versions/latest` resolves to `0.21.8`, so an unpinned install gets stable.
The system-wide `dagger` (v0.21.7) is deliberately left in place; `.bin/` is gitignored.

## Verified in Phase 0

All checks below were run, not assumed — **on beta.9**. They are left as the beta.9 record;
the amendment lists what was re-run on beta.10.

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
