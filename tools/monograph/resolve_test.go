package main

import (
	"sort"
	"testing"
)

// TestPathResolution pins how each kind of changed path is interpreted.
//
// This exists because the original implementation had a single prefix fallback
// that resolved *anything* it did not recognise to the root workspace target.
// A typo, or repo-root-relative paths from `git diff` without --relative,
// therefore selected every target while looking like a successful narrow run —
// silently turning the tool into a full rebuild.
func TestPathResolution(t *testing.T) {
	g := loadTestGraph(t)

	cases := []struct {
		name        string
		path        string
		wantHow     string
		wantTargets []string
		why         string
	}{
		{
			name:        "an indexed file",
			path:        "libs/core/src/index.ts",
			wantHow:     ResolvedFile,
			wantTargets: []string{"libs/core"},
			why:         "the ordinary case: an exact hit in the file index",
		},
		{
			name:        "a directory of one target",
			path:        "docs",
			wantHow:     ResolvedDirectory,
			wantTargets: []string{"docs"},
			why:         "a directory is expanded to the targets owning files under it, not guessed at",
		},
		{
			name:        "a directory spanning several targets",
			path:        "libs",
			wantHow:     ResolvedDirectory,
			wantTargets: []string{"libs/authz", "libs/core", "libs/ui"},
			why:         "libs/ owns no target itself; it must expand to all three, not collapse to workspace",
		},
		{
			name:        "a deleted file inside a known target",
			path:        "libs/core/src/gone.ts",
			wantHow:     ResolvedDeleted,
			wantTargets: []string{"libs/core"},
			why:         "deletions are absent from the index, so a specific target root prefix is the only signal",
		},
		{
			name:        "a deleted top-level file",
			path:        "pnpm-lock.yaml",
			wantHow:     ResolvedFile,
			wantTargets: []string{"workspace"},
			why:         "root-level files really do belong to the workspace target",
		},
		{
			name:        "a build sidecar",
			path:        "apps/web/tsconfig.tsbuildinfo",
			wantHow:     ResolvedIgnored,
			wantTargets: nil,
			why:         "tsc sidecars are never indexed; a diff mentioning one must select nothing",
		},
		{
			name:        "a typo in a directory name",
			path:        "libs/coer/src/index.ts",
			wantHow:     ResolvedUnknown,
			wantTargets: nil,
			why:         "a typo must be reported, never silently widened to the whole repo",
		},
		{
			name:        "a repository-root-relative path",
			path:        "monorepo/libs/core/src/index.ts",
			wantHow:     ResolvedUnknown,
			wantTargets: nil,
			why:         "this is what `git diff --name-only` emits without --relative; it used to select everything",
		},
		{
			name:        "a path outside the monorepo",
			path:        "tools/monograph/main.go",
			wantHow:     ResolvedUnknown,
			wantTargets: nil,
			why:         "the tooling is not part of the subject under test",
		},

		// The cases below were all live defects. Each one resolved to something
		// that looked plausible while being wrong, which is why they are pinned
		// individually rather than folded into one "bad input" test.
		{
			name:        "a typo in a top-level filename",
			path:        "REDME.md",
			wantHow:     ResolvedUnknown,
			wantTargets: nil,
			why:         "the last surviving workspace catch-all: this resolved to workspace and fanned out to every compiled target, exit 0, no warning",
		},
		{
			name:        "a nonexistent top-level file",
			path:        "nosuchfile.txt",
			wantHow:     ResolvedUnknown,
			wantTargets: nil,
			why:         "no slash is not evidence of workspace ownership; a real root-level file is caught by rule 1 because extract indexes it",
		},
		{
			name:        "an indexed top-level file still resolves",
			path:        "tsconfig.base.json",
			wantHow:     ResolvedFile,
			wantTargets: []string{"workspace"},
			why:         "removing the catch-all must not break genuine root-level files, which rule 1 owns",
		},
		{
			name:        "a path containing a parent traversal",
			path:        "docs/../libs/core/src/index.ts",
			wantHow:     ResolvedFile,
			wantTargets: []string{"libs/core"},
			why:         "unnormalised, this prefix-matched the docs target and ran only the markdown lint while libs/core was what changed",
		},
		{
			name:        "a directory with a trailing slash",
			path:        "libs/core/",
			wantHow:     ResolvedDirectory,
			wantTargets: []string{"libs/core"},
			why:         "Clean strips the slash so this is a directory, not the catch-all `deleted` classification it used to get",
		},
		{
			name:        "a doubled separator",
			path:        "libs//core",
			wantHow:     ResolvedDirectory,
			wantTargets: []string{"libs/core"},
			why:         "Clean collapses it; previously a hard error for a path that is merely ugly",
		},
		{
			name:        "an escaping path",
			path:        "../etc/passwd",
			wantHow:     ResolvedUnknown,
			wantTargets: nil,
			why:         "Clean leaves a leading .. in place deliberately, so escaping paths still resolve to nothing",
		},
		{
			name:        "an absolute path",
			path:        "/libs/core/src/index.ts",
			wantHow:     ResolvedUnknown,
			wantTargets: nil,
			why:         "Clean preserves the leading slash, so it matches no indexed path",
		},
		{
			name:        "the monorepo root itself",
			path:        ".",
			wantHow:     ResolvedUnknown,
			wantTargets: nil,
			why:         "`.` used to reach the workspace catch-all and select 9 targets while omitting docs and infra",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveChangedPaths(g, []string{tc.path})
			if len(got) != 1 {
				t.Fatalf("expected 1 resolution, got %d", len(got))
			}
			r := got[0]
			if r.How != tc.wantHow {
				t.Errorf("how = %q, want %q\n(%s)", r.How, tc.wantHow, tc.why)
			}
			targets := append([]string{}, r.Targets...)
			sort.Strings(targets)
			if !equalStringSets(targets, tc.wantTargets) {
				t.Errorf("targets = %v, want %v\n(%s)", targets, tc.wantTargets, tc.why)
			}
		})
	}
}

