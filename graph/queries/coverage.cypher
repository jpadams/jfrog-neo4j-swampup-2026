// Coverage gaps: a target that was selected, did not run, and has no reuse proof.
//
// Run with:  ./graph/query.sh coverage
//
// The safety property this whole tool rests on is that everything a change could
// affect either runs or is provably unnecessary to run. Stated as a set relation:
//
//     affected  ⊆  executed  ∪  proven-reusable
//
// Every row this query returns is a violation: a target the graph said was
// affected, which nothing executed, and for which no earlier PASSED run with the
// same targetHash exists to justify the skip. That is a target left unverified at
// its current content while CI reported success.
//
// An empty result is the assertion. It should normally be empty, which is why the
// query is worth running rather than the numbers being taken on trust.
//
// Non-runnable targets are excluded deliberately: `proto` and `workspace`
// legitimately appear in an affected set with no work to do, so testCmd = '' is
// nothing to run rather than something skipped. See the README's note that
// affected and *runnable* are different questions.
//
// This is also the shape ADR-003 proposes attesting to. If a JFrog Evidence
// predicate ever carries CI coverage, it should be a serialisation of this
// result, not a separately assembled claim — the two must not be able to
// disagree.
MATCH (run:CIRun)-[sel:SELECTED]->(t:Target)
WHERE sel.executed = false
  AND t.testCmd <> ''
OPTIONAL MATCH (run)-[pb:PROVEN_BY]->(:TargetRun)
  WHERE pb.target = t.name
WITH run, t, sel, count(pb) AS proofs
WHERE proofs = 0
RETURN run.id       AS run,
       t.name       AS unverifiedTarget,
       sel.reason   AS whySelected,
       sel.targetHash AS targetHash
ORDER BY run DESC, unverifiedTarget;
