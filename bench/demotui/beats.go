package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// stepMsg is the single result type every phase's work produces. Only the
// fields relevant to the phase that sent it are populated; Update dispatches on
// m.phase (exactly one phase's Cmd is ever in flight at a time) to know which
// ones to read.
type stepMsg struct {
	err        error
	lines      []string
	sha        string
	changedCSV string
	plan       *Plan
	report     *RunReport
	elapsed    time.Duration
	names      []string
	traceURL   string // this step's Dagger Cloud trace link, if one was printed

	// The Cypher the overlay shows, read once from the real tool during the
	// intro step. queriesErr rides along so a failure surfaces IN the panel
	// rather than taking down a demo over a feature nobody may press.
	queries    []CypherQuery
	queriesErr error

	// The JFrog Evidence predicate and the jf command that would upload it, both
	// as the tool emitted them. Shown in full by the "e" panel.
	evidence    string
	evidenceCmd string
}

func boldLine(s string) string { return titleStyle.Bold(true).Render("==> " + s) }
func beatLine(s string) string { return beatStyle.Render(s) }
func note(s string) string     { return noteStyle.Render("    " + s) }
func notes(ss ...string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = note(s)
	}
	return out
}

// linkLine renders a label and a URL, styled so it reads as a link -- most
// terminals auto-detect a bare URL and make it clickable on their own, which
// is safer than emitting an OSC 8 hyperlink escape a viewer might not support.
func linkLine(label, url string) string {
	return noteStyle.Render("    "+label+" ") + linkStyle.Render(url)
}

// traceLines shows the run's Dagger Cloud trace link, or -- when there wasn't
// one -- says so out loud, with the byte counts dagger actually produced.
//
// The loud "no trace" branch stays deliberately. Silence was the original sin
// here: a missing link was indistinguishable from a link we had failed to
// parse, so a parsing bug looked exactly like "Cloud isn't configured" and
// survived a round of fixes without being localised. What it prints is the
// SHORT diagnostic (counts, no stderr tail) -- enough to tell "dagger emitted
// nothing" from "dagger emitted 35KB and we matched none of it", which is the
// distinction that mattered, without pasting a wall of escaped log into the
// middle of a live demo. The full tail is still on the error path.
func traceLines(r daggerRun) []string {
	if r.traceURL != "" {
		return []string{linkLine("trace:", r.traceURL)}
	}
	return []string{dimStyle.Render("    no trace URL -- " + r.traceDiagLine())}
}

// cmdIntro clones the real repo into a throwaway workspace and reports the
// clone's HEAD and which Neo4j this run is pointed at.
func cmdIntro(e *env) tea.Cmd {
	return func() tea.Msg {
		if err := cloneWorkspace(e); err != nil {
			return stepMsg{err: err}
		}
		sha, err := gitShortHEAD(e.repo)
		if err != nil {
			return stepMsg{err: err}
		}
		lines := []string{boldLine("throwaway clone -- the real repository is untouched")}
		lines = append(lines, note(fmt.Sprintf("HEAD %s   graph: %s", sha, e.neo4jTarget)))
		switch {
		case e.fresh:
			lines = append(lines, note("FRESH: engine destroyed before each run -- the quotable regime"))
		case e.cold:
			lines = append(lines, note("COLD: cache pruned, images warm -- the ratio is inflated"))
		default:
			lines = append(lines, note("WARM engine -- read the TARGET COUNTS, not the wall clock"))
		}
		qs, qErr := monographQueries(e)
		return stepMsg{lines: lines, sha: sha, queries: qs, queriesErr: qErr}
	}
}

