package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// env is everything a work step needs to know about where it is running.
// Bundled once at startup rather than threaded field by field.
type env struct {
	root         string // real repository root; never written to
	work         string // throwaway scratch dir (holds the clone plus graph.json/plan.json/report.json)
	repo         string // work/repo -- the throwaway clone; every edit happens here
	monographBin string
	daggerBin    string
	nonce        string
	fresh        bool
	cold         bool
	noPause      bool
	web          bool          // pass dagger's own -w so it opens each run's trace in a browser
	stageDwell   time.Duration // minimum time a step stays on screen; see atLeast

	// traceCh carries a run's Dagger Cloud URL the instant dagger prints it,
	// rather than when the run finishes. See traceWatcher.
	traceCh     chan string
	neo4jTarget string // "local container (...)" or "remote (...)", set by resolveNeo4jEnv
}

// resetTrace discards any trace URL an earlier run published but nobody consumed
// yet, so a new run cannot be handed the previous run's link.
//
// The listener is normally parked on this channel and takes each value the
// instant it appears, which leaves only a narrow window -- between the listener
// consuming a value and the model re-arming it -- where a late duplicate could
// still be sitting in the buffer. Draining costs nothing and closes it, rather
// than leaving "w" able to open the wrong trace for a fraction of a second at
// the one moment a presenter is most likely to press it.
func (e *env) resetTrace() {
	if e == nil {
		return
	}
	for {
		select {
		case <-e.traceCh:
		default:
			return
		}
	}
}

func (e *env) graphJSON() string  { return filepath.Join(e.work, "graph.json") }
func (e *env) planJSON() string   { return filepath.Join(e.work, "plan.json") }
func (e *env) reportJSON() string { return filepath.Join(e.work, "report.json") }
func (e *env) naiveJSON() string  { return filepath.Join(e.work, "naive.json") }

// evidenceJSON is where the predicate lands. The printed `jf` command names the
// BASENAME rather than this path: the work dir is a temp directory whose name
// wraps across two lines on a projector, and the command is illustrative anyway
// (its subject is a placeholder). The transcript prints this path underneath, so
// nothing about where the file actually is gets hidden.
func (e *env) evidenceJSON() string { return filepath.Join(e.work, "evidence.json") }

