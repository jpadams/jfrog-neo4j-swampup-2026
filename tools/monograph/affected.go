package main

import (
	"path"
	"sort"
	"strings"
)

// PlanTarget is one entry in the selection plan. This struct is the contract
// consumed by both orchestrators, so field names are stable.
type PlanTarget struct {
	Name string `json:"name"`
	Kind string `json:"kind"`

	// Image and TestCmd are always emitted, never omitted. Non-runnable
	// targets carry empty strings rather than absent keys: this JSON is a
	// cross-language contract, and a missing key is a type error to a strict
	// consumer (Dang rejects it outright).
	Image      string `json:"image"`
	TestCmd    string `json:"testCmd"`
	TargetHash string `json:"targetHash"`

	// Runnable is false for targets with nothing to execute (docs, proto).
	// They still appear so the plan is a faithful record of what was affected.
	Runnable bool `json:"runnable"`

	// Reusable is true when a TargetRun with this exact targetHash has already
	// passed. Filled in from the graph's history layer; false without a DB.
	Reusable bool `json:"reusable"`
}

// CodegenStep is a producer target whose outputs must exist before any selected
// target can build.
type CodegenStep struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Image      string   `json:"image"`
	Cmd        string   `json:"cmd"`
	Produces   []string `json:"produces"`
	TargetHash string   `json:"targetHash"`
}

// Plan is the full selection result.
type Plan struct {
	Repo           string   `json:"repo"`
	SHA            string   `json:"sha,omitempty"`
	ChangedFiles   []string `json:"changedFiles"`
	ChangedTargets []string `json:"changedTargets"`

	// Resolutions records how each changed path was mapped to targets, so a
	// plan is auditable rather than having to be trusted.
	Resolutions []PathResolution `json:"resolutions,omitempty"`
	Targets     []PlanTarget     `json:"targets"`

	// Codegen lists producer targets that must run before Targets are tested,
	// in dependency order (a producer that consumes another producer's output
	// comes later).
	//
	// This is deliberately NOT the same set as Targets. A change to libs/authz
	// does not affect proto, yet authz cannot compile without proto's generated
	// bindings. So Codegen covers producers among the selected targets *and
	// their transitive dependencies* — otherwise uncommitted generated code
	// would simply be missing.
	Codegen []CodegenStep `json:"codegen"`
}

// Runnable returns the subset with work to do that is not already reusable —
// exactly the set an orchestrator should execute.
func (p Plan) Runnable() []PlanTarget {
	var out []PlanTarget
	for _, t := range p.Targets {
		if t.Runnable && !t.Reusable {
			out = append(out, t)
		}
	}
	return out
}

// How a changed path was mapped to targets.
const (
	ResolvedFile      = "file"       // an exact match in the graph's file index
	ResolvedDirectory = "directory"  // a directory containing indexed files
	ResolvedDeleted   = "deleted"    // absent from the index, but inside a known target
	ResolvedIgnored   = "ignored"    // a build sidecar the extractor never indexes
	ResolvedUnknown   = "unresolved" // nothing in this repo owns it
)

// PathResolution records how one changed path was interpreted, so a caller can
// tell a real selection from a guess.
type PathResolution struct {
	Path    string   `json:"path"`
	How     string   `json:"how"`
	Targets []string `json:"targets,omitempty"`
}