// cmdDocsEdit is beat 1/2's setup: a one-line docs typo fix, committed for
// real, with its actual diff shown -- not asserted, not simulated.
//
// It opens with BEAT 1's heading, so the beat is announced BEFORE the edit it is
// about. The heading used to live two steps later, on the naive target list,
// which meant a demo showed an unexplained diff, then a target list, and only
// then said what the audience had been looking at.
func cmdDocsEdit(e *env) tea.Cmd {
	return func() tea.Msg {
		if err := gitCheckoutNewBranch(e.repo, "pr-docs", "main"); err != nil {
			return stepMsg{err: err}
		}
		path := e.repo + "/monorepo/docs/README.md"
		edit := fmt.Sprintf("\n<!-- demo %s: fixed a stray double  space -->\n", e.nonce)
		if err := appendToFile(path, edit); err != nil {
			return stepMsg{err: err}
		}
		if err := gitCommitAll(e.repo, "docs: fix a stray double space"); err != nil {
			return stepMsg{err: err}
		}
		sha, err := gitShortHEAD(e.repo)
		if err != nil {
			return stepMsg{err: err}
		}
		csv, _, err := changedFiles(e.repo)
		if err != nil {
			return stepMsg{err: err}
		}
		diff, err := coloredDiff(e.repo, "monorepo/docs/README.md")
		if err != nil {
			return stepMsg{err: err}
		}
		lines := []string{
			beatLine("BEAT 1 -- NAIVE CI: the docs typo rebuilds everything"),
			note(fmt.Sprintf("PR 1, commit %s   changed: %s", sha, csv)),
			"",
			diff,
		}
		return stepMsg{lines: lines, sha: sha, changedCSV: csv}
	}
}

// cmdBeat1List shows the hardcoded target list a Dagger-only setup maintains
// by hand -- the thing that never consults the change.
func cmdBeat1List(e *env) tea.Cmd {
	return func() tea.Msg {
		names, err := daggerNaiveSelected(e)
		if err != nil {
			return stepMsg{err: err}
		}
		// No heading here: BEAT 1's is printed by cmdDocsEdit, ahead of the edit
		// this list is the response to.
		lines := []string{note("hand-maintained target list, never consults the change:")}
		for _, n := range names {
			lines = append(lines, "      "+n)
		}
		return stepMsg{lines: lines, names: names}
	}
}

// cmdBeat1Run executes the naive, unfiltered target list.
func cmdBeat1Run(e *env) tea.Cmd {
	return func() tea.Msg {
		lines := notes(pruneIfCold(e)...)
		r, err := runDagger(e, e.naiveJSON(), "call", "orchestrator-dang", "straight", "--run-id=demo-naive-"+e.nonce)
		if err != nil {
			return stepMsg{err: err, lines: append(lines, dimStyle.Render("    "+r.diagLine()))}
		}
		lines = append(lines, renderResults(r.report, r.elapsed)...)
		lines = append(lines, traceLines(r)...)
		lines = append(lines, "", note(fmt.Sprintf("%d targets compiled and tested to check one markdown file.", len(r.report.Results))))
		return stepMsg{lines: lines, report: &r.report, elapsed: r.elapsed, traceURL: r.traceURL}
	}
}

// cmdBeat2Select extracts the edited tree and asks the graph what the docs
// change actually affects.
//
// It re-shows the commit and its diff under the heading, which looks like
// repetition and is not. Beat 2's heading re-anchors the viewport, so beat 1's
// diff scrolls away exactly when the audience is being told what the graph did
// WITH that change -- while beat 3 keeps its diff on screen for free, because
// its heading sits on the edit step and its select step adds no heading of its
// own. Re-printing here is what gives the two graph-selected beats the same
// shape: heading, the change, what the graph selected from it.
func cmdBeat2Select(e *env, sha, changedCSV string) tea.Cmd {
	return func() tea.Msg {
		lines := []string{beatLine("BEAT 2 -- THE SAME COMMIT, GRAPH-SELECTED")}
		if err := monographExtract(e); err != nil {
			return stepMsg{err: err}
		}
		// Read back off git rather than carried in state: it is the same working
		// tree and the same commit beat 1 showed, so this cannot drift from it.
		diff, err := coloredDiff(e.repo, "monorepo/docs/README.md")
		if err != nil {
			return stepMsg{err: err}
		}
		lines = append(lines,
			note(fmt.Sprintf("PR 1, commit %s   changed: %s", sha, changedCSV)),
			"", diff)

		plan, err := monographAffected(e, changedCSV)
		if err != nil {
			return stepMsg{err: err}
		}
		lines = append(lines, renderSelection(plan)...)
		lines = append(lines, "", note("Not a glob: the file resolved to its owning target, and nothing"), note("depends on docs -- so the walk stops there."))
		return stepMsg{lines: lines, plan: &plan}
	}
}

