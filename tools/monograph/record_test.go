package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// TestRecordDerivesCacheHit pins the honesty property of the history layer:
// an orchestrator reports a duration, but only the graph knows whether the work
// was actually fresh. A replayed exec must be recorded as a cache hit with an
// unknown duration, never with the earlier execution's number.
func TestRecordDerivesCacheHit(t *testing.T) {
	if os.Getenv("MONOGRAPH_SKIP_NEO4J") != "" {
		t.Skip("MONOGRAPH_SKIP_NEO4J set")
	}
	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		t.Skipf("Neo4j unavailable: %v", err)
	}
	// Registered FIRST so it runs LAST: t.Cleanup is LIFO, and a `defer` here
	// would close the driver before any t.Cleanup body could use it. That is what
	// silently leaked test runs into the shared database — the cleanup opened a
	// session on a closed driver and its error was discarded.
	t.Cleanup(func() { d.Close(ctx) })

	// A unique hash per test invocation, so the assertions do not depend on
	// whatever history the local database already holds.
	hash := fmt.Sprintf("test-hash-%d", os.Getpid())
	target := "libs/authz" // any real target, for the :OF edge
	runA := fmt.Sprintf("test-run-a-%d", os.Getpid())
	runB := fmt.Sprintf("test-run-b-%d", os.Getpid())

	t.Cleanup(func() {
		session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
		defer session.Close(ctx)
		if _, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			_, err := tx.Run(ctx, `
				MATCH (tr:TargetRun {targetHash: $hash}) DETACH DELETE tr`,
				map[string]any{"hash": hash})
			if err != nil {
				return nil, err
			}
			_, err = tx.Run(ctx, `
				MATCH (r:CIRun) WHERE r.id IN $ids DETACH DELETE r`,
				map[string]any{"ids": []any{runA, runB}})
			return nil, err
		}); err != nil {
			// Never silent: a failed cleanup leaves rows in a SHARED database that
			// later assertions and the history queries both read.
			t.Logf("cleanup failed, leaving test data behind: %v", err)
		}
	})

	report := func(id string, ms int64) RunReport {
		return RunReport{
			ID: id, Repo: "monorepo", Orchestrator: "test", Trigger: "test",
			Results: []TargetResult{{
				Target: target, Status: "PASSED", DurationMs: ms,
				TargetHash: hash, Toolchain: "go-lib",
			}},
		}
	}

	if err := RecordRun(ctx, d, report(runA, 250)); err != nil {
		t.Fatalf("first RecordRun: %v", err)
	}
	// Same content, reported again with a duration that is really a replay of
	// the first execution's number.
	if err := RecordRun(ctx, d, report(runB, 250)); err != nil {
		t.Fatalf("second RecordRun: %v", err)
	}

	type row struct {
		cacheHit bool
		duration any
	}
	read := func(runID string) row {
		t.Helper()
		session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
		defer session.Close(ctx)
		res, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			r, err := tx.Run(ctx, `
				MATCH (:CIRun {id: $id})-[:RAN]->(tr:TargetRun)
				RETURN tr.cacheHit AS cacheHit, tr.durationMs AS durationMs`,
				map[string]any{"id": runID})
			if err != nil {
				return nil, err
			}
			rec, err := r.Single(ctx)
			if err != nil {
				return nil, err
			}
			ch, _ := rec.Get("cacheHit")
			dur, _ := rec.Get("durationMs")
			hit, _ := ch.(bool)
			return row{cacheHit: hit, duration: dur}, nil
		})
		if err != nil {
			t.Fatalf("reading %s: %v", runID, err)
		}
		return res.(row)
	}

	first := read(runA)
	if first.cacheHit {
		t.Error("first run marked as a cache hit; nothing had built this content before")
	}
	if first.duration == nil {
		t.Error("first run has no duration; it was genuine work and should be measured")
	}

	second := read(runB)
	if !second.cacheHit {
		t.Error("second run of identical content not marked as a cache hit")
	}
	if second.duration != nil {
		t.Errorf("second run recorded durationMs = %v; a replayed exec's duration is unknown, not the earlier run's number", second.duration)
	}
}