// ResolveChangedPaths maps changed paths to targets under three explicit rules,
// and reports anything it cannot place.
//
// The rules matter because the old behaviour was a single prefix fallback that
// silently resolved *everything* to the root workspace target — so a typo, or
// repo-root-relative paths like "monorepo/libs/core/x.ts", selected every target
// while looking like a successful narrow run. Selecting everything by accident
// defeats the entire tool, and it fails in the unsafe direction too: someone
// could believe a narrow test ran when the plan was actually a full rebuild.
//
//  0. A build sidecar the extractor skips (see skipFiles) -> nothing.
//  1. Exact file match -> that file's target.
//  2. Directory containing indexed files -> every target owning something under
//     it. This makes `--changed=libs` correctly select libs/core, libs/ui and
//     libs/authz rather than collapsing to the workspace catch-all.
//  3. Otherwise the path is presumed deleted. It resolves only if a *specific*
//     target root is a prefix of it. Anything else is unresolved.
//
// Rule 3 deliberately has NO top-level fallback. It used to resolve any path
// with no slash in it to the root workspace target, on the reasoning that a
// root-level file genuinely does belong to the workspace. The reasoning was
// sound and the consequence was not: a bare typo ("REDME.md") is
// indistinguishable from a real root-level file, so it resolved to workspace and
// fanned out to every compiled target — exit 0, no warning, a plan that reads
// like a narrow run. That is the original bug this whole function exists to
// prevent, surviving in the one place the catch-all still lived.
//
// A top-level file that really exists is caught by rule 1, because `extract`
// indexes it. So the fallback only ever fired for paths the repo does not
// contain. The cost of removing it: deleting a root-level file now reports
// unresolved and fails, since nothing indexes it any more and no specific target
// root prefixes it. That is a loud failure with `--allow-unknown-paths` as the
// escape hatch, which is the correct direction for a tool whose job is knowing
// what to run.
func ResolveChangedPaths(g *Graph, changedFiles []string) []PathResolution {
	return ResolveChangedPathsWithBase(g, nil, changedFiles)
}

// ResolveChangedPathsWithBase is ResolveChangedPaths plus a rule 4: a path that
// HEAD cannot account for, but which the BASE graph indexes, is a genuine
// deletion and resolves to the target that owned it at base.
//
// This exists because rules 0-3 read the graph extracted from the post-change
// tree, which is right for additions and wrong for deletions. Delete a top-level
// file and nothing indexes it and no specific target root prefixes it, so it is
// unresolved — a hard error, or with --allow-unknown-paths a plan selecting
// NOTHING while `pnpm-lock.yaml` disappearing genuinely affects every compiled
// target. Additions need the HEAD graph, deletions need the base graph, and one
// extract cannot serve both.
//
// Resolving against the union of the two indexes dissolves the ambiguity that
// made the top-level catch-all tempting in the first place: a real deletion
// appears in base, a typo appears in NEITHER index and still hard-errors. So
// this buys deletion support without giving back the safety of rule 3.
//
// base may be nil, in which case this is exactly rules 0-3.
func ResolveChangedPathsWithBase(g, base *Graph, changedFiles []string) []PathResolution {
	byPath := make(map[string]string, len(g.Files))
	for _, f := range g.Files {
		byPath[f.Path] = f.TargetName
	}
	basePath := map[string]string{}
	if base != nil {
		for _, f := range base.Files {
			basePath[f.Path] = f.TargetName
		}
	}

	out := make([]PathResolution, 0, len(changedFiles))
	for _, raw := range normalisePaths(changedFiles) {
		res := PathResolution{Path: raw, How: ResolvedUnknown}

		// 0. build sidecars (*.tsbuildinfo and friends) are never indexed, so a
		// diff mentioning one must select nothing rather than fall through to
		// the deleted-file rule and drag in its whole target.
		if matchAnyGlob(skipFiles, raw) {
			res.How = ResolvedIgnored
			out = append(out, res)
			continue
		}

		// 1. exact file
		if owner, known := byPath[raw]; known {
			res.How = ResolvedFile
			if owner != "" {
				res.Targets = []string{owner}
			}
			out = append(out, res)
			continue
		}

		// 2. directory holding indexed files
		dirOwners := map[string]bool{}
		prefix := raw + "/"
		for p, owner := range byPath {
			if strings.HasPrefix(p, prefix) && owner != "" {
				dirOwners[owner] = true
			}
		}
		if len(dirOwners) > 0 {
			res.How = ResolvedDirectory
			res.Targets = sortedKeys(dirOwners)
			out = append(out, res)
			continue
		}

		// 3. presumed deleted: only a specific target root counts, never the
		// root catch-all. Longest root first, so a nested target wins over its
		// ancestor rather than whichever happens to appear first in the slice.
		best := ""
		for _, t := range g.Targets {
			if t.Root == "." {
				continue
			}
			if raw == t.Root || strings.HasPrefix(raw, t.Root+"/") {
				if len(t.Root) > len(best) {
					best = t.Root
					res.How = ResolvedDeleted
					res.Targets = []string{t.Name}
				}
			}
		}

		// 4. still unaccounted for, but the base graph indexed it: a real
		// deletion. Attribute it to the target that owned it at base. That
		// target may itself be gone from HEAD (a whole package removed); the
		// walk handles that, see AffectedTargetsWithBase.
		if res.How == ResolvedUnknown {
			if owner, known := basePath[raw]; known {
				res.How = ResolvedDeleted
				if owner != "" {
					res.Targets = []string{owner}
				}
			}
		}
		out = append(out, res)
	}
	return out
}