func cmdBeat2Run(e *env) tea.Cmd {
	return func() tea.Msg {
		lines := notes(pruneIfCold(e)...)
		r, err := runDaggerPlan(e, "docs")
		if err != nil {
			return stepMsg{err: err, lines: append(lines, dimStyle.Render("    "+r.diagLine()))}
		}
		lines = append(lines, renderResults(r.report, r.elapsed)...)
		lines = append(lines, traceLines(r)...)
		lines = append(lines, "", note("The other 8 were never reached: no pull, no upload, no cache lookup."))
		return stepMsg{lines: lines, report: &r.report, elapsed: r.elapsed, traceURL: r.traceURL}
	}
}

// cmdCoreEdit is beat 3/4's setup: a real change to a shared library, so the
// graph has something non-trivial to fan out across. It carries BEAT 3's
// heading, for the same reason cmdDocsEdit carries BEAT 1's.
func cmdCoreEdit(e *env) tea.Cmd {
	return func() tea.Msg {
		if err := gitCheckoutNewBranch(e.repo, "pr-core", "main"); err != nil {
			return stepMsg{err: err}
		}
		path := e.repo + "/monorepo/libs/core/src/index.ts"
		edit := fmt.Sprintf(`
/** demo %s */
export function isPrivileged(user: { role: string }): boolean {
  return user.role === "ROLE_ADMIN";
}
`, e.nonce)
		if err := appendToFile(path, edit); err != nil {
			return stepMsg{err: err}
		}
		if err := gitCommitAll(e.repo, "feat(core): add isPrivileged"); err != nil {
			return stepMsg{err: err}
		}
		sha, err := gitShortHEAD(e.repo)
		if err != nil {
			return stepMsg{err: err}
		}
		csv, _, err := changedFiles(e.repo)
		if err != nil {
			return stepMsg{err: err}
		}
		diff, err := coloredDiff(e.repo, "monorepo/libs/core/src/index.ts")
		if err != nil {
			return stepMsg{err: err}
		}
		lines := []string{
			beatLine("BEAT 3 -- A REAL CHANGE: scoped, not trivial"),
			note(fmt.Sprintf("PR 2, commit %s   changed: %s", sha, csv)),
			"",
			diff,
		}
		return stepMsg{lines: lines, sha: sha, changedCSV: csv}
	}
}

func cmdBeat3Select(e *env, changedCSV string) tea.Cmd {
	return func() tea.Msg {
		// Heading already printed by cmdCoreEdit, ahead of the edit.
		var lines []string
		if err := monographExtract(e); err != nil {
			return stepMsg{err: err}
		}
		plan, err := monographAffected(e, changedCSV)
		if err != nil {
			return stepMsg{err: err}
		}
		lines = append(lines, renderSelection(plan)...)
		lines = append(lines, "", note("apps/admin never imports libs/core -- it is reached via libs/ui."), note("A path filter on libs/core/** would silently never test it."))
		return stepMsg{lines: lines, plan: &plan}
	}
}

