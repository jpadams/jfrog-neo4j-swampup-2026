package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// The tests in this file exercise --base-in / --base-sha through the actual CLI
// entry points (cmdLoad, cmdAffected), not just the library functions underneath.
// resolve_test.go and record_test.go already pin BuildPlanWithBase,
// RecordCommitFiles and BaseGraphAtCommit directly; what was untested is the
// wiring in commands.go: flag validation, the error messages a user actually
// sees, and cmdLoad --sha really reaching Neo4j.

// writeGraphFile serialises g to a temp file and returns its path.
func writeGraphFile(t *testing.T, g *Graph) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "graph-*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	if err := writeJSON(f, g); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	return f.Name()
}

// runCmdAffected runs cmdAffected with stdout captured, so a test can assert on
// both the returned error and, on success, the emitted Plan.
func runCmdAffected(t *testing.T, args []string) (Plan, error) {
	t.Helper()
	real := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	cmdErr := cmdAffected(args)
	w.Close()
	os.Stdout = real

	var out []byte
	buf := make([]byte, 64*1024)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			break
		}
	}

	if cmdErr != nil {
		return Plan{}, cmdErr
	}
	var p Plan
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("decoding plan output: %v\noutput: %s", err, out)
	}
	return p, nil
}

// connectOrSkip is the shared live-Neo4j setup used by every test below,
// following the skip convention already established in record_test.go.
func connectOrSkip(t *testing.T) neo4j.DriverWithContext {
	t.Helper()
	if os.Getenv("MONOGRAPH_SKIP_NEO4J") != "" {
		t.Skip("MONOGRAPH_SKIP_NEO4J set")
	}
	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		t.Skipf("Neo4j unavailable: %v", err)
	}
	t.Cleanup(func() { d.Close(ctx) })
	return d
}

func cleanupTestCommit(t *testing.T, d neo4j.DriverWithContext, sha string) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
		defer session.Close(ctx)
		if _, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			return tx.Run(ctx, `MATCH (c:Commit {sha: $sha}) DETACH DELETE c`,
				map[string]any{"sha": sha})
		}); err != nil {
			t.Logf("cleanup failed, leaving test data behind: %v", err)
		}
	})
}

// headWithoutFile returns a copy of g with path removed from the file index,
// as a fresh extract on the post-delete tree would produce.
func headWithoutFile(g *Graph, path string) *Graph {
	head := *g
	head.Files = nil
	for _, f := range g.Files {
		if f.Path != path {
			head.Files = append(head.Files, f)
		}
	}
	return &head
}

// headWithoutTarget returns a copy of g with an entire target — its node, its
// files, and every edge mentioning it — removed, mirroring what an extract on a
// tree with that package deleted would produce.
func headWithoutTarget(g *Graph, name string) *Graph {
	head := *g
	head.Targets = nil
	for _, tg := range g.Targets {
		if tg.Name != name {
			head.Targets = append(head.Targets, tg)
		}
	}
	head.Files = nil
	for _, f := range g.Files {
		if f.TargetName != name {
			head.Files = append(head.Files, f)
		}
	}
	head.Edges = nil
	for _, e := range g.Edges {
		if e.From != name && e.To != name {
			head.Edges = append(head.Edges, e)
		}
	}
	return &head
}

// TestCmdAffectedRejectsBothBaseFlags pins the flag-level validation error a
// user actually sees when passing --base-in and --base-sha together. Needs no
// Neo4j: the check runs before either base is read or the DB is touched.
func TestCmdAffectedRejectsBothBaseFlags(t *testing.T) {
	g := loadTestGraph(t)
	graphFile := writeGraphFile(t, g)

	_, err := runCmdAffected(t, []string{
		"--in", graphFile,
		"--base-in", "unused.json",
		"--base-sha", "unused-sha",
		"--changed", "pnpm-lock.yaml",
	})
	if err == nil {
		t.Fatal("expected an error when both --base-in and --base-sha are set")
	}
	if got := err.Error(); got != "--base-in and --base-sha are two ways to answer the same question; pass one" {
		t.Errorf("error = %q", got)
	}
}

// TestCmdAffectedBaseShaUnrecorded pins the error a user sees when --base-sha
// names a commit `load --sha` never ran for. Silence here would be
// indistinguishable from "nothing was deleted".
func TestCmdAffectedBaseShaUnrecorded(t *testing.T) {
	connectOrSkip(t)
	g := loadTestGraph(t)
	graphFile := writeGraphFile(t, g)

	sha := fmt.Sprintf("no-such-commit-%d", os.Getpid())
	_, err := runCmdAffected(t, []string{
		"--in", graphFile,
		"--base-sha", sha,
		"--changed", "pnpm-lock.yaml",
	})
	if err == nil {
		t.Fatal("expected an error for an unrecorded --base-sha commit")
	}
	if got := err.Error(); !strings.Contains(got, "no file index recorded for commit "+sha) {
		t.Errorf("error = %q, want it to name the missing commit and the fix", got)
	}
}

