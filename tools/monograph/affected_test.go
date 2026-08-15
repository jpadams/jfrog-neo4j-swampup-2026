package main

import (
	"context"
	"os"
	"sort"
	"testing"
)

const testRepo = "../../monorepo"

func loadTestGraph(t *testing.T) *Graph {
	t.Helper()
	g, err := Extract("monorepo", testRepo)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if err := ComputeHashes(g); err != nil {
		t.Fatalf("ComputeHashes: %v", err)
	}
	return g
}

// TestGoldenAffectedSets is the correctness contract for selection. Each case
// is a change whose correct answer is known by construction from the monorepo's
// shape (see monorepo/docs/README.md).
func TestGoldenAffectedSets(t *testing.T) {
	g := loadTestGraph(t)

	cases := []struct {
		name         string
		changed      []string
		wantChanged  []string
		wantAffected []string
		wantRunnable []string
		proves       string
	}{
		{
			name:         "docs change runs only the docs lint",
			changed:      []string{"docs/README.md"},
			wantChanged:  []string{"docs"},
			wantAffected: []string{"docs"},
			wantRunnable: []string{"docs"},
			proves:       "a docs edit runs one cheap markdown lint, not the seven test suites a full re-run would trigger",
		},
		{
			name:         "editing the lint config re-lints the docs",
			changed:      []string{"docs/.markdownlint.jsonc"},
			wantChanged:  []string{"docs"},
			wantAffected: []string{"docs"},
			wantRunnable: []string{"docs"},
			proves:       "build configuration is content: the lint rules live inside the docs target, so changing them re-runs the lint",
		},
		{
			name:        "a target with nothing to run is still reported as affected",
			changed:     []string{"proto/gen/go/userpb/user.go"},
			wantChanged: []string{"proto"},
			wantAffected: []string{
				"apps/admin", "apps/web", "libs/authz", "libs/core", "libs/ui",
				"proto", "services/api", "services/billing",
			},
			wantRunnable: []string{
				"apps/admin", "apps/web", "libs/authz", "libs/core", "libs/ui",
				"services/api", "services/billing",
			},
			proves: "proto has no test command, so it appears in the affected set but contributes no work — affected and runnable are genuinely different questions",
		},
		{
			name:        "shared TS lib fans out to both apps",
			changed:     []string{"libs/core/src/index.ts"},
			wantChanged: []string{"libs/core"},
			wantAffected: []string{
				"apps/admin", "apps/web", "libs/core", "libs/ui",
			},
			wantRunnable: []string{
				"apps/admin", "apps/web", "libs/core", "libs/ui",
			},
			proves: "transitive fan-out, including apps/admin which reaches core only through ui",
		},
		{
			name:        "proto change crosses the language boundary",
			changed:     []string{"proto/user.proto"},
			wantChanged: []string{"proto"},
			wantAffected: []string{
				"apps/admin", "apps/web", "libs/authz", "libs/core", "libs/ui",
				"proto", "services/api", "services/billing",
			},
			wantRunnable: []string{
				"apps/admin", "apps/web", "libs/authz", "libs/core", "libs/ui",
				"services/api", "services/billing",
			},
			proves: "one IDL change selects both the Go and the TypeScript consumers",
		},
		{
			name:         "leaf service has no dependents",
			changed:      []string{"services/billing/main.go"},
			wantChanged:  []string{"services/billing"},
			wantAffected: []string{"services/billing"},
			wantRunnable: []string{"services/billing"},
			proves:       "no false positives: nothing depends on billing, so nothing else runs",
		},
		{
			name:         "infra is a disconnected component",
			changed:      []string{"infra/main.tf"},
			wantChanged:  []string{"infra"},
			wantAffected: []string{"infra"},
			wantRunnable: []string{"infra"},
			proves:       "the extractor invents no edges between infra and the code graph",
		},
		{
			name:        "shared toolchain config affects every compiled target",
			changed:     []string{"tsconfig.base.json"},
			wantChanged: []string{"workspace"},
			wantAffected: []string{
				"apps/admin", "apps/web", "libs/authz", "libs/core", "libs/ui",
				"proto", "services/api", "services/billing", "workspace",
			},
			wantRunnable: []string{
				"apps/admin", "apps/web", "libs/authz", "libs/core", "libs/ui",
				"services/api", "services/billing",
			},
			proves: "root config is owned by the workspace target and reached via real tsconfig/go.mod references, but docs and infra are still excluded",
		},
		{
			name:        "a deleted file still selects its target",
			changed:     []string{"libs/core/src/deleted-helper.ts"},
			wantChanged: []string{"libs/core"},
			wantAffected: []string{
				"apps/admin", "apps/web", "libs/core", "libs/ui",
			},
			wantRunnable: []string{
				"apps/admin", "apps/web", "libs/core", "libs/ui",
			},
			proves: "deletions fall back to prefix matching, since the path is absent from the graph",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildPlan(g, "", tc.changed)

			if !equalStringSets(plan.ChangedTargets, tc.wantChanged) {
				t.Errorf("changed targets = %v, want %v", plan.ChangedTargets, tc.wantChanged)
			}
			if got := affectedNames(plan); !equalStringSets(got, tc.wantAffected) {
				t.Errorf("affected = %v, want %v\n(%s)", got, tc.wantAffected, tc.proves)
			}

			var runnable []string
			for _, t := range plan.Runnable() {
				runnable = append(runnable, t.Name)
			}
			sort.Strings(runnable)
			if !equalStringSets(runnable, tc.wantRunnable) {
				t.Errorf("runnable = %v, want %v\n(%s)", runnable, tc.wantRunnable, tc.proves)
			}
		})
	}
}