// TestRecordSelectionProvesSkips pins the property that makes the coverage
// question answerable: a skipped target must carry a citation.
//
// The safety claim this project rests on is affected ⊆ executed ∪
// proven-reusable. Before selections were recorded, the middle and right terms
// were the only things in the graph and the left term was thrown away with the
// plan file, so the relation could not be checked after the fact at all. A skip
// was an assertion by the tool rather than a fact with evidence.
//
// The negative half matters as much as the positive: queries/coverage.cypher
// returning nothing only means something if it can detect a genuine violation.
func TestRecordSelectionProvesSkips(t *testing.T) {
	if os.Getenv("MONOGRAPH_SKIP_NEO4J") != "" {
		t.Skip("MONOGRAPH_SKIP_NEO4J set")
	}
	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		t.Skipf("Neo4j unavailable: %v", err)
	}
	// Registered FIRST so it runs LAST: t.Cleanup is LIFO, and a `defer` here
	// would close the driver before any t.Cleanup body could use it. That is what
	// silently leaked test runs into the shared database — the cleanup opened a
	// session on a closed driver and its error was discarded.
	t.Cleanup(func() { d.Close(ctx) })

	passed := fmt.Sprintf("sel-passed-%d", os.Getpid())
	orphan := fmt.Sprintf("sel-orphan-%d", os.Getpid())
	seedRun := fmt.Sprintf("sel-seed-%d", os.Getpid())
	proofRun := fmt.Sprintf("sel-proof-%d", os.Getpid())
	gapRun := fmt.Sprintf("sel-gap-%d", os.Getpid())
	target := "libs/authz"

	t.Cleanup(func() {
		session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
		defer session.Close(ctx)
		if _, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			if _, err := tx.Run(ctx, `
				MATCH (tr:TargetRun) WHERE tr.targetHash IN $hashes DETACH DELETE tr`,
				map[string]any{"hashes": []any{passed, orphan}}); err != nil {
				return nil, err
			}
			_, err := tx.Run(ctx, `
				MATCH (r:CIRun) WHERE r.id IN $ids DETACH DELETE r`,
				map[string]any{"ids": []any{seedRun, proofRun, gapRun}})
			return nil, err
		}); err != nil {
			// Never silent: a failed cleanup leaves rows in a SHARED database that
			// later assertions and the history queries both read.
			t.Logf("cleanup failed, leaving test data behind: %v", err)
		}
	})

	// Seed history: this content has genuinely passed once.
	if err := RecordRun(ctx, d, RunReport{
		ID: seedRun, Repo: "monorepo", Orchestrator: "test", Trigger: "test",
		Results: []TargetResult{{
			Target: target, Status: "PASSED", DurationMs: 10,
			TargetHash: passed, Toolchain: "go-lib",
		}},
	}); err != nil {
		t.Fatalf("seeding history: %v", err)
	}

	planFor := func(hash string) Plan {
		return Plan{
			Repo:           "monorepo",
			ChangedTargets: []string{target},
			Resolutions: []PathResolution{{
				Path: "libs/authz/authz.go", How: ResolvedFile, Targets: []string{target},
			}},
			Targets: []PlanTarget{{
				Name: target, Kind: "go-lib", TestCmd: "go test ./...",
				TargetHash: hash, Runnable: true, Reusable: true,
			}},
		}
	}

	// A run that skipped the target because that exact content had passed.
	if err := RecordRun(ctx, d, RunReport{
		ID: proofRun, Repo: "monorepo", Orchestrator: "test", Trigger: "test",
	}); err != nil {
		t.Fatalf("RecordRun(proof): %v", err)
	}
	if _, err := RecordSelection(ctx, d, proofRun, planFor(passed)); err != nil {
		t.Fatalf("RecordSelection(proof): %v", err)
	}

	// A run that skipped the target on content nothing ever built. Same shape,
	// no justification: this is the violation.
	if err := RecordRun(ctx, d, RunReport{
		ID: gapRun, Repo: "monorepo", Orchestrator: "test", Trigger: "test",
	}); err != nil {
		t.Fatalf("RecordRun(gap): %v", err)
	}
	if _, err := RecordSelection(ctx, d, gapRun, planFor(orphan)); err != nil {
		t.Fatalf("RecordSelection(gap): %v", err)
	}

	// The reason must survive: this target's own file changed.
	if got := selectionReason(t, ctx, d, proofRun, target); got != "changed" {
		t.Errorf("SELECTED.reason = %q, want \"changed\"", got)
	}

	if n := proofCount(t, ctx, d, proofRun, target); n != 1 {
		t.Errorf("%d PROVEN_BY citations for a legitimately reused target, want 1; "+
			"a skip with no citation cannot be distinguished from untested work", n)
	}
	if n := proofCount(t, ctx, d, gapRun, target); n != 0 {
		t.Errorf("%d PROVEN_BY citations for content that never passed, want 0; "+
			"coverage.cypher would report no gap and the check would be worthless", n)
	}
}

