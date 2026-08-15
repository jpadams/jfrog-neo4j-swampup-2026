package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// TestEvidenceCommandUsesTheVerifiedFlags pins the six flag names ADR-003
// recorded as verified against JFrog's documentation on 2026-08-10.
//
// The command is rendered by the tool rather than written into the demo for the
// same reason `monograph queries` exists: bench/demotui puts it on a screen in
// front of an audience, and a hand-copied command is one that can silently stop
// matching what CI would run. This test is what keeps the rendered one honest.
func TestEvidenceCommandUsesTheVerifiedFlags(t *testing.T) {
	cmd := EvidenceCommand(EvidenceSubject{})
	for _, flag := range []string{
		"jf evd create",
		"--predicate ",
		"--predicate-type ",
		"--subject-repo-path ",
		"--subject-sha256 ",
		"--key ",
		"--key-alias ",
	} {
		if !strings.Contains(cmd, flag) {
			t.Errorf("rendered command is missing %q:\n%s", flag, cmd)
		}
	}
	if !strings.Contains(cmd, EvidencePredicateType) {
		t.Errorf("command does not carry the predicate type %q:\n%s", EvidencePredicateType, cmd)
	}
	// The placeholders have to look fake. A plausible default would be pasted
	// into a terminal and either fail somewhere less obvious or, worse, attach
	// the attestation to the wrong subject.
	for _, placeholder := range []string{"<artifactory-repo>", "<artifact-sha256>", "<key-alias>"} {
		if !strings.Contains(cmd, placeholder) {
			t.Errorf("command should carry the visibly-fake %s placeholder:\n%s", placeholder, cmd)
		}
	}
}

