// One-off cleanup: remove :CIRun nodes whose creation time is unrecoverable.
//
// WHY
// ---
// A NULL createdAt is not just a gap. Neo4j sorts NULL FIRST in descending
// order, so a single un-timestamped node makes the ordinary "latest run" query
//
//     MATCH (r:CIRun) RETURN r.id ORDER BY r.createdAt DESC LIMIT 1
//
// return that node forever, regardless of what has been recorded since. The
// query looks correct and answers wrongly.
//
// backfill-cirun-createdat.cypher recovers the time from the epoch embedded in a
// run id. What is left after it are runs whose ids carry no epoch at all --
// fossils of an older harness that used a STATIC nonce, e.g.
//
//     demo-core-e2e-test        <- no epoch anywhere in the id
//
// Their commit shas are synthetic (made inside the demo's throwaway clone, so
// absent from the real repository) and their Commit nodes have no committedAt,
// which means no evidence of when they ran survives anywhere in the graph or in
// git. There is nothing to backfill from. Inventing a timestamp would put a
// fabricated date in a history whose whole purpose is to be citable, so these
// are deleted instead.
//
// SAFETY: run this first. It must report citations = 0. A non-zero count means
// some selection's PROVEN_BY edge cites one of these TargetRuns as the proof
// that work was safely skipped, and deleting it would erase that citation.
//
//     MATCH (r:CIRun) WHERE r.createdAt IS NULL
//     MATCH (r)-[:RAN]->(tr:TargetRun)
//     OPTIONAL MATCH (citer)-[p:PROVEN_BY]->(tr)
//     RETURN r.id AS fossilRun, count(DISTINCT tr) AS targetRuns, count(p) AS citations;
//
// The TargetRuns go too, not just the run: :RAN is the only thing tying them to
// a run, so deleting the CIRun alone would leave them as unreachable orphans
// still holding PASSED statuses and content hashes.

MATCH (r:CIRun)
WHERE r.createdAt IS NULL
OPTIONAL MATCH (r)-[:RAN]->(tr:TargetRun)
WITH collect(DISTINCT r) AS runs, collect(DISTINCT tr) AS targetRuns
WITH runs, targetRuns, size(runs) AS nRuns, size(targetRuns) AS nTargetRuns
FOREACH (x IN targetRuns | DETACH DELETE x)
FOREACH (x IN runs | DETACH DELETE x)
RETURN nRuns AS runsDeleted, nTargetRuns AS targetRunsDeleted;

// ---------------------------------------------------------------- verification
//     MATCH (r:CIRun) RETURN count(*) AS runs, count(r.createdAt) AS withCreatedAt;
//     MATCH (tr:TargetRun) WHERE NOT ()-[:RAN]->(tr) RETURN count(tr) AS orphans;
//
// Expected: withCreatedAt = runs, orphans = 0. Then the latest-run query is
// sound: MATCH (r:CIRun) RETURN r.id ORDER BY r.createdAt DESC LIMIT 1.