// run executes name with args in dir, returning combined stdout (stderr is
// captured separately so callers can decide whether to surface it).
func run(dir, name string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func mustRun(dir, name string, args ...string) (string, error) {
	out, errOut, err := run(dir, name, args...)
	if err != nil {
		if errOut != "" {
			return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errOut))
		}
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// resolveNeo4jEnv reproduces graph/neo4j-env.sh's precedence (real environment
// > .env at repo root > local container default) by literally sourcing it and
// re-exporting the result into this process. Re-implementing that resolution
// logic in Go would be a second copy of a rule that already has one home.
func resolveNeo4jEnv(root string) (target string, warning string, err error) {
	script := fmt.Sprintf(`cd %q && . ./graph/neo4j-env.sh && printf '%%s\n' "$NEO4J_URI" "$NEO4J_USERNAME" "$NEO4J_PASSWORD" "$NEO4J_DATABASE" "$(describe_target)"`, root)
	cmd := exec.Command("bash", "-c", script)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if runErr := cmd.Run(); runErr != nil {
		return "", "", fmt.Errorf("sourcing graph/neo4j-env.sh: %w: %s", runErr, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 5 {
		return "", "", fmt.Errorf("graph/neo4j-env.sh produced unexpected output: %q", out.String())
	}
	os.Setenv("NEO4J_URI", lines[0])
	os.Setenv("NEO4J_USERNAME", lines[1])
	os.Setenv("NEO4J_PASSWORD", lines[2])
	os.Setenv("NEO4J_DATABASE", lines[3])
	return lines[4], strings.TrimSpace(errBuf.String()), nil
}

// daggerArgs prefixes --progress plain onto every dagger invocation.
//
// It keeps dagger from rendering its own TUI over the top of our alt-screen,
// and it guarantees the verbose progress stream -- which carries the
// `cloud url=…` line the trace link is read from -- lands in the pipe we
// capture rather than being summarised away.
//
// web adds dagger's own -w, which makes dagger open the trace in a browser
// itself. That is the simplest possible version of "show me the trace": no
// scraping, no URL handling of ours involved at all.
func daggerArgs(web bool, args ...string) []string {
	out := []string{"--progress", "plain"}
	if web {
		out = append(out, "--web")
	}
	return append(out, args...)
}

// daggerTraceURLRe matches the trace link by its SHAPE rather than by any
// surrounding prose. Dagger emits the same URL in at least two places and only
// one of them is the "Full trace at …" footer:
//
//	1   : [0.0s] | cloud url=https://dagger.cloud/<org>/traces/<id>   <- line 2
//	Full trace at https://dagger.cloud/<org>/traces/<id>              <- last line
//
// Anchoring on "Full trace at" was a real bug: that footer is written at exit
// and does not reliably reach a captured pipe (a run that produced 35KB of
// progress on stderr ended mid-stream with no footer at all), whereas the
// early `cloud url=` line is part of the ordinary progress stream and is
// always there. Matching either one makes this work regardless.
var daggerTraceURLRe = regexp.MustCompile(`https://dagger\.cloud/[^\s/]+/traces/[0-9a-f]{8,}`)

// daggerTraceURL finds the Dagger Cloud trace link in any of the given
// streams. ANSI styling is stripped first, so a colourised URL still matches.
// An empty return means Dagger Cloud is not configured for this CLI, not that
// anything failed.
func daggerTraceURL(streams ...string) string {
	for _, s := range streams {
		if m := daggerTraceURLRe.FindString(stripANSI(s)); m != "" {
			return m
		}
	}
	return ""
}

// openURL spawns the OS's "open a URL" helper -- the same mechanism `gh pr
// view --web` and `open`/`xdg-open` themselves use. It returns as soon as
// that helper has been launched, not once a browser tab is showing.
func openURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// cloneWorkspace clones root into a throwaway repo, exactly as bench/demo.sh
// does, and removes the origin remote so Dagger never tries (and fails) to
// resolve VCS metadata from a filesystem path.
func cloneWorkspace(e *env) error {
	if _, err := mustRun("", "git", "clone", "--quiet", e.root, e.repo); err != nil {
		return err
	}
	if _, err := mustRun(e.repo, "git", "config", "user.email", "demo@example.com"); err != nil {
		return err
	}
	if _, err := mustRun(e.repo, "git", "config", "user.name", "CI Demo"); err != nil {
		return err
	}

	// Every edit phase branches off `main` by name. A plain `git clone` only
	// checks out a local branch matching e.root's CURRENT branch -- if that
	// happens to be anything other than main (e.g. this demo is being run from
	// a feature branch), the clone has no local `main` ref at all, and
	// `git checkout -b pr-docs main` fails with "not a commit". `origin/main`
	// is still there (clone fetches every branch's objects and sets up
	// remote-tracking refs for all of them, not just HEAD's), so create the
	// local branch from that before removing the remote makes it unreachable.
	if _, _, err := run(e.repo, "git", "rev-parse", "--verify", "--quiet", "main"); err != nil {
		if _, err := mustRun(e.repo, "git", "branch", "main", "origin/main"); err != nil {
			return fmt.Errorf("creating local main from origin/main: %w", err)
		}
	}

	if _, err := mustRun(e.repo, "git", "remote", "remove", "origin"); err != nil {
		return err
	}
	return nil
}

func gitShortHEAD(repo string) (string, error) {
	out, err := mustRun(repo, "git", "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(out), err
}

func gitFullHEAD(repo string) (string, error) {
	out, err := mustRun(repo, "git", "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

func gitCheckoutNewBranch(repo, branch, from string) error {
	_, err := mustRun(repo, "git", "checkout", "-q", "-b", branch, from)
	return err
}

func gitCommitAll(repo, message string) error {
	_, err := mustRun(repo, "git", "commit", "-qam", message)
	return err
}

// changedFiles lists paths changed on this branch vs main, relative to
// monorepo/ -- exactly what .github/workflows/ci.yml computes.
func changedFiles(repo string) (csv string, list []string, err error) {
	out, err := mustRun(repo, "git", "diff", "--name-only", "main...HEAD", "--", "monorepo")
	if err != nil {
		return "", nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		list = append(list, strings.TrimPrefix(line, "monorepo/"))
	}
	return strings.Join(list, ","), list, nil
}

var diffHeaderNoise = regexp.MustCompile(`diff --git a/|index [0-9a-f]{6,40}\.\.[0-9a-f]{6,40}|--- a/|\+\+\+ b/`)

// hunkHeaderTail matches whatever git appends AFTER the closing @@ of a hunk
// header -- the enclosing function or, in a markdown file, whichever line of
// prose git guessed was the nearest heading. It is never about the edit, and
// on screen it was the longest line in the whole demo.
var hunkHeaderTail = regexp.MustCompile(`^((?:\x1b\[[0-9;]*m)*@@ [^@]*@@).*$`)

// trimHunkHeader cuts a hunk header down to its @@ … @@ marker.
//
// The appended \x1b[0m is not optional: git colours the whole header line and
// puts its reset at the END, so dropping the tail takes the reset with it and
// leaves cyan bleeding down the rest of the transcript.
func trimHunkHeader(line string) string {
	if !strings.Contains(stripANSI(line), "@@") {
		return line
	}
	return hunkHeaderTail.ReplaceAllString(line, "${1}\x1b[0m")
}

// coloredDiff is the actual red/green edit for one path, against main, with
// file-header noise stripped -- see the sibling helper in bench/demo.sh for
// why the @@ hunk marker survives that filter and the header lines do not.
//
// It has since diverged from that sibling, deliberately: demo.sh still uses
// -U1 and keeps the whole hunk header, while this takes -U0 and trims the
// header to its marker. One line of context is one more line of unrelated file
// on a slide, and every edit this demo makes is an append, so neither the
// context line nor git's guess at the enclosing heading is ever the thing
// being pointed at.
func coloredDiff(repo string, paths ...string) (string, error) {
	args := append([]string{"-c", "color.diff=always", "diff", "-U0", "main...HEAD", "--"}, paths...)
	out, _, err := run(repo, "git", args...)
	if err != nil {
		return "", err
	}
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if diffHeaderNoise.MatchString(line) {
			continue
		}
		kept = append(kept, trimHunkHeader(line))
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n"), nil
}

func appendToFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// pruneIfCold mirrors bench/demo.sh's prune_if_cold: --fresh destroys the
// engine container outright (Dagger recreates it with an empty cache AND an
// empty image store), --cold only prunes Dagger's own cache and leaves images
// warm. Both are best-effort, matching the original's `|| true`.
func pruneIfCold(e *env) []string {
	var notes []string
	if e.fresh {
		out, _, _ := run("", "docker", "ps", "-a", "--filter", "name=dagger-engine", "--format", "{{.Names}}")
		name := strings.TrimSpace(strings.Split(out, "\n")[0])
		if name != "" {
			notes = append(notes, fmt.Sprintf("destroying engine container %s (real fresh runner; images re-pull)", name))
			_, _, _ = run("", "docker", "rm", "-f", name)
			for i := 0; i < 20; i++ {
				if _, _, err := run("", "docker", "inspect", name); err != nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		return notes
	}
	if !e.cold {
		return nil
	}
	notes = append(notes, "pruning the engine cache (images stay warm)")
	cmd := exec.Command(e.daggerBin, "-M", "query")
	cmd.Stdin = strings.NewReader("{ engine { localCache { prune } } }")
	_ = cmd.Run()
	return notes
}

func monographExtract(e *env) error {
	cmd := exec.Command(e.monographBin, "extract", "--repo=monorepo")
	cmd.Dir = e.repo
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("monograph extract: %w", err)
	}
	return os.WriteFile(e.graphJSON(), out, 0o644)
}

// monographAffected runs `monograph affected` and writes its plan to
// e.planJSON(), returning the decoded Plan for immediate rendering.
func monographAffected(e *env, changedCSV string) (Plan, error) {
	cmd := exec.Command(e.monographBin, "affected", "--in", e.graphJSON(), "--changed="+changedCSV)
	cmd.Dir = e.repo
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return Plan{}, fmt.Errorf("monograph affected: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	if err := os.WriteFile(e.planJSON(), out.Bytes(), 0o644); err != nil {
		return Plan{}, err
	}
	var p Plan
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		return Plan{}, fmt.Errorf("decoding plan: %w", err)
	}
	return p, nil
}

// reuseRunID is the id of beat 4's selection-only run -- the one the evidence
// step attests. Shared by the recorder and the attester so the two cannot name
// different runs, which would produce a predicate about a run nobody watched.
func reuseRunID(e *env) string { return "demo-reuse-" + e.nonce }

// monographEvidence reads a recorded run back out of the graph as a JFrog
// Evidence predicate, and asks the tool for the `jf evd create` command that
// would upload it.
//
// Two invocations of the real binary, not one call plus a string built here. The
// command in particular has to come from the tool for the same reason the Cypher
// panel reads `monograph queries`: this text goes on a screen in front of an
// audience, and a copy kept in the demo is a copy that can quietly stop matching
// what CI would actually run.
//
// The predicate is written to disk as well as returned, because the printed
// command names a file and pointing at one that does not exist would make the
// whole step a mock-up.
func monographEvidence(e *env, runID string) (predicate, command string, err error) {
	out, errOut, err := run(e.repo, e.monographBin, "evidence", "--run-id", runID)
	if err != nil {
		return "", "", fmt.Errorf("monograph evidence: %w: %s", err, strings.TrimSpace(errOut))
	}
	if err := os.WriteFile(e.evidenceJSON(), []byte(out), 0o644); err != nil {
		return "", "", err
	}

	cmdOut, cmdErr, err := run(e.repo, e.monographBin, "evidence",
		"--run-id", runID, "--command", "--predicate-file", "evidence.json")
	if err != nil {
		return "", "", fmt.Errorf("monograph evidence --command: %w: %s", err, strings.TrimSpace(cmdErr))
	}
	return strings.TrimSpace(out), strings.TrimSpace(cmdOut), nil
}

func monographRecord(e *env, sha string) (string, error) {
	out, errOut, err := run(e.repo, e.monographBin, "record",
		"--in", e.reportJSON(), "--plan", e.planJSON(), "--sha", sha)
	combined := strings.TrimSpace(out + errOut)
	if err != nil {
		return combined, fmt.Errorf("monograph record: %w: %s", err, combined)
	}
	return combined, nil
}

// monographRecordReuse records beat 4's selection -- the one where every target
// was skipped because its content already had a PASSED run.
//
// Without this the demo proved reuse on screen and left no trace of it in the
// graph. `RecordSelection` writes (CIRun)-[:PROVEN_BY {targetHash}]->(TargetRun),
// which its own doc comment calls the important one, because it "turns 'we
// skipped it because it was already green' from an assertion by the tool into a
// fact with a citation" -- and the demo, having never recorded beat 4, produced
// zero of those edges. Anyone who opened Neo4j after a demo and asked it to show
// the reuse got an empty result for the demo's central claim.
//
// It goes in as a report with NO results, which is what a selection where
// nothing executed honestly is. `record` needs the CIRun to exist before it will
// attach a selection to it, and creating that run is exactly what an empty
// report does; trigger and orchestrator say plainly that nothing ran, so this
// row can never be mistaken for a 30th build.
func monographRecordReuse(e *env, sha string) (string, error) {
	// Its own type rather than the RunReport in types.go: that one is the DECODE
	// model, carrying only the fields this UI renders from what Dagger printed.
	// This is the encode side of a different contract (monograph's), and the two
	// having no fields in common beyond the results list is the reason not to
	// widen one struct to serve both directions.
	report := struct {
		ID           string   `json:"id"`
		Repo         string   `json:"repo"`
		SHA          string   `json:"sha"`
		Trigger      string   `json:"trigger"`
		Orchestrator string   `json:"orchestrator"`
		Results      []string `json:"results"`
	}{
		ID:           reuseRunID(e),
		Repo:         "monorepo",
		SHA:          sha,
		Trigger:      "selection-only",
		Orchestrator: "monograph",
		Results:      []string{},
	}
	blob, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	path := filepath.Join(e.work, "reuse-report.json")
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		return "", err
	}
	out, errOut, err := run(e.repo, e.monographBin, "record",
		"--in", path, "--plan", e.planJSON(), "--sha", sha)
	combined := strings.TrimSpace(out + errOut)
	if err != nil {
		return combined, fmt.Errorf("monograph record (reuse): %w: %s", err, combined)
	}
	return combined, nil
}

// jsonLine extracts the single `{...}` line a Dang function prints among its
// other (human-readable) stdout -- the same `grep -E '^\{'` bench/demo.sh
// relies on.
func jsonLine(stdout string) (string, bool) {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "{") {
			return line, true
		}
	}
	return "", false
}

// runDagger runs a Dang function that ends by printing one JSON report line,
// writes that line to outPath (monograph record's --in expects a file, not the
// in-memory struct this also returns), and reports wall time. Used for both
// the naive `straight` call and the graph-selected `run` call.
//
// The returned traceURL is Dagger's own "Full trace at https://dagger.cloud/…"
// line, which it prints to stderr -- .Output() alone would have discarded
// this, since it only ever wires up stdout. traceURL is "" whenever Dagger
// Cloud isn't configured for the CLI making the call; that is not an error.
// daggerRun is one dagger invocation's result. A struct rather than a long
// return list mainly so stderr can ride along for diagnostics without every
// caller having to thread it.
type daggerRun struct {
	report   RunReport
	elapsed  time.Duration
	traceURL string
	stderr   string // raw, exactly as dagger wrote it
	stdout   string // raw, likewise
}

func runDagger(e *env, outPath string, args ...string) (daggerRun, error) {
	start := time.Now()
	cmd := exec.Command(e.daggerBin, daggerArgs(e.web, args...)...)
	cmd.Dir = e.repo
	var out, errBuf bytes.Buffer
	// Watched rather than plain buffers: the trace URL is published the moment it
	// appears in the stream, so "w" works while the run is still going.
	cmd.Stdout = &traceWatcher{buf: &out, ch: e.traceCh}
	cmd.Stderr = &traceWatcher{buf: &errBuf, ch: e.traceCh}
	err := cmd.Run()

	r := daggerRun{
		elapsed: time.Since(start),
		stderr:  errBuf.String(),
		stdout:  out.String(),
	}
	r.traceURL = daggerTraceURL(r.stderr, r.stdout)

	if err != nil {
		return r, fmt.Errorf("dagger %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(r.stderr))
	}
	line, ok := jsonLine(r.stdout)
	if !ok {
		return r, fmt.Errorf("dagger %s: no JSON report line in output", strings.Join(args, " "))
	}
	if err := os.WriteFile(outPath, []byte(line), 0o644); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(line), &r.report); err != nil {
		return r, fmt.Errorf("decoding run report: %w", err)
	}
	return r, nil
}

// diagLine reports what a dagger call actually produced, so a missing trace
// URL can be told apart from a trace URL we failed to parse -- a distinction
// that cost a full debugging round when this printed nothing at all.
func (r daggerRun) diagLine() string {
	tail := stripANSI(r.stderr)
	if n := len(tail); n > 220 {
		tail = tail[n-220:]
	}
	tail = strings.ReplaceAll(strings.TrimSpace(tail), "\n", " ⏎ ")
	return fmt.Sprintf("[diag] traceURL=%q stderr=%dB stdout=%dB tail=%q",
		r.traceURL, len(r.stderr), len(r.stdout), tail)
}

// traceDiagLine is diagLine without the stderr tail -- the version that goes
// on screen mid-demo, where a 220-char blob of escaped log would be worse than
// the ambiguity it resolves. The byte counts still separate "dagger emitted
// nothing" from "dagger emitted plenty and we matched none of it".
func (r daggerRun) traceDiagLine() string {
	return fmt.Sprintf("cloud not configured? (stderr %s, stdout %s)",
		byteCount(len(r.stderr)), byteCount(len(r.stdout)))
}

func byteCount(n int) string {
	if n >= 1024 {
		return fmt.Sprintf("%dKB", n/1024)
	}
	return fmt.Sprintf("%dB", n)
}

var ansiEscapeRe = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

func stripANSI(s string) string { return ansiEscapeRe.ReplaceAllString(s, "") }

// daggerNaiveSelected lists the hardcoded target set a Dagger-only setup
// maintains by hand, for beat 1's "it never consults the change" moment.
func daggerNaiveSelected(e *env) ([]string, error) {
	// Never --web here: this is a metadata lookup, not one of the three runs
	// the demo is actually about, and it would open a browser tab before the
	// first beat had executed anything.
	cmd := exec.Command(e.daggerBin, daggerArgs(false, "call", "orchestrator-dang", "straight-selected")...)
	cmd.Dir = e.repo
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dagger call orchestrator-dang straight-selected: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	var names []string
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// renderResults formats a run report exactly as bench/demo.sh's inline python
// did: one line per target sorted by name, then a summary line with wall time.
func renderResults(r RunReport, elapsed time.Duration) []string {
	sorted := append([]TargetResult{}, r.Results...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Target < sorted[j].Target })
	var lines []string
	for _, t := range sorted {
		dur := "null"
		if t.DurationMs != nil {
			dur = fmt.Sprintf("%dms", *t.DurationMs)
		}
		style := successStyle
		if t.Status != "PASSED" {
			style = failStyle
		}
		lines = append(lines, fmt.Sprintf("    %-18s %s", t.Target, style.Render(fmt.Sprintf("%-7s %s", t.Status, dur))))
	}
	lines = append(lines, fmt.Sprintf("    %d target(s) executed, %ds wall", len(sorted), int(elapsed.Seconds())))
	return lines
}

// renderSelection formats a Plan exactly as bench/demo.sh's show_selection did.
func renderSelection(p Plan) []string {
	var lines []string
	for _, r := range p.Resolutions {
		lines = append(lines, fmt.Sprintf("    %s  ->  %s  ->  owned by %s", r.Path, r.How, strings.Join(r.Targets, ",")))
	}
	runnable := p.Runnable()
	label := strings.Join(runnable, ",")
	if label == "" {
		label = "(nothing)"
	}
	lines = append(lines, fmt.Sprintf("    graph selects %d target(s) to run: %s", len(runnable), label))
	if reused := p.Reused(); len(reused) > 0 {
		lines = append(lines, fmt.Sprintf("    already passed on this exact content, skipped: %s", strings.Join(reused, ",")))
	}
	if len(p.Codegen) > 0 && len(runnable) > 0 {
		names := make([]string, len(p.Codegen))
		for i, c := range p.Codegen {
			names[i] = c.Name
		}
		lines = append(lines, fmt.Sprintf("    codegen first: %s", strings.Join(names, ",")))
	}
	return lines
}

// monographQueries asks the real tool for the Cypher it runs, for the overlay.
//
// Shelling out rather than embedding the text: these strings are constants in
// tools/monograph, and a copy living here would go stale the first time a query
// changed, leaving the demo asserting on a projector that the tool runs
// something it no longer runs.
func monographQueries(e *env) ([]CypherQuery, error) {
	out, errOut, err := run(e.root, e.monographBin, "queries", "--json")
	if err != nil {
		return nil, fmt.Errorf("monograph queries: %w: %s", err, strings.TrimSpace(errOut))
	}
	var qs []CypherQuery
	if err := json.Unmarshal([]byte(out), &qs); err != nil {
		return nil, fmt.Errorf("decoding monograph queries: %w", err)
	}
	return qs, nil
}

// traceWatcher tees a stream into buf and, until it finds one, watches the bytes
// going past for dagger's Cloud trace URL -- publishing the first match to ch
// immediately instead of at the end of the run.
//
// Waiting for the run to finish was the whole problem: a Cmd returns one message
// when it completes, so a twenty-second Dagger call meant twenty seconds of "w"
// doing nothing, which is exactly the window where a presenter wants the trace
// open in a browser tab. Dagger prints `cloud url=…` in the first lines of its
// progress stream, so the link is available almost immediately.
//
// Two properties this must not violate, both about not interfering with the run:
// the send never blocks (a full or nil channel drops the notification rather than
// stalling dagger's output), and scanning stops after the first hit, so the
// remaining tens of kilobytes of progress stream cost one boolean check each.
type traceWatcher struct {
	buf   *bytes.Buffer
	ch    chan<- string
	found bool
}

func (w *traceWatcher) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if !w.found {
		if url := daggerTraceURL(w.buf.String()); url != "" {
			w.found = true
			select {
			case w.ch <- url:
			default: // nil channel, or nobody listening: never hold up the run
			}
		}
	}
	return n, err
}