// TestEvidenceCommandTakesRealSubjects checks the placeholders give way to
// whatever the caller supplied.
func TestEvidenceCommandTakesRealSubjects(t *testing.T) {
	cmd := EvidenceCommand(EvidenceSubject{
		PredicateFile: "coverage.json",
		RepoPath:      "libs-release-local/app/1.2.3/app.tgz",
		SHA256:        "abc123",
		Key:           "/keys/private.pem",
		KeyAlias:      "monograph",
	})
	for _, want := range []string{
		"--predicate coverage.json",
		"--subject-repo-path libs-release-local/app/1.2.3/app.tgz",
		"--subject-sha256 abc123",
		"--key /keys/private.pem --key-alias monograph",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("rendered command is missing %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "<") {
		t.Errorf("a fully-specified subject should leave no placeholders:\n%s", cmd)
	}
}

// TestEvidenceSerialisesEmptyCollectionsAsArrays pins that absence is [] and
// never null.
//
// A Rego gate asking `count(predicate.coverageGaps) == 0` must not have to tell
// "no gaps" from "field missing", and the two are the same JSON if a nil slice
// is allowed through. This is the whole-predicate version of the reason
// PlanTarget emits empty strings rather than omitting keys.
func TestEvidenceSerialisesEmptyCollectionsAsArrays(t *testing.T) {
	blob, err := json.Marshal(Evidence{PredicateType: EvidencePredicateType})
	if err != nil {
		t.Fatalf("marshalling an empty predicate: %v", err)
	}
	for _, field := range []string{
		`"resolutions":[]`, `"affected":[]`, `"executed":[]`,
		`"skipped":[]`, `"coverageGaps":[]`, `"unresolvedPaths":[]`,
	} {
		if !strings.Contains(string(blob), field) {
			t.Errorf("predicate JSON is missing %s; got %s", field, blob)
		}
	}
}

// TestEvidenceOrderIsStable pins the sort itself. The round trip that actually
// matters -- two reads of one recorded run serialising to identical bytes -- is
// asserted against a live graph in TestEvidenceFromGraphAttestsARecordedRun,
// because that is where nondeterminism could come from.
//
// Neo4j promises no ordering, and this document gets signed: without a sort, an
// unchanged run yields a different digest on every read, and a diff between two
// attestations shows churn where nothing changed.
func TestEvidenceOrderIsStable(t *testing.T) {
	ev := Evidence{
		Affected: []EvidenceAffected{{Target: "libs/ui"}, {Target: "apps/admin"}, {Target: "libs/core"}},
		Executed: []EvidenceExecuted{{Target: "libs/ui"}, {Target: "apps/web"}},
		Skipped:  []EvidenceSkipped{{Target: "libs/ui"}, {Target: "apps/admin"}},
		Resolutions: []EvidenceResolution{
			{Path: "libs/ui/src/x.ts", Target: "libs/ui"},
			{Path: "libs/core/src/index.ts", Target: "libs/core"},
		},
		CoverageGaps:    []string{"libs/ui", "apps/admin"},
		UnresolvedPaths: []string{"z.txt", "a.txt"},
	}
	sortEvidence(&ev)

	if got := []string{ev.Affected[0].Target, ev.Affected[1].Target, ev.Affected[2].Target}; got[0] != "apps/admin" || got[2] != "libs/ui" {
		t.Errorf("affected not sorted by target: %v", got)
	}
	if ev.Executed[0].Target != "apps/web" {
		t.Errorf("executed not sorted by target: %v", ev.Executed)
	}
	if ev.Skipped[0].Target != "apps/admin" {
		t.Errorf("skipped not sorted by target: %v", ev.Skipped)
	}
	if ev.Resolutions[0].Path != "libs/core/src/index.ts" {
		t.Errorf("resolutions not sorted by path: %v", ev.Resolutions)
	}
	if ev.CoverageGaps[0] != "apps/admin" || ev.UnresolvedPaths[0] != "a.txt" {
		t.Errorf("string collections not sorted: %v %v", ev.CoverageGaps, ev.UnresolvedPaths)
	}
}

// TestEvidenceCoveredTracksTheGaps pins the set relation the predicate exists to
// carry: affected ⊆ executed ∪ proven-reusable.
func TestEvidenceCoveredTracksTheGaps(t *testing.T) {
	proven := Evidence{Skipped: []EvidenceSkipped{{Target: "libs/ui", ProvenBy: &EvidenceProof{CIRun: "r1"}}}}
	if !proven.Covered() || proven.ProvenSkips() != 1 {
		t.Errorf("a skip with a citation should be covered: covered=%v proven=%d", proven.Covered(), proven.ProvenSkips())
	}
	gap := Evidence{
		Skipped:      []EvidenceSkipped{{Target: "libs/ui"}},
		CoverageGaps: []string{"libs/ui"},
	}
	if gap.Covered() || gap.ProvenSkips() != 0 {
		t.Errorf("a skip with no citation is a gap: covered=%v proven=%d", gap.Covered(), gap.ProvenSkips())
	}
}

// TestEvidenceFromGraphAttestsARecordedRun is the beat-4 case, and it is the one
// the demo actually shows: a selection where NOTHING executed and every skip
// carries a citation.
//
// That case is worth pinning precisely because it is the degenerate-looking one.
// A generator that quietly produced an empty predicate when `executed` was empty
// would still pass a smoke test, while attesting nothing at exactly the moment
// the claim is strongest — coverage proven with no work done.
func TestEvidenceFromGraphAttestsARecordedRun(t *testing.T) {
	ctx, d := evidenceTestDB(t)
	target := "libs/authz"
	requireTarget(t, ctx, d, target)

	hash := fmt.Sprintf("evd-hash-%d", os.Getpid())
	seedRun := fmt.Sprintf("evd-seed-%d", os.Getpid())
	reuseRun := fmt.Sprintf("evd-reuse-%d", os.Getpid())
	cleanupEvidence(t, ctx, d, []string{hash}, []string{seedRun, reuseRun})

	// A run that genuinely executed the target.
	if err := RecordRun(ctx, d, RunReport{
		ID: seedRun, Repo: "monorepo", SHA: "cafe1234", Orchestrator: "test", Trigger: "test",
		Results: []TargetResult{{
			Target: target, Status: "PASSED", DurationMs: 120,
			TargetHash: hash, Toolchain: "go-lib",
		}},
	}); err != nil {
		t.Fatalf("seeding history: %v", err)
	}

	// ...then the same question asked again, with nothing to do: the plan says
	// runnable and reusable, so the target is skipped and cited.
	if err := RecordRun(ctx, d, RunReport{
		ID: reuseRun, Repo: "monorepo", SHA: "cafe1234",
		Orchestrator: "monograph", Trigger: "selection-only",
	}); err != nil {
		t.Fatalf("RecordRun(reuse): %v", err)
	}
	plan := Plan{
		Repo: "monorepo", SHA: "cafe1234",
		ChangedTargets: []string{target},
		Resolutions: []PathResolution{{
			Path: "libs/authz/authz.go", How: ResolvedFile, Targets: []string{target},
		}},
		Targets: []PlanTarget{{
			Name: target, Kind: "go-lib", TestCmd: "go test ./...",
			TargetHash: hash, Runnable: true, Reusable: true,
		}},
	}
	if _, err := RecordSelection(ctx, d, reuseRun, plan); err != nil {
		t.Fatalf("RecordSelection(reuse): %v", err)
	}

	ev, err := EvidenceFromGraph(ctx, d, reuseRun)
	if err != nil {
		t.Fatalf("EvidenceFromGraph: %v", err)
	}

	if ev.PredicateType != EvidencePredicateType {
		t.Errorf("predicateType = %q, want %q", ev.PredicateType, EvidencePredicateType)
	}
	if ev.Repo != "monorepo" || ev.SHA != "cafe1234" || ev.Trigger != "selection-only" {
		t.Errorf("run metadata = repo %q sha %q trigger %q", ev.Repo, ev.SHA, ev.Trigger)
	}
	// createdAt is stamped server-side by RecordRun. An empty one here would mean
	// the datetime never survived decoding, which is how "the latest run" stopped
	// being askable once before.
	if ev.CreatedAt == "" {
		t.Error("createdAt is empty; the server-stamped timestamp did not survive decoding")
	}
	if len(ev.Executed) != 0 {
		t.Errorf("executed = %v, want empty: nothing ran in a selection-only run", ev.Executed)
	}
	if len(ev.Affected) != 1 || ev.Affected[0].Target != target {
		t.Fatalf("affected = %v, want just %s", ev.Affected, target)
	}
	if ev.Affected[0].Executed || !ev.Affected[0].Runnable || ev.Affected[0].Reason != "changed" {
		t.Errorf("affected entry = %+v; want reason=changed, runnable, not executed", ev.Affected[0])
	}
	if len(ev.Skipped) != 1 {
		t.Fatalf("skipped = %v, want one entry", ev.Skipped)
	}
	if ev.Skipped[0].ProvenBy == nil {
		t.Fatal("the skip carries no citation; this is the claim the predicate exists to make")
	}
	if got := ev.Skipped[0].ProvenBy.CIRun; got != seedRun {
		t.Errorf("citation names run %q, want the run that actually passed (%q)", got, seedRun)
	}
	if got := ev.Skipped[0].ProvenBy.Verdict; got != "PASSED" {
		t.Errorf("citation verdict = %q, want PASSED", got)
	}
	if !ev.Covered() || len(ev.CoverageGaps) != 0 {
		t.Errorf("coverageGaps = %v, want none: every skip is proven", ev.CoverageGaps)
	}
	if len(ev.Resolutions) != 1 || ev.Resolutions[0].Path != "libs/authz/authz.go" {
		t.Errorf("resolutions = %v; the audit trail for WHY this was selected is missing", ev.Resolutions)
	}

	// Two reads of the same run must produce the same bytes. This is the property
	// sortEvidence exists for, and asserting it on the hand-built struct alone
	// (TestEvidenceOrderIsStable) leaves the round trip untested -- which is the
	// half that matters, because the document gets signed and Neo4j promises no
	// ordering. A digest that changes when nothing did makes two attestations
	// about identical facts look like a diff.
	again, err := EvidenceFromGraph(ctx, d, reuseRun)
	if err != nil {
		t.Fatalf("second EvidenceFromGraph: %v", err)
	}
	first, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshalling the first read: %v", err)
	}
	second, err := json.Marshal(again)
	if err != nil {
		t.Fatalf("marshalling the second read: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("two reads of one run serialised differently; a signature over this would not be stable\nfirst:  %s\nsecond: %s", first, second)
	}

	// The executed run attests the other half of the same relation.
	seedEv, err := EvidenceFromGraph(ctx, d, seedRun)
	if err != nil {
		t.Fatalf("EvidenceFromGraph(seed): %v", err)
	}
	if len(seedEv.Executed) != 1 || seedEv.Executed[0].Verdict != "PASSED" {
		t.Errorf("executed = %v, want one PASSED entry", seedEv.Executed)
	}
	if seedEv.Executed[0].DurationMs == nil || *seedEv.Executed[0].DurationMs != 120 {
		t.Errorf("durationMs = %v, want 120 for genuinely fresh work", seedEv.Executed[0].DurationMs)
	}
}

// TestEvidenceReportsAnUnprovenSkip is the negative half, and it matters as much
// as the positive one: a predicate whose coverageGaps are always empty proves
// nothing, exactly as an empty queries/coverage.cypher result would mean nothing
// if the query could not detect a real violation.
func TestEvidenceReportsAnUnprovenSkip(t *testing.T) {
	ctx, d := evidenceTestDB(t)
	target := "libs/authz"
	requireTarget(t, ctx, d, target)

	orphan := fmt.Sprintf("evd-orphan-%d", os.Getpid())
	gapRun := fmt.Sprintf("evd-gap-%d", os.Getpid())
	cleanupEvidence(t, ctx, d, []string{orphan}, []string{gapRun})

	// A run that skipped the target on content nothing ever built.
	if err := RecordRun(ctx, d, RunReport{
		ID: gapRun, Repo: "monorepo", Orchestrator: "test", Trigger: "test",
	}); err != nil {
		t.Fatalf("RecordRun(gap): %v", err)
	}
	if _, err := RecordSelection(ctx, d, gapRun, Plan{
		Repo:           "monorepo",
		ChangedTargets: []string{target},
		Targets: []PlanTarget{{
			Name: target, Kind: "go-lib", TestCmd: "go test ./...",
			TargetHash: orphan, Runnable: true, Reusable: true,
		}},
	}); err != nil {
		t.Fatalf("RecordSelection(gap): %v", err)
	}

	ev, err := EvidenceFromGraph(ctx, d, gapRun)
	if err != nil {
		t.Fatalf("EvidenceFromGraph: %v", err)
	}
	if len(ev.Skipped) != 1 {
		t.Fatalf("skipped = %v, want one entry: an unproven skip must still be reported", ev.Skipped)
	}
	if ev.Skipped[0].ProvenBy != nil {
		t.Errorf("skip carries a citation %+v, but nothing ever built this content", ev.Skipped[0].ProvenBy)
	}
	if ev.Covered() {
		t.Error("Covered() is true with an unproven skip; the predicate would assert coverage it does not have")
	}
	if len(ev.CoverageGaps) != 1 || ev.CoverageGaps[0] != target {
		t.Errorf("coverageGaps = %v, want [%s]", ev.CoverageGaps, target)
	}
}

// TestEvidenceRefusesAnUnrecordedRun: attesting a run the graph has never heard
// of is the one output this must not produce. An empty predicate would be a
// signed claim about nothing.
func TestEvidenceRefusesAnUnrecordedRun(t *testing.T) {
	ctx, d := evidenceTestDB(t)
	_, err := EvidenceFromGraph(ctx, d, fmt.Sprintf("evd-nonexistent-%d", os.Getpid()))
	if err == nil {
		t.Fatal("EvidenceFromGraph returned a predicate for a run that was never recorded")
	}
	if !strings.Contains(err.Error(), "not in the graph") {
		t.Errorf("error should say the run is unknown, got: %v", err)
	}
}

// evidenceTestDB is the shared skip-or-connect preamble. Same convention as the
// rest of the DB-dependent tests: skip rather than fail, so `go test ./...` is
// safe with no setup.
func evidenceTestDB(t *testing.T) (context.Context, neo4j.DriverWithContext) {
	t.Helper()
	if os.Getenv("MONOGRAPH_SKIP_NEO4J") != "" {
		t.Skip("MONOGRAPH_SKIP_NEO4J set")
	}
	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		t.Skipf("Neo4j unavailable: %v", err)
	}
	// Registered first so it runs last: t.Cleanup is LIFO, and closing the driver
	// before the row cleanup would leak test data into a shared database.
	t.Cleanup(func() { d.Close(ctx) })
	return ctx, d
}