// UnresolvedPaths returns the paths nothing in the repo owns.
func UnresolvedPaths(resolutions []PathResolution) []string {
	var out []string
	for _, r := range resolutions {
		if r.How == ResolvedUnknown {
			out = append(out, r.Path)
		}
	}
	return out
}

// ChangedTargets maps a set of changed file paths to the targets that own them.
func ChangedTargets(g *Graph, changedFiles []string) []string {
	return ChangedTargetsWithBase(g, nil, changedFiles)
}

// ChangedTargetsWithBase is ChangedTargets with base-graph deletion support.
func ChangedTargetsWithBase(g, base *Graph, changedFiles []string) []string {
	owners := map[string]bool{}
	for _, r := range ResolveChangedPathsWithBase(g, base, changedFiles) {
		for _, t := range r.Targets {
			owners[t] = true
		}
	}
	return sortedKeys(owners)
}

// AffectedTargets computes reverse reachability: every target that transitively
// depends on a changed target, plus the changed targets themselves.
//
// Edges point dependent -> dependency, so this walks them backwards.
func AffectedTargets(g *Graph, changedTargets []string) []string {
	return AffectedTargetsWithBase(g, nil, changedTargets)
}

// AffectedTargetsWithBase walks dependents over HEAD's edges unioned with the
// base graph's.
//
// The union is what makes a deleted target's consumers reachable. Remove
// libs/ui entirely and HEAD has no edges mentioning it, so a walk over HEAD
// alone propagates nowhere and apps/web — which imported it and is now broken —
// is never selected. The base graph still holds those edges.
//
// The union over-approximates: an edge deliberately removed in HEAD still
// propagates for this run. That is the safe direction, and in practice the
// dependent's own source had to change for the import to go away, so it would be
// selected regardless.
//
// base may be nil, in which case only HEAD's edges are used.
func AffectedTargetsWithBase(g, base *Graph, changedTargets []string) []string {
	dependents := map[string][]string{}
	for _, e := range g.Edges {
		dependents[e.To] = append(dependents[e.To], e.From)
	}
	if base != nil {
		for _, e := range base.Edges {
			dependents[e.To] = append(dependents[e.To], e.From)
		}
	}

	affected := map[string]bool{}
	queue := append([]string{}, changedTargets...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if affected[cur] {
			continue
		}
		affected[cur] = true
		queue = append(queue, dependents[cur]...)
	}

	return sortedKeys(affected)
}

// CodegenSteps returns the producer targets required before `selected` can
// build, in dependency order.
//
// A producer qualifies if it is selected, or if any selected target transitively
// depends on it. Ordering is a topological sort over the producers themselves,
// so a generator that consumes another generator's output runs second.
func CodegenSteps(g *Graph, selected []string) []CodegenStep {
	deps := map[string][]string{}
	for _, e := range g.Edges {
		deps[e.From] = append(deps[e.From], e.To)
	}
	byName := g.TargetByName()

	// Forward closure: everything the selected targets rely on, plus themselves.
	closure := map[string]bool{}
	queue := append([]string{}, selected...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if closure[cur] {
			continue
		}
		closure[cur] = true
		queue = append(queue, deps[cur]...)
	}

	producers := map[string]bool{}
	for name := range closure {
		if t, ok := byName[name]; ok && t.CodegenCmd != "" {
			producers[name] = true
		}
	}
	if len(producers) == 0 {
		return []CodegenStep{}
	}

	// Depth-first post-order over producer-to-producer edges gives dependency
	// order. Edges through non-producers still count: if A's generator consumes
	// B's output via an intermediate target, B must still run first.
	var (
		ordered []string
		state   = map[string]int{}
	)
	const (
		unvisited = iota
		inProgress
		done
	)
	var visit func(string)
	visit = func(n string) {
		if state[n] != unvisited {
			return
		}
		state[n] = inProgress
		for _, d := range sortedCopy(deps[n]) {
			visit(d)
		}
		state[n] = done
		if producers[n] {
			ordered = append(ordered, n)
		}
	}
	for _, n := range sortedKeys(producers) {
		visit(n)
	}

	steps := make([]CodegenStep, 0, len(ordered))
	for _, name := range ordered {
		t := byName[name]
		steps = append(steps, CodegenStep{
			Name:       t.Name,
			Kind:       t.Kind,
			Image:      t.Image,
			Cmd:        t.CodegenCmd,
			Produces:   t.Produces,
			TargetHash: t.TargetHash,
		})
	}
	return steps
}