// cmdBeat3Run executes the graph-selected plan for the core change, then
// records the outcome AND the selection that justified it -- the step that
// makes beat 4's reuse claim checkable rather than asserted.
func cmdBeat3Run(e *env, sha string) tea.Cmd {
	return func() tea.Msg {
		lines := notes(pruneIfCold(e)...)
		r, err := runDaggerPlan(e, "core")
		if err != nil {
			return stepMsg{err: err, lines: append(lines, dimStyle.Render("    "+r.diagLine()))}
		}
		lines = append(lines, renderResults(r.report, r.elapsed)...)
		lines = append(lines, traceLines(r)...)
		return stepMsg{lines: lines, report: &r.report, elapsed: r.elapsed, traceURL: r.traceURL}
	}
}

// cmdBeat3Record writes the run and its selection into the graph.
//
// A step of its own, not a tail on cmdBeat3Run, because RECORD is a stage in
// the diagram and it was the one stage the audience never saw happen: folded
// into the run, it went from Pending straight to Done in the same frame the run
// finished, so the pipeline appeared to skip it. Now it lights up, holds, and
// waits for the presenter like every other stage.
func cmdBeat3Record(e *env, sha string) tea.Cmd {
	return func() tea.Msg {
		recordOut, err := monographRecord(e, sha)
		// monograph record reports the run AND the selection, on two separate
		// stderr lines; note() indents one string, so each line needs its own.
		// Dimmed: it is the bookkeeping beat 4's claim rests on, not something
		// to read out loud.
		var lines []string
		for _, l := range strings.Split(recordOut, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, dimStyle.Render("    "+l))
			}
		}
		if err != nil {
			return stepMsg{err: err, lines: lines}
		}
		// No closing note here, deliberately. The two dim lines above already say
		// what was written, and beat 3's block is length-critical: the viewport
		// anchors on the BEAT heading only while the whole beat still fits, so
		// every line this step adds is a line closer to scrolling the diff -- the
		// change the beat is ABOUT -- off the top. See syncViewport.
		return stepMsg{lines: lines}
	}
}

// cmdBeat4Select re-asks the identical question with nothing edited and no
// rebase, which is the only way "reuse" is a causal claim rather than a trick,
// and then RECORDS that answer -- see monographRecordReuse for why a demo that
// only showed the skip on screen left the graph unable to evidence it.
func cmdBeat4Select(e *env, sha, changedCSV string) tea.Cmd {
	return func() tea.Msg {
		lines := []string{beatLine("BEAT 4 -- REUSE: ask again about the same content")}
		lines = append(lines, note("Nothing edited, nothing rebased -- the same question again:"))
		plan, err := monographAffected(e, changedCSV)
		if err != nil {
			return stepMsg{err: err}
		}
		lines = append(lines, renderSelection(plan)...)
		lines = append(lines, "",
			note("Beat 3 recorded a PASSED run against each of those content hashes."),
			note("A rebase moves the SHA, not the hashes: ./bench/rebase-scenario.sh"),
		)
		return stepMsg{lines: lines, plan: &plan}
	}
}

// cmdBeat4Record records the skip itself -- the last RECORD of the demo, and
// the one the whole reuse claim rests on. It is a step of its own for the same
// reason as cmdBeat3Record, plus one more: this is where the script used to run
// straight on into the closing summary without waiting, so the final stage lit
// and the demo moved on before anyone could look at it.
func cmdBeat4Record(e *env, sha string) tea.Cmd {
	return func() tea.Msg {
		recordOut, err := monographRecordReuse(e, sha)
		var lines []string
		for _, l := range strings.Split(recordOut, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, dimStyle.Render("    "+l))
			}
		}
		if err != nil {
			return stepMsg{err: err, lines: lines}
		}
		lines = append(lines, "",
			note("The skip is in the graph now, citing the runs that proved it"),
			note("(:PROVEN_BY) -- evidence for work that did NOT happen."),
		)
		return stepMsg{lines: lines}
	}
}