// TestUnresolvedPathsAreReported guards the reporting path that `monograph
// affected` turns into a hard error.
func TestUnresolvedPathsAreReported(t *testing.T) {
	g := loadTestGraph(t)

	resolutions := ResolveChangedPaths(g, []string{
		"libs/core/src/index.ts",          // fine
		"monorepo/libs/core/src/index.ts", // wrong prefix
		"nope/nope.ts",                    // nonexistent
	})
	unresolved := UnresolvedPaths(resolutions)
	want := []string{"monorepo/libs/core/src/index.ts", "nope/nope.ts"}
	if !equalStringSets(unresolved, want) {
		t.Errorf("unresolved = %v, want %v", unresolved, want)
	}
}

// TestUnresolvedPathSelectsNothing is the regression that matters most: an
// unrecognised path must not widen the selection.
func TestUnresolvedPathSelectsNothing(t *testing.T) {
	g := loadTestGraph(t)

	plan := BuildPlan(g, "", []string{"monorepo/libs/core/src/index.ts"})
	if len(plan.ChangedTargets) != 0 {
		t.Errorf("changed targets = %v, want none; an unresolvable path must not select anything",
			plan.ChangedTargets)
	}
	if n := len(plan.Runnable()); n != 0 {
		t.Errorf("%d targets would run for an unresolvable path; it previously selected the whole repo", n)
	}
}

