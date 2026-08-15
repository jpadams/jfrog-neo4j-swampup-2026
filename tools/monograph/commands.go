package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// TargetResult is one target's outcome, reported by an orchestrator.
//
// There is no CacheHit field: an orchestrator cannot know whether the engine
// replayed a cached exec. RecordRun derives it from the graph.
type TargetResult struct {
	Target     string `json:"target"`
	Status     string `json:"status"` // PASSED | FAILED | SKIPPED
	DurationMs int64  `json:"durationMs"`
	TargetHash string `json:"targetHash"`
	Toolchain  string `json:"toolchain"`
}

// RunReport is what `monograph record` ingests.
type RunReport struct {
	ID   string `json:"id"`
	Repo string `json:"repo"`
	SHA  string `json:"sha"`
	// No StartedAt: nothing ever emitted one, so every run recorded an empty
	// string into an indexed property. `record` stamps CIRun.createdAt itself on
	// insert instead -- see RecordRun. A report that still carries a startedAt
	// key is accepted and ignored, so older reports keep decoding.
	Trigger      string         `json:"trigger"`
	Orchestrator string         `json:"orchestrator"`
	Results      []TargetResult `json:"results"`
}

func cmdLoad(args []string) error {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	in := fs.String("in", "-", "graph.json to read (- for stdin)")
	sha := fs.String("sha", "", "commit this graph was extracted at; records the file index so `affected --base-sha` can resolve deletions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g, err := readGraph(*in)
	if err != nil {
		return err
	}

	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		return err
	}
	defer d.Close(ctx)

	if err := LoadGraph(ctx, d, g); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "loaded %d targets, %d files, %d edges into %s\n",
		len(g.Targets), len(g.Files), len(g.Edges), envOr("NEO4J_URI", "neo4j://localhost:7687"))

	// The versioned file index is opt-in, because it is only useful if the caller
	// knows which commit the graph was extracted at. LoadGraph deliberately
	// rewrites the current snapshot; this records history alongside it.
	if *sha != "" {
		n, err := RecordCommitFiles(ctx, d, *sha, g)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "recorded the file index at %s: %d files\n", *sha, n)
	}
	return nil
}