// cmdEvidence is the demo's last step: the same recorded run, read back out of
// the graph as a JFrog Evidence predicate.
//
// It attests BEAT 4 -- the selection where nothing executed and every skip
// carries a citation -- and that is the strongest version of the claim rather
// than a convenient one. A predicate saying "these four targets are verified at
// this exact content, and here are the earlier runs that verified them" is
// checkable by someone who does not trust the machine that produced it, which is
// precisely the trust gap local-first CI leaves open (ADR-003).
//
// The predicate is generated for real. The upload is not attempted: `jf evd
// create` needs a subject in Artifactory and a signing key, and whether Evidence
// signing is available on a given tier is listed in ADR-003 as unverified. The
// step shows the command it would run and says so.
func cmdEvidence(e *env) tea.Cmd {
	return func() tea.Msg {
		predicate, command, err := monographEvidence(e, reuseRunID(e))
		if err != nil {
			return stepMsg{err: err}
		}
		return stepMsg{
			lines:       evidenceLines(predicate, command, e.evidenceJSON()),
			evidence:    predicate,
			evidenceCmd: command,
		}
	}
}

// evidenceLines renders the step's transcript block. Split out from the Cmd so
// it can be read by a test without a graph, a binary, or a clone -- this block
// is the one the audience actually reads, and "does it name both destinations
// and say the upload did not happen" is worth pinning.
func evidenceLines(predicate, command, path string) []string {
	lines := []string{
		boldLine("RECORD writes to two systems of record"),
		"",
		note("→ Neo4j            CIRun, SELECTED, PROVEN_BY        ") + successStyle.Render("[written]"),
		dimStyle.Render("      the graph COMPUTES the decision -- mutable, cross-run"),
		note("→ JFrog Evidence   ci-coverage/v1 predicate           ") + evidenceStyle.Render("[emitted]"),
		dimStyle.Render("      Evidence RECORDS it -- per-version, signed, immutable"),
		"",
	}

	var ev Evidence
	if err := json.Unmarshal([]byte(predicate), &ev); err != nil {
		// Not fatal: the predicate itself is already in hand and the panel can
		// show it. Only the one-line summary is lost, and saying so beats
		// printing a count derived from nothing.
		lines = append(lines, dimStyle.Render("    (could not summarise the predicate: "+err.Error()+")"))
	} else {
		lines = append(lines, note(fmt.Sprintf(
			"affected %d  ⊆  executed %d  ∪  proven-reusable %d      coverageGaps: %d",
			len(ev.Affected), len(ev.Executed), ev.ProvenSkips(), len(ev.CoverageGaps))))
	}

	lines = append(lines, "")
	for _, l := range strings.Split(command, "\n") {
		lines = append(lines, cypherStyle.Render("    "+l))
	}
	return append(lines, "",
		dimStyle.Render("    predicate written to "+path),
		note("Not run: the subject is an artifact in Artifactory and signing needs"),
		note("a key. JFrog's own Dagger integration attests HOW this was built --"),
		note("a signed link to the Cloud trace. This attests what a trace cannot:"),
		note("what was NOT run, and why that was safe."),
		dimStyle.Render("    e: the full predicate"),
	)
}

// runDaggerPlan runs `dagger call orchestrator-dang run --plan=...` against
// the plan.json a select step just wrote, under the given label.
func runDaggerPlan(e *env, label string) (daggerRun, error) {
	return runDagger(e, e.reportJSON(), "call", "orchestrator-dang", "run",
		"--plan="+e.planJSON(), "--run-id=demo-"+label+"-"+e.nonce)
}

func doneLines(e *env) []string {
	lines := []string{boldLine("demo complete")}
	lines = append(lines, notes(
		"naive: 9 targets, always.",
		"graph: 1 for a typo, 4 for a shared lib, 0 for proven content.",
		"and that 0 leaves a document: what was skipped, and what proved it.",
	)...)
	switch {
	case e.fresh:
		lines = append(lines, note("fresh engine: 47-48s for nine targets vs 11-12s for one, ~4x."))
	case !e.cold:
		lines = append(lines, note("warm engine: wall clock does not separate the arms, as expected."))
	}
	return lines
}