func selectionReason(t *testing.T, ctx context.Context, d neo4j.DriverWithContext, runID, target string) string {
	t.Helper()
	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)
	out, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		r, err := tx.Run(ctx, `
			MATCH (:CIRun {id: $id})-[sel:SELECTED]->(:Target {name: $target})
			RETURN sel.reason AS reason`,
			map[string]any{"id": runID, "target": target})
		if err != nil {
			return nil, err
		}
		rec, err := r.Single(ctx)
		if err != nil {
			return nil, err
		}
		v, _ := rec.Get("reason")
		s, _ := v.(string)
		return s, nil
	})
	if err != nil {
		t.Fatalf("reading SELECTED.reason for %s: %v", runID, err)
	}
	return out.(string)
}

func proofCount(t *testing.T, ctx context.Context, d neo4j.DriverWithContext, runID, target string) int64 {
	t.Helper()
	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)
	out, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		r, err := tx.Run(ctx, `
			MATCH (run:CIRun {id: $id})
			OPTIONAL MATCH (run)-[pb:PROVEN_BY]->(:TargetRun)
			  WHERE pb.target = $target
			RETURN count(pb) AS n`,
			map[string]any{"id": runID, "target": target})
		if err != nil {
			return nil, err
		}
		rec, err := r.Single(ctx)
		if err != nil {
			return nil, err
		}
		v, _ := rec.Get("n")
		n, _ := v.(int64)
		return n, nil
	})
	if err != nil {
		t.Fatalf("counting PROVEN_BY for %s: %v", runID, err)
	}
	return out.(int64)
}

// TestCommitFileIndexRoundTrip pins the write/read pair behind --base-sha.
//
// The index is stored on :FileVersion rather than :File deliberately. :File is
// the current snapshot that LoadGraph deletes and rewrites, so it must stay one
// node per path — that is what keeps `MATCH (f:File {repo, path})` in
// AffectedViaCypher single-valued. Folding history into it would have broken the
// Cypher selection path that TestCypherMatchesInMemory guards.
func TestCommitFileIndexRoundTrip(t *testing.T) {
	if os.Getenv("MONOGRAPH_SKIP_NEO4J") != "" {
		t.Skip("MONOGRAPH_SKIP_NEO4J set")
	}
	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		t.Skipf("Neo4j unavailable: %v", err)
	}
	t.Cleanup(func() { d.Close(ctx) })

	sha := fmt.Sprintf("testsha-%d", os.Getpid())
	t.Cleanup(func() {
		session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
		defer session.Close(ctx)
		if _, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			if _, err := tx.Run(ctx, `MATCH (c:Commit {sha: $sha}) DETACH DELETE c`,
				map[string]any{"sha": sha}); err != nil {
				return nil, err
			}
			_, err := tx.Run(ctx, `
				MATCH (fv:FileVersion) WHERE fv.path STARTS WITH 'testpkg/'
				  AND NOT (:Commit)-[:CONTAINS]->(fv)
				DELETE fv`, nil)
			return nil, err
		}); err != nil {
			t.Logf("cleanup failed, leaving test data behind: %v", err)
		}
	})

	g := &Graph{Repo: "monorepo", Files: []File{
		{Path: "testpkg/a.ts", SHA256: "aaa", TargetName: "libs/authz"},
		{Path: "testpkg/b.ts", SHA256: "bbb", TargetName: "libs/authz"},
	}}
	if n, err := RecordCommitFiles(ctx, d, sha, g); err != nil || n != 2 {
		t.Fatalf("RecordCommitFiles: n=%d err=%v", n, err)
	}

	got, ok, err := BaseGraphAtCommit(ctx, d, "monorepo", sha)
	if err != nil || !ok {
		t.Fatalf("BaseGraphAtCommit: ok=%v err=%v", ok, err)
	}
	owners := map[string]string{}
	for _, f := range got.Files {
		owners[f.Path] = f.TargetName
	}
	if owners["testpkg/a.ts"] != "libs/authz" {
		t.Errorf("owner of testpkg/a.ts = %q, want libs/authz; path->owner is the whole point of the index",
			owners["testpkg/a.ts"])
	}
	if len(got.Edges) != 0 {
		t.Errorf("%d edges returned; the file index carries none, and callers rely on that to refuse a deleted-package selection",
			len(got.Edges))
	}

	// Re-recording the same sha with a smaller tree must CONVERGE, not union.
	// Otherwise a deleted file would still look present at that commit.
	g2 := &Graph{Repo: "monorepo", Files: []File{
		{Path: "testpkg/a.ts", SHA256: "aaa", TargetName: "libs/authz"},
	}}
	if _, err := RecordCommitFiles(ctx, d, sha, g2); err != nil {
		t.Fatalf("second RecordCommitFiles: %v", err)
	}
	again, _, err := BaseGraphAtCommit(ctx, d, "monorepo", sha)
	if err != nil {
		t.Fatalf("BaseGraphAtCommit after re-record: %v", err)
	}
	if len(again.Files) != 1 {
		t.Errorf("%d files after re-recording a 1-file tree, want 1; the index unioned instead of converging, so a deleted file would still read as present",
			len(again.Files))
	}

	// An unrecorded commit must report ok=false rather than an empty index,
	// which would be indistinguishable from "nothing was deleted".
	if _, ok, err := BaseGraphAtCommit(ctx, d, "monorepo", "no-such-commit-"+sha); err != nil || ok {
		t.Errorf("unrecorded commit: ok=%v err=%v, want ok=false", ok, err)
	}
}