// TestBaseGraphResolvesDeletions pins --base-in's contract.
//
// Rules 0-3 read the graph extracted from the post-change tree, which is correct
// for additions and structurally cannot work for deletions: a deleted top-level
// file is in no index and under no surviving target root, so it is unresolved.
// Before --base-in that meant a hard error, or with --allow-unknown-paths a plan
// that selected NOTHING while the deletion genuinely affected every compiled
// target.
func TestBaseGraphResolvesDeletions(t *testing.T) {
	base := loadTestGraph(t)

	// HEAD: the same repo with a top-level file deleted. Drop it from the index
	// exactly as a fresh extract on the post-delete tree would.
	head := loadTestGraph(t)
	const deleted = "pnpm-lock.yaml"
	kept := head.Files[:0]
	var found bool
	for _, f := range head.Files {
		if f.Path == deleted {
			found = true
			continue
		}
		kept = append(kept, f)
	}
	head.Files = kept
	if !found {
		t.Skipf("%s not in the test graph", deleted)
	}

	// Without the base graph: unresolved, and nothing selected.
	if got := ResolveChangedPaths(head, []string{deleted})[0]; got.How != ResolvedUnknown {
		t.Errorf("without --base-in: how = %q, want %q", got.How, ResolvedUnknown)
	}
	if n := len(BuildPlan(head, "", []string{deleted}).Targets); n != 0 {
		t.Errorf("without --base-in: %d targets selected, want 0 (this is the under-selection --base-in fixes)", n)
	}

	// With it: a deletion attributed to its owner at base, and that owner's
	// dependents selected.
	got := ResolveChangedPathsWithBase(head, base, []string{deleted})[0]
	if got.How != ResolvedDeleted {
		t.Errorf("with --base-in: how = %q, want %q", got.How, ResolvedDeleted)
	}
	if !equalStringSets(got.Targets, []string{"workspace"}) {
		t.Errorf("with --base-in: targets = %v, want [workspace]", got.Targets)
	}
	plan := BuildPlanWithBase(head, base, "", []string{deleted})
	if len(plan.Targets) == 0 {
		t.Fatal("with --base-in: no targets selected for a deleted lockfile")
	}
	for _, pt := range plan.Targets {
		if pt.Name == "" {
			t.Error("plan contains a target with an empty name; a target absent from HEAD must be skipped, not zero-valued")
		}
	}

	// A typo appears in NEITHER index, so it must still hard-error. This is the
	// property that makes --base-in safe rather than a re-introduced catch-all.
	if got := ResolveChangedPathsWithBase(head, base, []string{"REDME.md"})[0]; got.How != ResolvedUnknown {
		t.Errorf("a typo with --base-in: how = %q, want %q; --base-in must not become a new catch-all", got.How, ResolvedUnknown)
	}
}

// TestVanishedTargetsDetectsDeletedPackage pins the guard that stops --base-sha
// from silently returning an empty plan for a deleted package.
//
// Rule 4 attributes a deleted path to its owner at base. When that owner is a
// whole package HEAD no longer has, the target is unplannable and its surviving
// consumers are the thing that needs testing — but reaching them needs the base
// commit's EDGE set. --base-in carries one; --base-sha versions only the file
// index. Without the guard the walk propagates nowhere and the plan is empty:
// broken consumers, reported as nothing to do.
func TestVanishedTargetsDetectsDeletedPackage(t *testing.T) {
	base := loadTestGraph(t)

	// HEAD with libs/ui removed entirely: its target, its files, and every edge
	// mentioning it — what an extract on the post-delete tree would produce.
	head := loadTestGraph(t)
	const gone = "libs/ui"
	targets := head.Targets[:0]
	for _, tg := range head.Targets {
		if tg.Name != gone {
			targets = append(targets, tg)
		}
	}
	head.Targets = targets
	files := head.Files[:0]
	for _, f := range head.Files {
		if f.TargetName != gone {
			files = append(files, f)
		}
	}
	head.Files = files
	edges := head.Edges[:0]
	for _, e := range head.Edges {
		if e.From != gone && e.To != gone {
			edges = append(edges, e)
		}
	}
	head.Edges = edges

	changed := []string{"libs/ui/src/index.ts", "libs/ui/monograph.toml"}

	// A base graph with only the file index — what --base-sha reconstructs.
	fileIndexOnly := &Graph{Repo: base.Repo, Files: base.Files}
	plan := BuildPlanWithBase(head, fileIndexOnly, "", changed)
	if got := VanishedTargets(head, plan); !equalStringSets(got, []string{gone}) {
		t.Errorf("VanishedTargets = %v, want [%s]", got, gone)
	}
	if n := len(plan.Targets); n != 0 {
		t.Errorf("file-index-only base selected %d targets; expected 0, which is exactly why the caller must refuse rather than emit this plan", n)
	}

	// A full base graph carries the edges, so the surviving consumers are found
	// and there is nothing to refuse.
	full := BuildPlanWithBase(head, base, "", changed)
	names := map[string]bool{}
	for _, tg := range full.Targets {
		names[tg.Name] = true
	}
	for _, want := range []string{"apps/web", "apps/admin"} {
		if !names[want] {
			t.Errorf("%s not selected with a full base graph; it consumed the deleted package and is now broken", want)
		}
	}
	if names[gone] {
		t.Errorf("%s appears in the plan; a target absent from HEAD has no image, command or hash and must be omitted", gone)
	}
}
