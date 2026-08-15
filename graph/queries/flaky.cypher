// Flaky targets: identical content that produced different verdicts.
//
// Run with:  ./graph/query.sh flaky
//
// This definition is only available because work is keyed by content hash. The
// usual proxy — "this test name fails sometimes" — cannot distinguish a flaky
// test from a test that legitimately broke when the code changed. Grouping by
// targetHash controls for that completely: every run in a group built byte-
// identical inputs with an identical toolchain, so any disagreement in outcome
// is non-determinism, not a code change.
//
// Cached runs are excluded. A replayed exec did not execute, so it cannot
// contribute evidence about determinism either way.
MATCH (tr:TargetRun)-[:OF]->(t:Target)
WHERE coalesce(tr.cacheHit, false) = false
WITH t.name          AS target,
     tr.targetHash   AS targetHash,
     collect(DISTINCT tr.status) AS outcomes,
     count(*)        AS runs,
     sum(CASE tr.status WHEN 'FAILED' THEN 1 ELSE 0 END) AS failures
WHERE size(outcomes) > 1
RETURN target,
       left(targetHash, 12) AS content,
       runs,
       failures,
       // A rate rather than a count, so a target run 100 times is comparable
       // with one run 4 times.
       round(100.0 * failures / runs) AS failureRatePct,
       outcomes
ORDER BY failureRatePct DESC, runs DESC;