// BuildPlan assembles the selection plan for a set of changed files.
func BuildPlan(g *Graph, sha string, changedFiles []string) Plan {
	return BuildPlanWithBase(g, nil, sha, changedFiles)
}

// BuildPlanWithBase is BuildPlan with an optional base graph, which is how
// deletions get accounted for. See ResolveChangedPathsWithBase.
func BuildPlanWithBase(g, base *Graph, sha string, changedFiles []string) Plan {
	changed := ChangedTargetsWithBase(g, base, changedFiles)
	affected := AffectedTargetsWithBase(g, base, changed)
	byName := g.TargetByName()

	plan := Plan{
		Repo:           g.Repo,
		SHA:            sha,
		ChangedFiles:   normalisePaths(changedFiles),
		ChangedTargets: changed,
		Resolutions:    ResolveChangedPathsWithBase(g, base, changedFiles),
		Targets:        []PlanTarget{},
	}
	for _, name := range affected {
		t, ok := byName[name]
		if !ok {
			// Selected but gone from HEAD: a deleted target, reached via a base
			// edge. It has no image, command or hash, so there is nothing to
			// plan. Its surviving dependents are already in `affected`. Emitting
			// a zero-value PlanTarget here would put an entry with an empty name
			// and empty testCmd into the plan, which Dang would reject as a
			// missing required field.
			continue
		}
		plan.Targets = append(plan.Targets, PlanTarget{
			Name:       t.Name,
			Kind:       t.Kind,
			Image:      t.Image,
			TestCmd:    t.TestCmd,
			TargetHash: t.TargetHash,
			Runnable:   t.Runnable(),
		})
	}
	plan.Codegen = CodegenSteps(g, affected)
	return plan
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func normalisePaths(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Clean before matching. Only "./" used to be stripped, so
		// "docs/../libs/core/src/index.ts" kept its literal form, matched the
		// docs target root by prefix, and selected the docs markdown lint alone
		// — a narrow-looking run that tested none of the code that changed.
		//
		// Clean also collapses "libs//core" and strips a trailing slash, so
		// those classify as `directory` instead of the catch-all `deleted`.
		//
		// It deliberately does NOT rescue escaping or absolute paths: Clean
		// leaves a leading ".." or "/" in place, so they still match nothing and
		// are reported unresolved rather than silently resolving somewhere.
		p = path.Clean(p)
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// VanishedTargets returns selected targets that no longer exist in the graph.
//
// Only base-graph resolution can produce these: rules 0-3 read HEAD's own index,
// so every target they name exists. Rule 4 attributes a deleted path to its owner
// AT BASE, and that owner may be a whole package that HEAD no longer has.
//
// This matters because such a target is unplannable — no image, no command, no
// hash — while its surviving consumers are exactly what needs testing. Reaching
// them requires the base commit's EDGE set, which `--base-in` carries and
// `--base-sha` does not (only the file index is versioned). Without those edges
// the walk propagates nowhere and the plan comes back empty: a deleted package
// with broken consumers, reported as nothing to do. Callers use this to refuse
// rather than to emit that plan.
func VanishedTargets(g *Graph, plan Plan) []string {
	byName := g.TargetByName()
	var out []string
	for _, name := range plan.ChangedTargets {
		if _, ok := byName[name]; !ok {
			out = append(out, name)
		}
	}
	return sortedCopy(out)
}
