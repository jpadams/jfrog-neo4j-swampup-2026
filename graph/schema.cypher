// Monorepo context graph — constraints and indexes.
// Idempotent: safe to re-apply. Run with:
//   neo4j-cli query --rw -f graph/schema.cypher

// ---------------------------------------------------------------- identity
CREATE CONSTRAINT repo_name IF NOT EXISTS
FOR (r:Repo) REQUIRE r.name IS UNIQUE;

CREATE CONSTRAINT commit_sha IF NOT EXISTS
FOR (c:Commit) REQUIRE c.sha IS UNIQUE;

CREATE CONSTRAINT file_path IF NOT EXISTS
FOR (f:File) REQUIRE f.path IS UNIQUE;

CREATE CONSTRAINT target_name IF NOT EXISTS
FOR (t:Target) REQUIRE t.name IS UNIQUE;

CREATE CONSTRAINT team_name IF NOT EXISTS
FOR (t:Team) REQUIRE t.name IS UNIQUE;

CREATE CONSTRAINT cirun_id IF NOT EXISTS
FOR (r:CIRun) REQUIRE r.id IS UNIQUE;

// A TargetRun is one target's outcome within one CI run.
CREATE CONSTRAINT targetrun_id IF NOT EXISTS
FOR (tr:TargetRun) REQUIRE tr.id IS UNIQUE;

// ---------------------------------------------------------------- lookups
// The reuse query is the hot path: "has this exact content already passed?"
CREATE INDEX targetrun_hash IF NOT EXISTS
FOR (tr:TargetRun) ON (tr.targetHash);

CREATE INDEX targetrun_hash_status IF NOT EXISTS
FOR (tr:TargetRun) ON (tr.targetHash, tr.status);

CREATE INDEX target_hash IF NOT EXISTS
FOR (t:Target) ON (t.targetHash);

CREATE INDEX target_kind IF NOT EXISTS
FOR (t:Target) ON (t.kind);

// Provenance separates parsed facts from inferred ones. Every node carries it.
CREATE INDEX target_provenance IF NOT EXISTS
FOR (t:Target) ON (t.provenance);

CREATE INDEX file_target_lookup IF NOT EXISTS
FOR (f:File) ON (f.targetName);

CREATE INDEX commit_committed IF NOT EXISTS
FOR (c:Commit) ON (c.committedAt);

// "Which run was the latest?" is the entry point to every history question, so
// the ordering property is indexed. This replaced an index on `startedAt`, a
// property `record` copied out of the run report and that no orchestrator ever
// filled -- every run in the graph carried startedAt: "", and the index made a
// dead field look like a usable one. RecordRun now stamps createdAt server-side
// on insert. Drop the old one when applying this to an existing database:
//
//	DROP INDEX cirun_started IF EXISTS;
CREATE INDEX cirun_created IF NOT EXISTS
FOR (r:CIRun) ON (r.createdAt);

// ------------------------------------------------------- versioned file state
// `monograph load --sha <sha>` records the file index AS OF that commit, which is
// what `affected --base-sha` reads back to account for deletions.
//
// This is a DISTINCT label from :File on purpose, and the distinction is
// semantic rather than cosmetic:
//
//   :File        the paths that exist NOW. `load` deletes and rewrites them, so
//                exactly one node per path, which is what makes
//                `MATCH (f:File {repo, path})` in AffectedViaCypher single-valued.
//   :FileVersion a (path, content) pair that existed at some commit. Many per
//                path, one per distinct content.
//
// Folding history into :File would have made that match multi-valued and broken
// the Cypher selection path that TestCypherMatchesInMemory guards. Keeping them
// apart means this whole feature is additive.
//
// Nodes dedupe on (path, sha256), so growth tracks churn rather than
// commits x files. The relationship carries targetName because ownership can
// change without content changing — a new nested monograph.toml re-parents files
// whose bytes never moved.
//
//   (Commit)-[:CONTAINS {targetName}]->(FileVersion)
CREATE CONSTRAINT fileversion_identity IF NOT EXISTS
FOR (fv:FileVersion) REQUIRE (fv.path, fv.sha256) IS UNIQUE;

CREATE INDEX fileversion_path IF NOT EXISTS
FOR (fv:FileVersion) ON (fv.path);

// --------------------------------------------------- selection provenance
// `monograph record --plan` writes three relationship types that answer
// questions the outcome data alone cannot. They carry no constraints because
// Neo4j does not constrain relationships; they are listed here so the schema
// documents the whole model rather than only its nodes:
//
//   (CIRun)-[:SELECTED {reason, executed, targetHash}]->(Target)
//   (CIRun)-[:CHANGED_PATH {path, how}]->(Target)
//   (CIRun)-[:PROVEN_BY {targetHash, target}]->(TargetRun)
//
// Consumed by queries/why.cypher and queries/coverage.cypher. PROVEN_BY is the
// load-bearing one: it is the citation for work that did NOT happen, so a skip
// is a fact with evidence rather than an assertion by the tool.
//
// These three are also what `monograph evidence` reads back out as a JFrog
// Evidence predicate. Nothing is written by that path -- an attestation is a
// signed document, not graph state -- but it means a change to these properties
// changes the shape of an attestation somebody may already have signed.
//
// :Commit is populated by these writes too. It was declared and indexed here for
// some time while nothing created it, because the orchestrator's report carries
// no sha and only `--sha` runs supplied one — the same "schema promising
// structure it does not have" this file warns about below. `record --sha` and
// `--plan` now write it.

// ------------------------------------------------------ deliberately absent
// This file declares constraints ONLY for labels the loader actually creates:
// Repo, Commit, File, Target, Team, CIRun, TargetRun.
//
// Three labels were previously declared here and are now removed, because
// nothing populated them and a schema that promises structure it does not have
// is worse than one that admits the gap:
//
//   :Test / [:COVERS]  test-to-target mapping. Needs either a per-package
//                      convention (cheap, coarse) or coverage instrumentation
//                      (accurate, much more work). Until one is chosen there is
//                      no honest way to answer "which tests cover this target".
//   :Artifact          published build outputs. `produces` globs describe
//                      generated *source*, not published artifacts, so there is
//                      nothing to point at yet.
//   :Concept/:Semantic the graphify layer. Deferred; when added it must stay on
//                      distinct labels with provenance = 'graphify' so inferred
//                      edges can never be mistaken for parsed ones.
//
// Re-add each constraint in the same commit that starts writing its nodes.