func cmdAffected(args []string) error {
	fs := flag.NewFlagSet("affected", flag.ExitOnError)
	in := fs.String("in", "graph.json", "graph.json to read (- for stdin)")
	baseIn := fs.String("base-in", "", "graph.json extracted at the base commit; lets deletions resolve to the target that owned them")
	baseSha := fs.String("base-sha", "", "resolve deletions against the file index recorded for this commit by `load --sha` (no second extract needed)")
	changed := fs.String("changed", "", "comma- or newline-separated changed file paths")
	sha := fs.String("sha", "", "compute changed files from git (diff against --base)")
	base := fs.String("base", "HEAD~1", "base ref when --sha is used")
	repoDir := fs.String("repo", "monorepo", "monorepo path, used to scope git output")
	viaCypher := fs.Bool("via-cypher", false, "resolve affected targets with a Neo4j query instead of in memory")
	crossCheck := fs.Bool("cross-check", false, "compute both ways and fail if they disagree")
	noReuse := fs.Bool("no-reuse", false, "skip the history lookup that marks targets reusable")
	allowUnknown := fs.Bool("allow-unknown-paths", false, "warn instead of failing when a changed path matches no target")
	if err := fs.Parse(args); err != nil {
		return err
	}

	g, err := readGraph(*in)
	if err != nil {
		return err
	}

	// The base graph is optional and only ever widens what can be resolved: it
	// accounts for paths that existed before the change and do not exist now.
	// Without it, deleting a top-level file is unresolvable — see
	// ResolveChangedPathsWithBase.
	var baseGraph *Graph
	if *baseIn != "" && *baseSha != "" {
		return fmt.Errorf("--base-in and --base-sha are two ways to answer the same question; pass one")
	}
	if *baseIn != "" {
		baseGraph, err = readGraph(*baseIn)
		if err != nil {
			return fmt.Errorf("--base-in: %w", err)
		}
		if baseGraph.Repo != g.Repo {
			return fmt.Errorf("--base-in is for repo %q but --in is for %q; comparing two different repos would attribute deletions to the wrong targets",
				baseGraph.Repo, g.Repo)
		}
	}

	var changedFiles []string
	switch {
	case *changed != "":
		changedFiles = splitList(*changed)
	case *sha != "":
		changedFiles, err = gitChangedFiles(*repoDir, *base, *sha)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("one of --changed or --sha is required")
	}

	// The connection is opened before the plan is built, because --base-sha feeds
	// the plan rather than annotating it afterwards.
	needsDB := *viaCypher || *crossCheck || !*noReuse || *baseSha != ""
	ctx := context.Background()
	var driver neo4j.DriverWithContext
	if needsDB {
		driver, err = connect(ctx)
		if err != nil {
			return err
		}
		defer driver.Close(ctx)
	}

	if *baseSha != "" {
		bg, ok, err := BaseGraphAtCommit(ctx, driver, g.Repo, *baseSha)
		if err != nil {
			return fmt.Errorf("--base-sha: %w", err)
		}
		if !ok {
			// Silence here would be indistinguishable from "nothing was deleted",
			// which is the failure this flag exists to prevent.
			return fmt.Errorf("no file index recorded for commit %s\n"+
				"  run `monograph load --sha %s` at that commit, or pass --base-in with a graph extracted there",
				*baseSha, *baseSha)
		}
		baseGraph = bg
	}

	plan := BuildPlanWithBase(g, baseGraph, *sha, changedFiles)

	// Refuse to emit a plan built on paths nothing owns. Selecting everything
	// (or nothing) because of a typo or a wrong path prefix is the failure mode
	// this tool exists to prevent.
	if unresolved := UnresolvedPaths(plan.Resolutions); len(unresolved) > 0 {
		msg := fmt.Sprintf("these changed paths match no target: %s\n"+
			"  paths must be relative to the monorepo root (%s), not the repository root",
			strings.Join(unresolved, ", "), *repoDir)
		if !*allowUnknown {
			return fmt.Errorf("%s\n  pass --allow-unknown-paths to continue anyway", msg)
		}
		fmt.Fprintln(os.Stderr, "warning:", msg)
	}

	// A target resolved from base history that HEAD no longer has is only
	// plannable if we also know that commit's edges, and --base-sha versions the
	// file index only. Without them the walk reaches none of the surviving
	// consumers and the plan comes back EMPTY — a deleted package whose
	// dependents are broken, reported as nothing to do. Refusing is the same
	// call made everywhere else here: a loud failure beats a narrow-looking plan
	// that tested nothing.
	if vanished := VanishedTargets(g, plan); len(vanished) > 0 && (baseGraph == nil || len(baseGraph.Edges) == 0) {
		msg := fmt.Sprintf("these targets were deleted and their dependents cannot be determined: %s\n"+
			"  --base-sha versions the file index but not the dependency edges\n"+
			"  pass --base-in with a graph extracted at the base commit instead",
			strings.Join(vanished, ", "))
		if !*allowUnknown {
			return fmt.Errorf("%s\n  or --allow-unknown-paths to continue with an incomplete selection", msg)
		}
		fmt.Fprintln(os.Stderr, "warning:", msg)
	}

	if needsDB {
		d := driver

		if *viaCypher || *crossCheck {
			names, err := AffectedViaCypher(ctx, d, g.Repo, changedFiles)
			if err != nil {
				return err
			}
			inMemory := affectedNames(plan)
			if *crossCheck && !equalStringSets(names, inMemory) {
				return fmt.Errorf("selection disagreement:\n  cypher:    %v\n  in-memory: %v", names, inMemory)
			}
			if *viaCypher {
				plan = planFromNames(g, *sha, changedFiles, names)
			}
		}

		if !*noReuse {
			if err := MarkReusable(ctx, d, &plan); err != nil {
				return err
			}
		}
	}

	return writeJSON(os.Stdout, plan)
}

func cmdRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	in := fs.String("in", "-", "run report JSON (- for stdin)")
	planIn := fs.String("plan", "", "the selection plan this run executed; records WHY each target was selected")
	shaFlag := fs.String("sha", "", "commit this run was for, when the report and plan do not carry one")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var report RunReport
	r, closeFn, err := openInput(*in)
	if err != nil {
		return err
	}
	defer closeFn()
	if err := decodeJSON(r, &report); err != nil {
		return err
	}
	if report.ID == "" {
		return fmt.Errorf("run report is missing \"id\"")
	}

	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		return err
	}
	defer d.Close(ctx)

	if *shaFlag != "" {
		report.SHA = *shaFlag
	}
	if err := RecordRun(ctx, d, report); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "recorded run %s with %d target results\n", report.ID, len(report.Results))

	// The selection is recorded separately and is optional, because the report
	// alone cannot answer "why did this target run?" — that reason lives in the
	// plan, which until now was an ephemeral file thrown away after the run.
	if *planIn != "" {
		plan, err := readPlan(*planIn)
		if err != nil {
			return fmt.Errorf("--plan: %w", err)
		}
		if *shaFlag != "" {
			plan.SHA = *shaFlag
		}
		n, err := RecordSelection(ctx, d, report.ID, plan)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "recorded selection for %s: %d targets, %d resolved paths\n",
			report.ID, n, len(plan.Resolutions))
	}
	return nil
}

// cmdEvidence emits the JFrog Evidence predicate for a run that has already been
// recorded, or the `jf evd create` command that would upload it.
//
// Two things it deliberately does not do:
//
// It does not build the predicate from the plan and report files. Those are the
// files the tool itself just wrote, so a predicate assembled from them is only as
// good as the tool — which is the trust question Evidence exists to answer. It
// reads the graph instead, which is what queries/coverage.cypher and the README
// already committed to: a serialisation of the recorded facts, "not a separately
// assembled claim".
//
// And it does not run `jf`. See EvidenceCommand.
func cmdEvidence(args []string) error {
	fs := flag.NewFlagSet("evidence", flag.ExitOnError)
	runID := fs.String("run-id", "", "the recorded CI run to attest")
	command := fs.Bool("command", false, "print the `jf evd create` invocation instead of the predicate")
	predicateFile := fs.String("predicate-file", "evidence.json", "path the printed command reads the predicate from")
	repoPath := fs.String("subject-repo-path", "", "Artifactory repo path of the subject artifact")
	sha256 := fs.String("subject-sha256", "", "sha256 digest of the subject artifact")
	key := fs.String("key", "", "signing key, as jf evd create takes it")
	keyAlias := fs.String("key-alias", "", "signing key alias")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runID == "" {
		return fmt.Errorf("--run-id is required: evidence attests one recorded run")
	}

	// --command needs no database. Keeping it that way is what lets a caller show
	// the command beside the predicate without opening a second connection, and
	// lets anyone read the command without credentials at all.
	if *command {
		fmt.Println(EvidenceCommand(EvidenceSubject{
			PredicateFile: *predicateFile,
			RepoPath:      *repoPath,
			SHA256:        *sha256,
			Key:           *key,
			KeyAlias:      *keyAlias,
		}))
		fmt.Fprintln(os.Stderr,
			"monograph does not run this: the subject must be an artifact in Artifactory, "+
				"and signing needs a key. See docs/adr-003-jfrog-integration.md.")
		return nil
	}

	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		return err
	}
	defer d.Close(ctx)

	ev, err := EvidenceFromGraph(ctx, d, *runID)
	if err != nil {
		return err
	}
	if err := writeJSON(os.Stdout, ev); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "attested %s: affected %d, executed %d, skipped %d (%d proven)\n",
		ev.RunID, len(ev.Affected), len(ev.Executed), len(ev.Skipped), ev.ProvenSkips())
	// Loud, because this is the predicate reporting its own violation. A gate
	// reading coverageGaps will reject it; someone reading the tool's output
	// should learn that here rather than from a failed promotion.
	if !ev.Covered() {
		fmt.Fprintf(os.Stderr,
			"warning: %d selected target(s) neither ran nor have a passing run to cite: %s\n"+
				"  the predicate reports this under coverageGaps rather than hiding it\n",
			len(ev.CoverageGaps), strings.Join(ev.CoverageGaps, ", "))
	}
	return nil
}