// TestNoDeclaredDependencies guards the central honesty property: manifests
// describe how to build, never what depends on what.
func TestNoDeclaredDependencies(t *testing.T) {
	g := loadTestGraph(t)
	if len(g.Edges) == 0 {
		t.Fatal("no edges extracted; the graph would be vacuously correct")
	}
	for _, e := range g.Edges {
		switch e.Via {
		case ViaGoImport, ViaGoModule, ViaTSWorkspace, ViaTSReference, ViaTSExtends, ViaProtoImport:
		default:
			t.Errorf("edge %s -> %s has unrecognised provenance %q", e.From, e.To, e.Via)
		}
	}
}

// TestExtractionIsDeterministic catches map-iteration order leaking into output,
// which would make target hashes unstable and silently destroy cache reuse.
func TestExtractionIsDeterministic(t *testing.T) {
	first := loadTestGraph(t)
	second := loadTestGraph(t)

	if len(first.Edges) != len(second.Edges) {
		t.Fatalf("edge count differs between runs: %d vs %d", len(first.Edges), len(second.Edges))
	}
	for i := range first.Edges {
		if first.Edges[i] != second.Edges[i] {
			t.Errorf("edge %d differs between runs: %+v vs %+v", i, first.Edges[i], second.Edges[i])
		}
	}

	firstHashes := map[string]string{}
	for _, tg := range first.Targets {
		firstHashes[tg.Name] = tg.TargetHash
	}
	for _, tg := range second.Targets {
		if firstHashes[tg.Name] != tg.TargetHash {
			t.Errorf("target %s hash unstable: %s vs %s", tg.Name, firstHashes[tg.Name], tg.TargetHash)
		}
	}
}

// TestHashesPropagateUpward checks the Merkle property: changing a dependency's
// content must change every dependent's hash, or reuse would be unsound.
func TestHashesPropagateUpward(t *testing.T) {
	base := loadTestGraph(t)

	// Simulate an edit to libs/core by perturbing one of its file hashes.
	mutated := loadTestGraph(t)
	var touched bool
	for i := range mutated.Files {
		if mutated.Files[i].Path == "libs/core/src/index.ts" {
			mutated.Files[i].SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
			touched = true
		}
	}
	if !touched {
		t.Fatal("fixture file libs/core/src/index.ts not found")
	}
	for i := range mutated.Targets {
		mutated.Targets[i].TargetHash = ""
	}
	if err := ComputeHashes(mutated); err != nil {
		t.Fatalf("ComputeHashes: %v", err)
	}

	baseHashes := map[string]string{}
	for _, tg := range base.Targets {
		baseHashes[tg.Name] = tg.TargetHash
	}

	mustChange := map[string]bool{"libs/core": true, "libs/ui": true, "apps/web": true, "apps/admin": true}
	mustNotChange := map[string]bool{"docs": true, "infra": true, "proto": true, "workspace": true, "libs/authz": true, "services/api": true, "services/billing": true}

	for _, tg := range mutated.Targets {
		changed := baseHashes[tg.Name] != tg.TargetHash
		if mustChange[tg.Name] && !changed {
			t.Errorf("%s hash did not change after editing libs/core; reuse would wrongly skip it", tg.Name)
		}
		if mustNotChange[tg.Name] && changed {
			t.Errorf("%s hash changed after editing libs/core; unrelated work would be rebuilt", tg.Name)
		}
	}
}

// TestCypherMatchesInMemory cross-checks the two selection implementations.
// The Cypher path is the project's thesis; the in-memory path is what the tests
// above pin down. If they ever disagree, one of them is lying.
//
// Skipped when Neo4j is unreachable so the unit suite stays hermetic.
func TestCypherMatchesInMemory(t *testing.T) {
	if os.Getenv("MONOGRAPH_SKIP_NEO4J") != "" {
		t.Skip("MONOGRAPH_SKIP_NEO4J set")
	}

	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		t.Skipf("Neo4j unavailable, skipping integration cross-check: %v", err)
	}
	defer d.Close(ctx)

	g := loadTestGraph(t)
	if err := LoadGraph(ctx, d, g); err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	for _, changed := range [][]string{
		{"docs/README.md"},
		{"libs/core/src/index.ts"},
		{"proto/user.proto"},
		{"services/billing/main.go"},
		{"infra/main.tf"},
		{"tsconfig.base.json"},
		{"libs/core/src/index.ts", "services/billing/main.go"},
	} {
		viaCypher, err := AffectedViaCypher(ctx, d, g.Repo, changed)
		if err != nil {
			t.Fatalf("AffectedViaCypher(%v): %v", changed, err)
		}
		inMemory := affectedNames(BuildPlan(g, "", changed))
		if !equalStringSets(viaCypher, inMemory) {
			t.Errorf("selection disagreement for %v:\n  cypher:    %v\n  in-memory: %v", changed, viaCypher, inMemory)
		}
	}
}
