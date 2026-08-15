package main

// The Cypher this tool executes, as named constants rather than literals inline
// at their call sites.
//
// They are hoisted for one reason: something other than the driver needs to be
// able to SHOW them. bench/demotui puts the query on screen next to the stage
// that runs it, and a demo that displays a hand-copied approximation of its own
// query is worse than a demo that displays nothing -- it invites the audience to
// check a claim against text that no longer matches the code. `monograph
// queries` prints these same constants, so the thing displayed and the thing
// executed cannot drift.
//
// Only the ones the demo shows live here. The other queries in neo4j.go stay
// inline at their call sites, where the surrounding comments explain them; there
// is no consumer that needs them as data.

// AffectedViaCypherQuery answers "what does this change affect?" as a graph
// query. Reverse reachability: the variable-length pattern is written
// dependent -> dependency with the changed target on the RIGHT, so `affected`
// collects everything upstream of the change.
//
// This one is the project's thesis, and it is also the one that does NOT run in
// a default selection -- `affected` resolves the walk in memory unless asked for
// Cypher. TestCypherMatchesInMemory is what makes the in-memory path
// trustworthy: it asserts the two agree.
const AffectedViaCypherQuery = `
UNWIND $changed AS changedPath
MATCH (f:File {repo: $repo, path: changedPath})-[:PART_OF]->(changed:Target)
MATCH (affected:Target)-[:DEPENDS_ON*0..]->(changed)
RETURN DISTINCT affected.name AS name
ORDER BY name`

// MarkReusableQuery asks history whether this exact content has already passed,
// which is what makes a rebase free. Unlike the reachability query above, this
// runs on EVERY selection: `affected` opens a connection for it unless
// --no-reuse is passed.
const MarkReusableQuery = `
UNWIND $hashes AS h
OPTIONAL MATCH (tr:TargetRun {targetHash: h, status: 'PASSED'})
WITH h, count(tr) AS passes
RETURN h AS hash, passes > 0 AS reusable`

// RecordTargetRunsQuery writes one TargetRun per executed target, and derives
// cacheHit rather than trusting the orchestrator's report.
//
// If some earlier TargetRun already recorded this exact targetHash then this
// execution cannot have been fresh work, so the reported duration is the
// EARLIER execution's number replayed out of a cached results file and is
// stored as null instead. An unknown duration is honest; a stale one silently
// corrupts any slow-test or flakiness analysis built on top of it. See
// docs/adr-002-go-vs-dang.md.
const RecordTargetRunsQuery = `
MATCH (run:CIRun {id: $id})
UNWIND $results AS r
OPTIONAL MATCH (prior:TargetRun {targetHash: r.targetHash})
  WHERE prior.id <> r.id
WITH run, r, count(prior) > 0 AS wasCached
MERGE (tr:TargetRun {id: r.id})
SET tr.status     = r.status,
    tr.cacheHit   = wasCached,
    tr.durationMs = CASE WHEN wasCached THEN null ELSE r.durationMs END,
    tr.targetHash = r.targetHash,
    tr.toolchain  = r.toolchain,
    tr.provenance = $prov
MERGE (run)-[:RAN]->(tr)
WITH tr, r
MATCH (t:Target {name: r.target})
MERGE (tr)-[:OF]->(t)`

// RecordProvenByQuery cites the earlier PASSED run that justifies each skip --
// evidence for work that did NOT happen. It executes only when the plan has
// reused targets, so a run that skipped nothing never writes a PROVEN_BY edge.
//
// collect(prior)[0] rather than a subquery with LIMIT: LIMIT in an UNWIND
// pipeline applies to the whole stream, not per row, so it would attach a
// single proof to only one reused target. Aggregating groups by run and r.
const RecordProvenByQuery = `
MATCH (run:CIRun {id: $id})
UNWIND $reused AS r
MATCH (prior:TargetRun {targetHash: r.targetHash, status: 'PASSED'})
WITH run, r, collect(prior)[0] AS proof
WHERE proof IS NOT NULL
MERGE (run)-[pb:PROVEN_BY {targetHash: r.targetHash}]->(proof)
SET pb.target = r.name, pb.provenance = $prov`