// gitChangedFiles lists files changed between base and sha, scoped to repoDir
// and returned relative to it.
func gitChangedFiles(repoDir, base, sha string) ([]string, error) {
	// --relative is essential: without it git prints paths from the repository
	// root ("monorepo/libs/core/x.ts"), which match nothing in a graph whose
	// paths are relative to the monorepo. That silently resolved every path to
	// the workspace catch-all and turned every --sha run into a full rebuild.
	// --no-renames is essential for the same class of reason as --relative.
	// Git's rename detection prints ONLY the destination path for a rename, so
	// moving a file from one target to another silently dropped the SOURCE
	// target from the changed set: its content hash moved, the plan said not to
	// run it, and a later run would find that hash unrecorded-but-unselected.
	// Broken code, untested, exit 0. Both paths are needed, and --no-renames
	// reports the delete/add pair instead of an R100.
	//
	// -z NUL-terminates the paths and disables git's path munging. Without it,
	// a filename containing a comma was shredded by the comma-separated split
	// below (each fragment then resolving to whatever it happened to prefix),
	// and a non-ASCII filename arrived C-quoted as "\303\251..." which matched
	// nothing and failed the whole selection over a single file.
	cmd := exec.Command("git", "diff", "--relative", "--no-renames", "-z",
		"--name-only", base, sha, "--", ".")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s..%s in %s: %w", base, sha, repoDir, err)
	}
	// Split on NUL alone. Comma and newline are both legal in a filename, so
	// splitList must not be used on git output.
	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			files = append(files, p)
		}
	}
	return files, nil
}

func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func affectedNames(p Plan) []string {
	out := make([]string, 0, len(p.Targets))
	for _, t := range p.Targets {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

func planFromNames(g *Graph, sha string, changedFiles, names []string) Plan {
	byName := g.TargetByName()
	plan := Plan{
		Repo:           g.Repo,
		SHA:            sha,
		ChangedFiles:   normalisePaths(changedFiles),
		ChangedTargets: ChangedTargets(g, changedFiles),
	}
	for _, n := range names {
		t := byName[n]
		plan.Targets = append(plan.Targets, PlanTarget{
			Name:       t.Name,
			Kind:       t.Kind,
			Image:      t.Image,
			TestCmd:    t.TestCmd,
			TargetHash: t.TargetHash,
			Runnable:   t.Runnable(),
		})
	}
	return plan
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string{}, a...)
	bs := append([]string{}, b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// cmdQueries prints the Cypher this tool executes, for a reader who wants to
// check the claim rather than take it -- and for bench/demotui, which shows the
// query on screen beside the stage that runs it. Printing the same constants the
// driver is handed is the whole point: a second, hand-copied copy would drift and
// nothing would notice.
func cmdQueries(args []string) error {
	fs := flag.NewFlagSet("queries", flag.ExitOnError)
	stage := fs.String("stage", "", "only queries for this pipeline stage (select|record|evidence)")
	asJSON := fs.Bool("json", false, "emit JSON, for programmatic consumers")
	if err := fs.Parse(args); err != nil {
		return err
	}

	all := Queries()
	out := make([]CypherQuery, 0, len(all))
	for _, q := range all {
		if *stage == "" || q.Stage == *stage {
			out = append(out, q)
		}
	}
	if len(out) == 0 {
		return fmt.Errorf("no queries for stage %q; known stages are select, record and evidence", *stage)
	}

	if *asJSON {
		return writeJSON(os.Stdout, out)
	}
	for i, q := range out {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("# %s (%s %s)\n# %s\n# runs: %s\n%s\n",
			q.Name, q.Stage, q.Kind, q.Title, q.When, strings.TrimSpace(q.Cypher))
	}
	return nil
}