// requireTarget skips when the monorepo graph has not been loaded.
//
// Without it, a database with history but no :Target nodes produces a predicate
// with an empty affected set and a pile of confusing assertion failures, when the
// real problem is one missing `monograph load`.
func requireTarget(t *testing.T, ctx context.Context, d neo4j.DriverWithContext, name string) {
	t.Helper()
	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)
	n, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, `MATCH (t:Target {name: $name}) RETURN count(t) AS n`,
			map[string]any{"name": name})
		if err != nil {
			return nil, err
		}
		rec, err := res.Single(ctx)
		if err != nil {
			return nil, err
		}
		v, _ := rec.Get("n")
		return v, nil
	})
	if err != nil {
		t.Skipf("checking for target %s: %v", name, err)
	}
	if count, _ := n.(int64); count == 0 {
		t.Skipf("target %s is not in the graph; run `monograph extract | monograph load` first", name)
	}
}

func cleanupEvidence(t *testing.T, ctx context.Context, d neo4j.DriverWithContext, hashes, runIDs []string) {
	t.Helper()
	toAny := func(in []string) []any {
		out := make([]any, len(in))
		for i, s := range in {
			out[i] = s
		}
		return out
	}
	t.Cleanup(func() {
		session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
		defer session.Close(ctx)
		if _, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			if _, err := tx.Run(ctx, `
				MATCH (tr:TargetRun) WHERE tr.targetHash IN $hashes DETACH DELETE tr`,
				map[string]any{"hashes": toAny(hashes)}); err != nil {
				return nil, err
			}
			_, err := tx.Run(ctx, `
				MATCH (r:CIRun) WHERE r.id IN $ids DETACH DELETE r`,
				map[string]any{"ids": toAny(runIDs)})
			return nil, err
		}); err != nil {
			// Never silent: leftovers live in a SHARED database that the history
			// queries and later assertions both read.
			t.Logf("cleanup failed, leaving test data behind: %v", err)
		}
	})
}