// EvidenceQuery reads a recorded run back out as the body of a JFrog Evidence
// predicate. It writes nothing: an attestation is not graph state.
//
// The point of sourcing this from the graph rather than from the plan and report
// files the run just wrote is stated in queries/coverage.cypher and in the
// README: a predicate has to be a serialisation of what the graph holds, "not a
// separately assembled claim -- the two must not be able to disagree." A
// predicate built from the same files the tool emitted would only ever be as
// good as the tool, which is exactly the trust question Evidence exists to
// answer.
//
// Four collections, each in its own variable-scoped subquery. They cannot be
// four OPTIONAL MATCHes in one pattern: that is a cartesian product, and the
// predicate would report a target once per changed path.
//
// The `skipped` arm is queries/coverage.cypher's own pattern with the
// `proofs = 0` filter removed. Coverage returns only the violations; the
// predicate must carry every skip, so an UNPROVEN one appears with a null
// provenByRun rather than being dropped. Same pattern, and the same exclusion of
// targets whose testCmd is empty -- so the predicate and the coverage query
// cannot disagree about what counts as a gap.
const EvidenceQuery = `
MATCH (run:CIRun {id: $id})
OPTIONAL MATCH (run)-[:FOR_COMMIT]->(commit:Commit)
CALL (run) {
  MATCH (run)-[sel:SELECTED]->(t:Target)
  RETURN collect({
    target:     t.name,
    reason:     sel.reason,
    executed:   sel.executed,
    runnable:   t.testCmd <> '',
    targetHash: sel.targetHash
  }) AS affected
}
CALL (run) {
  MATCH (run)-[cp:CHANGED_PATH]->(t:Target)
  RETURN collect({path: cp.path, how: cp.how, target: t.name}) AS resolutions
}
CALL (run) {
  MATCH (run)-[:RAN]->(tr:TargetRun)-[:OF]->(t:Target)
  RETURN collect({
    target:     t.name,
    targetHash: tr.targetHash,
    verdict:    tr.status,
    durationMs: tr.durationMs,
    cacheHit:   tr.cacheHit,
    toolchain:  tr.toolchain
  }) AS executed
}
CALL (run) {
  MATCH (run)-[sel:SELECTED]->(t:Target)
  WHERE sel.executed = false AND t.testCmd <> ''
  OPTIONAL MATCH (run)-[pb:PROVEN_BY]->(proof:TargetRun)
    WHERE pb.target = t.name
  OPTIONAL MATCH (provenRun:CIRun)-[:RAN]->(proof)
  RETURN collect({
    target:            t.name,
    targetHash:        sel.targetHash,
    reason:            sel.reason,
    provenByRun:       provenRun.id,
    provenByTargetRun: proof.id,
    provenByVerdict:   proof.status
  }) AS skipped
}
RETURN run.repo            AS repo,
       run.trigger         AS trigger,
       run.createdAt       AS createdAt,
       run.unresolvedPaths AS unresolvedPaths,
       commit.sha          AS sha,
       affected, resolutions, executed, skipped`

// CypherQuery is one query the tool runs, described well enough that a viewer
// can tell what it does and -- the part that is easy to get wrong on a slide --
// WHEN it actually runs.
type CypherQuery struct {
	Name   string `json:"name"`
	Stage  string `json:"stage"` // pipeline stage: select | record | evidence
	Kind   string `json:"kind"`  // read | write
	Title  string `json:"title"`
	When   string `json:"when"` // the honesty field: when this really executes
	Cypher string `json:"cypher"`
}

// Queries returns the queries `monograph queries` prints and bench/demotui
// displays, in the order a run reaches them.
//
// The When strings are load-bearing. Presenting the reachability query and the
// reuse lookup as an equal pair would misrepresent a default selection, which
// resolves the walk in memory and asks the graph only about history.
func Queries() []CypherQuery {
	return []CypherQuery{
		{
			Name:   "reuse-lookup",
			Stage:  "select",
			Kind:   "read",
			Title:  "Has this exact content already passed?",
			When:   "every selection (unless --no-reuse)",
			Cypher: MarkReusableQuery,
		},
		{
			Name:   "affected-reachability",
			Stage:  "select",
			Kind:   "read",
			Title:  "What does this change affect? (reverse reachability)",
			When:   "only with --via-cypher or --cross-check; the default walk is in memory",
			Cypher: AffectedViaCypherQuery,
		},
		{
			Name:   "record-target-runs",
			Stage:  "record",
			Kind:   "write",
			Title:  "One TargetRun per executed target, with cacheHit DERIVED",
			When:   "every record",
			Cypher: RecordTargetRunsQuery,
		},
		{
			Name:   "record-proven-by",
			Stage:  "record",
			Kind:   "write",
			Title:  "Cite the run that justifies each skip",
			When:   "only when the plan reused something (beat 4's record, not beat 3's)",
			Cypher: RecordProvenByQuery,
		},
		{
			Name:   "evidence-predicate",
			Stage:  "evidence",
			Kind:   "read",
			Title:  "Read the recorded run back as an Evidence predicate",
			When:   "`monograph evidence`, after the run is recorded -- never during a run",
			Cypher: EvidenceQuery,
		},
	}
}
