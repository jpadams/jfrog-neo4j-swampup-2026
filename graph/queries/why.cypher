// Why did each target run? The selection rationale, per CI run.
//
// Run with:  ./graph/query.sh why
//
// This is the question the graph could not answer until selections were
// recorded. The plan's `resolutions` array always carried the audit trail, but it
// lived in an ephemeral file that CI threw away, so the reasoning behind a
// selection evaporated the moment the run exited.
//
// `reason` distinguishes a target whose own files changed from one reached
// transitively through DEPENDS_ON — the difference between "you edited this" and
// "this consumes what you edited". `viaPaths` names the changed paths that
// resolved to it, so a surprising fan-out can be traced to its cause.
//
// `reuseProvenBy` is the citation for work that did NOT happen: the earlier
// PASSED run whose targetHash matched. A skip with no citation is a coverage
// gap, which is what queries/coverage.cypher looks for.
MATCH (run:CIRun)-[sel:SELECTED]->(t:Target)
OPTIONAL MATCH (run)-[cp:CHANGED_PATH]->(t)
OPTIONAL MATCH (run)-[pb:PROVEN_BY]->(proof:TargetRun)
  WHERE pb.target = t.name
RETURN run.id                            AS run,
       t.name                            AS target,
       sel.reason                        AS reason,
       sel.executed                      AS executed,
       collect(DISTINCT cp.path)         AS viaPaths,
       head(collect(DISTINCT proof.id))  AS reuseProvenBy
ORDER BY run DESC, target
LIMIT 60;
