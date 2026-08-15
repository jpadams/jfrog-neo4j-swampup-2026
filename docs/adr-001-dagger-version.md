# ADR-001: Pin Dagger to v1.0.0-beta.9 and author orchestrators in Dang

- **Status**: Accepted
- **Date**: 2026-08-04

## Decision

Target Dagger `v1.0.0-beta.9`, installed repo-locally at `.bin/dagger`, and write the
comparison orchestrators in both the Go SDK and Dang.

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
  trustworthy rather than hopeful.
- **1.0 alignment** — a 2026 talk demoing the 1.0 line ages forward.

Accepted cost: beta churn, `/next`-only docs, and beta tags that exist as git tags without
published GitHub releases.

## Install (the version must be pinned explicitly)

```bash
curl -fsSL https://dl.dagger.io/dagger/install.sh \
  | BIN_DIR="$PWD/.bin" DAGGER_VERSION=1.0.0-beta.9 sh
```

`dl.dagger.io/dagger/versions/latest` resolves to `0.21.8`, so an unpinned install gets stable.
The system-wide `dagger` (v0.21.7) is deliberately left in place; `.bin/` is gitignored.

## Verified in Phase 0

All checks below were run, not assumed.

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
