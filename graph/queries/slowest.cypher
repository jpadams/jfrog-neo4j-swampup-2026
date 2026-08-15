// Slowest targets, for scheduling: start the long poles first.
//
// Run with:  ./graph/query.sh slowest
//
// Only genuinely measured runs count. `monograph record` stores durationMs as
// null when it derives that a run was satisfied from cache, because a replayed
// exec reports the *earlier* execution's number — so averaging those would
// quietly weight the result toward whatever happened to be cached.
// percentileCont aggregates over rows and groups by the non-aggregated return
// items, so it takes tr.durationMs directly. Passing it a collect()ed list is a
// type error (LIST<ANY> where FLOAT is expected).
MATCH (tr:TargetRun)-[:OF]->(t:Target)
WHERE tr.durationMs IS NOT NULL
  AND coalesce(tr.cacheHit, false) = false
RETURN t.name                                    AS target,
       t.kind                                    AS kind,
       count(*)                                  AS measuredRuns,
       round(percentileCont(tr.durationMs, 0.5))  AS medianMs,
       round(percentileCont(tr.durationMs, 0.95)) AS p95Ms,
       max(tr.durationMs)                         AS maxMs
ORDER BY medianMs DESC;