// TestCmdLoadShaThenAffectedBaseShaResolvesDeletion is the end-to-end path this
// whole feature exists for: `load --sha` at the base commit, then `affected
// --base-sha` on a branch that deletes a top-level file, through the real CLI
// commands a user runs — not the library functions directly.
func TestCmdLoadShaThenAffectedBaseShaResolvesDeletion(t *testing.T) {
	d := connectOrSkip(t)
	base := loadTestGraph(t)

	sha := fmt.Sprintf("testsha-cli-%d", os.Getpid())
	cleanupTestCommit(t, d, sha)

	const deleted = "pnpm-lock.yaml"
	if n, err := RecordCommitFiles(context.Background(), d, sha, base); err != nil || n == 0 {
		t.Fatalf("RecordCommitFiles: n=%d err=%v", n, err)
	}

	head := headWithoutFile(base, deleted)
	headFile := writeGraphFile(t, head)

	// Without --base-sha: the deletion is unresolved and nothing is selected —
	// the failure mode this flag exists to fix.
	if _, err := runCmdAffected(t, []string{
		"--in", headFile, "--changed", deleted, "--no-reuse",
	}); err == nil {
		t.Error("expected an error resolving a deleted top-level file with no base supplied")
	}

	plan, err := runCmdAffected(t, []string{
		"--in", headFile, "--base-sha", sha, "--changed", deleted, "--no-reuse",
	})
	if err != nil {
		t.Fatalf("affected --base-sha: %v", err)
	}
	if len(plan.Targets) == 0 {
		t.Fatal("--base-sha resolved a deleted lockfile to zero targets")
	}
	var deletedHow string
	for _, r := range plan.Resolutions {
		if r.Path == deleted {
			deletedHow = r.How
		}
	}
	if deletedHow != ResolvedDeleted {
		t.Errorf("resolution.how = %q, want %q", deletedHow, ResolvedDeleted)
	}
}

// TestCmdAffectedBaseShaRefusesDeletedPackage pins the CLI-level refusal:
// --base-sha versions the file index only, so a deleted whole target cannot
// reach its surviving consumers and the command must error rather than emit an
// empty plan — unless the caller explicitly accepts that with
// --allow-unknown-paths.
func TestCmdAffectedBaseShaRefusesDeletedPackage(t *testing.T) {
	d := connectOrSkip(t)
	base := loadTestGraph(t)

	sha := fmt.Sprintf("testsha-cli-pkg-%d", os.Getpid())
	cleanupTestCommit(t, d, sha)

	const gone = "libs/ui"
	if _, err := RecordCommitFiles(context.Background(), d, sha, base); err != nil {
		t.Fatalf("RecordCommitFiles: %v", err)
	}

	head := headWithoutTarget(base, gone)
	headFile := writeGraphFile(t, head)
	changed := "libs/ui/src/index.ts,libs/ui/monograph.toml"

	_, err := runCmdAffected(t, []string{
		"--in", headFile, "--base-sha", sha, "--changed", changed, "--no-reuse",
	})
	if err == nil {
		t.Fatal("expected a refusal when a deleted package's dependents cannot be determined")
	}
	if got := err.Error(); !strings.Contains(got, gone) || !strings.Contains(got, "--base-in") {
		t.Errorf("error = %q, want it to name %q and point at --base-in", got, gone)
	}

	// --allow-unknown-paths downgrades the refusal to a warning and an
	// incomplete-but-non-empty-error plan.
	if _, err := runCmdAffected(t, []string{
		"--in", headFile, "--base-sha", sha, "--changed", changed,
		"--no-reuse", "--allow-unknown-paths",
	}); err != nil {
		t.Errorf("--allow-unknown-paths should downgrade the refusal, got: %v", err)
	}
}

// TestCmdLoadShaRecordsFileIndex pins that `load --sha` really reaches Neo4j and
// writes the versioned index, not just that RecordCommitFiles works when called
// directly (record_test.go's TestCommitFileIndexRoundTrip covers that half).
func TestCmdLoadShaRecordsFileIndex(t *testing.T) {
	d := connectOrSkip(t)
	g := loadTestGraph(t)
	graphFile := writeGraphFile(t, g)

	sha := fmt.Sprintf("testsha-cmdload-%d", os.Getpid())
	cleanupTestCommit(t, d, sha)

	if err := cmdLoad([]string{"--in", graphFile, "--sha", sha}); err != nil {
		t.Fatalf("cmdLoad: %v", err)
	}

	_, ok, err := BaseGraphAtCommit(context.Background(), d, g.Repo, sha)
	if err != nil {
		t.Fatalf("BaseGraphAtCommit: %v", err)
	}
	if !ok {
		t.Error("cmdLoad --sha did not leave a queryable file index behind")
	}
}

// TestOrdinaryChangeIdenticalWithOrWithoutBase pins the README's claim that a
// base graph only ever ADDS deletion support: an ordinary add/modify must
// select exactly the same targets whether or not a base graph is supplied.
func TestOrdinaryChangeIdenticalWithOrWithoutBase(t *testing.T) {
	g := loadTestGraph(t)
	changed := []string{"libs/core/src/index.ts"}

	without := BuildPlan(g, "", changed)
	with := BuildPlanWithBase(g, g, "", changed)

	a := affectedNames(without)
	b := affectedNames(with)
	if !equalStringSets(a, b) {
		t.Errorf("targets without a base = %v, with an (identical) base = %v; a base graph must not change ordinary resolution", a, b)
	}
}
