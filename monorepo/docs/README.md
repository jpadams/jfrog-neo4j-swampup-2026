# Demo monorepo

This is the subject under test for the monorepo context graph — not the tooling
itself. The tooling lives one level up in `tools/monograph` and `ci/`.

## Shape

```text
proto ──> libs/core ──> libs/ui ──┬──> apps/web
  │                               └──> apps/admin
  └────> libs/authz ──┬──> services/api
                      └──> services/billing
infra   (no edges)
docs    (no edges)
```

`apps/web` depends on `libs/ui` **and** `libs/core`; `apps/admin` depends on
`libs/ui` only and reaches `libs/core` transitively. That asymmetry is
deliberate — it proves the extractor walks transitive edges.

## Why this file matters

Editing this file must run **exactly one** job: the markdown lint for the `docs`
target. Not the seven test suites a full re-run would trigger, and not nothing
at all — the relevant check, and only the relevant check.

That is the first golden test. If a docs change fans out to any other target,
the graph is wrong; if it runs nothing, the docs are unchecked.
