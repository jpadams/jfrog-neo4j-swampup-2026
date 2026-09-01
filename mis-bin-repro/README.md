# Dagger: a CLI flag silently populates the wrong argument

`dagger v1.0.0-beta.11`, and unchanged since `v1.0.0-beta.9`. Dang SDK (the Go
SDK behaves identically). Confirmed under beta.9, beta.10 and beta.11 module
`engineVersion`s, so it is not a module-API-version artefact.

A module declares two arguments differing only by one letter's case:

```dang
pub probe(neo4juri: String! = "", neo4jUri: String! = ""): String! { ... }
```

## The mapping

This is the whole bug in one table. Two distinct arguments, two distinct
advertised flags — but both flags populate the same argument.

| Declared in `main.dang` | API argument name | Flag `--help` advertises | Flag *should* set | Flag *actually* sets |
|---|---|---|---|---|
| `neo4juri` — all lowercase | `neo4Juri` | `--neo-4-juri` | `neo4juri` | `neo4juri` — correct |
| `neo4jUri` — capital U | `neo4JUri` | `--neo-4-j-uri` | `neo4jUri` | **`neo4juri` — wrong argument** |

So `neo4jUri` is unreachable: no flag sets it, and the flag advertised for it
feeds `neo4juri` instead, with no error and exit code 0.

### Where the names come from

The API uppercases a lowercase letter that follows a digit, then kebab-casing
splits on digit boundaries *and* case transitions:

```
declared  neo4juri  ->  API neo4Juri  ->  neo | 4 | juri     ->  --neo-4-juri
declared  neo4jUri  ->  API neo4JUri  ->  neo | 4 | j | uri  ->  --neo-4-j-uri
```

The forward direction is fine — the two flags really are distinct. It is the
reverse, flag back to argument, that fails to tell them apart.

Confirm the API names yourself:

```bash
echo '{ __type(name: "Mangle") { fields { name args { name } } } }' \
  | dagger -m ./mangle query
# -> ["neo4Juri", "neo4JUri"]
```

## Observed behaviour

```
$ dagger call probe --neo-4-juri=MEANT_FOR_neo4juri
neo4juri="MEANT_FOR_neo4juri"  neo4jUri=""              # correct

$ dagger call probe --neo-4-j-uri=MEANT_FOR_neo4jUri
neo4juri="MEANT_FOR_neo4jUri"  neo4jUri=""              # WRONG, and exit 0
```

The sentinel values name the argument they were intended for, so no
interpretation is required: `MEANT_FOR_neo4jUri` ends up in `neo4juri`.

With `neo4juri` removed, so that `neo4jUri` is the only argument, the silent
mis-binding becomes a hard error — suggesting the flag never resolves to
`neo4JUri` at all, and only appears to work when another argument can absorb it:

```
$ dagger call probe --neo-4-j-uri=x     # module declaring only neo4jUri
Error: set call inputs: find arg "neo4JUri"
```

**Why it matters:** were the intended argument a `Secret`, a credential would be
routed to a different parameter and the build would still be green.

## Reproduce

```bash
./verify.sh                        # dagger on PATH
DAGGER=/path/to/dagger ./verify.sh
```

Exit 0 = reproduced. Exit 1 = not reproduced, so this doubles as a regression
check against a later release.

## Files

| Path | What |
|---|---|
| `mangle/main.dang` | The whole reproducer: `probe` (both arguments, silent mis-binding) plus `probeOnlyLowercase` / `probeOnlyCapital`, which isolate the working and hard-erroring single-argument cases |
| `verify.sh` | Runs it and explains each step |
| `dagger.toml` | Standalone workspace, so a parent repo is never modified |