// TestRecordRunStampsCreatedAt pins the property that makes "which run was the
// latest?" answerable, and the two ways it could silently stop being true.
//
// The field it replaced, startedAt, was copied out of the run report and no
// orchestrator ever emitted one, so every run in the graph carried the empty
// string -- with an index on it, advertising an ordering the data could not
// provide. So this asserts createdAt is stamped server-side on insert, that
// re-recording the same run id does NOT restamp it (otherwise it drifts into
// meaning "last touched" and creation order is lost), and that nothing writes
// startedAt any more.
func TestRecordRunStampsCreatedAt(t *testing.T) {
	if os.Getenv("MONOGRAPH_SKIP_NEO4J") != "" {
		t.Skip("MONOGRAPH_SKIP_NEO4J set")
	}
	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		t.Skipf("Neo4j unavailable: %v", err)
	}
	t.Cleanup(func() { d.Close(ctx) })

	runID := fmt.Sprintf("test-createdat-%d", os.Getpid())
	hash := fmt.Sprintf("test-createdat-hash-%d", os.Getpid())
	t.Cleanup(func() {
		session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
		defer session.Close(ctx)
		if _, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			if _, err := tx.Run(ctx, `MATCH (tr:TargetRun {targetHash: $hash}) DETACH DELETE tr`,
				map[string]any{"hash": hash}); err != nil {
				return nil, err
			}
			_, err := tx.Run(ctx, `MATCH (r:CIRun {id: $id}) DETACH DELETE r`,
				map[string]any{"id": runID})
			return nil, err
		}); err != nil {
			t.Logf("cleanup failed, leaving test data behind: %v", err)
		}
	})

	report := RunReport{
		ID: runID, Repo: "monorepo", Orchestrator: "test", Trigger: "test",
		Results: []TargetResult{{
			Target: "libs/authz", Status: "PASSED", DurationMs: 1,
			TargetHash: hash, Toolchain: "go-lib",
		}},
	}

	read := func() (createdAt any, startedAtIsNull bool) {
		t.Helper()
		session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
		defer session.Close(ctx)
		res, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			r, err := tx.Run(ctx, `
				MATCH (run:CIRun {id: $id})
				RETURN run.createdAt AS createdAt, run.startedAt IS NULL AS noStartedAt`,
				map[string]any{"id": runID})
			if err != nil {
				return nil, err
			}
			rec, err := r.Single(ctx)
			if err != nil {
				return nil, err
			}
			c, _ := rec.Get("createdAt")
			n, _ := rec.Get("noStartedAt")
			noStarted, _ := n.(bool)
			return []any{c, noStarted}, nil
		})
		if err != nil {
			t.Fatalf("reading %s: %v", runID, err)
		}
		pair := res.([]any)
		return pair[0], pair[1].(bool)
	}

	before := time.Now().Add(-time.Minute)
	if err := RecordRun(ctx, d, report); err != nil {
		t.Fatalf("first RecordRun: %v", err)
	}
	first, noStartedAt := read()
	if first == nil {
		t.Fatal("createdAt is null after recording; the run cannot be ordered against any other")
	}
	if !noStartedAt {
		t.Error("startedAt was written; it is the dead field createdAt replaced")
	}
	ts, ok := first.(time.Time)
	if !ok {
		t.Fatalf("createdAt is %T, want a temporal type the graph can ORDER BY", first)
	}
	if ts.Before(before) || ts.After(time.Now().Add(time.Minute)) {
		t.Errorf("createdAt = %v, outside the window this test ran in; it is not a write-time stamp", ts)
	}

	// Same id again: this is what a re-record does, and it must not move.
	if err := RecordRun(ctx, d, report); err != nil {
		t.Fatalf("second RecordRun: %v", err)
	}
	second, _ := read()
	if !second.(time.Time).Equal(ts) {
		t.Errorf("createdAt moved from %v to %v on re-record; ON CREATE makes it creation time, not last-touched",
			ts, second)
	}
}
