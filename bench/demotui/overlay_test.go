package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// fakeQueries stands in for what `monograph queries --json` returns, for the
// pure tests. TestOverlayShowsTheRealToolsQueries below uses the real binary.
func fakeQueries() []CypherQuery {
	return []CypherQuery{
		{Name: "reuse-lookup", Stage: "select", Kind: "read",
			Title: "Has this exact content already passed?", When: "every selection",
			Cypher: "UNWIND $hashes AS h\nRETURN h"},
		{Name: "affected-reachability", Stage: "select", Kind: "read",
			Title: "What does this change affect?", When: "only with --via-cypher",
			Cypher: "MATCH (a:Target)-[:DEPENDS_ON*0..]->(c)\nRETURN a"},
		{Name: "record-target-runs", Stage: "record", Kind: "write",
			Title: "One TargetRun per executed target", When: "every record",
			Cypher: "MERGE (tr:TargetRun {id: $id})"},
		{Name: "record-proven-by", Stage: "record", Kind: "write",
			Title: "Cite the run that justifies each skip", When: "only when the plan reused something",
			Cypher: "MERGE (run)-[pb:PROVEN_BY]->(proof)"},
	}
}

// TestCypherForMatchesWhatTheStepRuns is the honesty property of the panel: a
// step must never be shown a query it did not run.
//
// Two specific traps. PROVEN_BY executes only when a plan reused something, so
// it belongs to beat 4's record and not beat 3's. And EDIT/RUN touch no graph at
// all -- offering Cypher there would imply the graph is involved in git and
// Dagger work that it is not.
func TestCypherForMatchesWhatTheStepRuns(t *testing.T) {
	all := fakeQueries()

	names := func(qs []CypherQuery) []string {
		var out []string
		for _, q := range qs {
			out = append(out, q.Name)
		}
		return out
	}

	if got := names(cypherFor(phaseBeat3Record, all)); len(got) != 1 || got[0] != "record-target-runs" {
		t.Errorf("beat 3 record shows %v; PROVEN_BY does not fire there, nothing was skipped", got)
	}

	got := names(cypherFor(phaseBeat4Record, all))
	if len(got) != 2 || got[0] != "record-proven-by" {
		t.Errorf("beat 4 record shows %v, want PROVEN_BY first -- it is the query that fires in this step", got)
	}

	for _, p := range []Phase{phaseBeat2Select, phaseBeat3Select, phaseBeat4Select} {
		if got := names(cypherFor(p, all)); len(got) != 2 {
			t.Errorf("phase %d shows %v, want both select reads", p, got)
		}
	}

	for _, p := range []Phase{phaseIntro, phaseDocsEdit, phaseBeat1List, phaseBeat1Run, phaseBeat2Run, phaseCoreEdit, phaseBeat3Run} {
		if qs := cypherFor(p, all); len(qs) != 0 {
			t.Errorf("phase %d offers Cypher (%v) but runs none", p, names(qs))
		}
	}
}

// TestPanelLabelsWhenEachQueryRuns pins the label that keeps the SELECT page
// honest. The default selection resolves the affected walk IN MEMORY and asks
// the graph only about history, so presenting the two reads as an equal pair
// would misrepresent what just happened.
func TestPanelLabelsWhenEachQueryRuns(t *testing.T) {
	body := stripANSI(cypherPanelBody(phaseBeat2Select, fakeQueries(), nil))
	for _, want := range []string{"every selection", "only with --via-cypher"} {
		if !strings.Contains(body, want) {
			t.Errorf("select panel is missing the %q qualifier", want)
		}
	}
	if !strings.Contains(body, "READ") {
		t.Error("select panel does not say these are reads")
	}
	if w := stripANSI(cypherPanelBody(phaseBeat4Record, fakeQueries(), nil)); !strings.Contains(w, "WRITE") {
		t.Error("record panel does not say these are writes")
	}
	if none := stripANSI(cypherPanelBody(phaseBeat1Run, fakeQueries(), nil)); !strings.Contains(none, "No Cypher at this stage") {
		t.Error("a stage with no Cypher should say so plainly")
	}
	// A failure to read the queries must surface in the panel, not vanish.
	bad := stripANSI(cypherPanelBody(phaseBeat2Select, nil, errors.New("boom")))
	if !strings.Contains(bad, "boom") {
		t.Error("the load error is not shown; the panel would look empty for no reason")
	}
}

// TestPanelFillsTheTranscriptSlotExactly pins the geometry the no-ANSI-slicing
// approach depends on: the panel is rendered INSTEAD of the transcript, so if it
// is not exactly that size, the status line below it moves when it opens.
func TestPanelFillsTheTranscriptSlotExactly(t *testing.T) {
	for _, size := range [][2]int{{100, 22}, {80, 12}, {140, 30}} {
		w, h := size[0], size[1]
		iw, ih := overlayInner(w, h)
		vp := lipgloss.NewStyle().Width(iw).Height(ih).Render("MATCH (n) RETURN n")
		panel := cypherPanel(vp, w, h)
		if got := lipgloss.Width(panel); got != w {
			t.Errorf("%dx%d: panel width = %d, want %d", w, h, got, w)
		}
		if got := lipgloss.Height(panel); got != h {
			t.Errorf("%dx%d: panel height = %d, want %d", w, h, got, h)
		}
	}
}

// TestOverlayDoesNotDisturbTheTranscript pins that the panel is genuinely a
// separate surface: opening it, scrolling inside it, and closing it must leave
// the transcript's scroll position and both anchors untouched, or the beat-at-top
// behaviour would jump every time a presenter looked at a query.
func TestOverlayDoesNotDisturbTheTranscript(t *testing.T) {
	m := NewModel(&env{})
	m.width, m.height = 100, 30
	m.resizeViewport()
	m.cypher = fakeQueries()

	m.appendTranscript(beatLine("BEAT 2 -- THE SAME COMMIT, GRAPH-SELECTED"))
	for i := 0; i < 8; i++ {
		m.appendTranscript(note("selection line"))
	}
	m.phase = phaseBeat2Select

	beforeOffset, beforeAnchor, beforeStep := m.viewport.YOffset, m.anchor, m.stepAnchor
	beforeView := m.viewport.View()

	var tm tea.Model = m
	press := func(k string) {
		tm, _ = tm.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	}
	press("c")
	if !tm.(Model).overlayOpen {
		t.Fatal(`"c" did not open the panel`)
	}
	if body := stripANSI(tm.(Model).overlayVP.View()); !strings.Contains(body, "UNWIND $hashes") {
		t.Error("the open panel is not showing the select query")
	}
	// Scroll inside the panel, then close.
	tm, _ = tm.(Model).Update(tea.KeyMsg{Type: tea.KeyDown})
	press("c")
	after := tm.(Model)
	if after.overlayOpen {
		t.Fatal(`"c" did not close the panel`)
	}
	if after.viewport.YOffset != beforeOffset || after.anchor != beforeAnchor || after.stepAnchor != beforeStep {
		t.Errorf("transcript moved: offset %d->%d, anchor %d->%d, step %d->%d",
			beforeOffset, after.viewport.YOffset, beforeAnchor, after.anchor, beforeStep, after.stepAnchor)
	}
	if after.viewport.View() != beforeView {
		t.Error("the transcript renders differently after the panel closed")
	}
}

// TestOverlaySwallowsAdvance pins that Enter cannot advance the demo while the
// panel covers the transcript -- a step running behind it would be missed.
func TestOverlaySwallowsAdvance(t *testing.T) {
	m := NewModel(&env{})
	m.width, m.height = 100, 30
	m.resizeViewport()
	m.cypher = fakeQueries()
	m.phase = phaseBeat2Select
	m.waiting = true
	m.overlayOpen = true
	m.syncOverlay()

	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := tm.(Model)
	if after.phase != phaseBeat2Select {
		t.Errorf("Enter advanced to phase %d from behind the panel", after.phase)
	}
	if after.overlayOpen {
		t.Error("Enter should close the panel")
	}
}

// TestOverlayShowsTheRealToolsQueries is the one that makes the rest mean
// something: the panel's content comes from the real monograph binary, so what
// is displayed is the same string the driver executes. A hardcoded copy here
// would pass every other test in this file and still be wrong the day a query
// changes.
func TestOverlayShowsTheRealToolsQueries(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("not in a repo: %v", err)
	}
	bin := filepath.Join(root, ".bin", "monograph")
	e := &env{root: root, monographBin: bin}
	qs, err := monographQueries(e)
	if err != nil {
		t.Skipf("monograph queries unavailable (build .bin/monograph): %v", err)
	}

	want := map[string]string{
		"reuse-lookup": "select", "affected-reachability": "select",
		"record-target-runs": "record", "record-proven-by": "record",
		"evidence-predicate": "evidence",
	}
	got := map[string]string{}
	for _, q := range qs {
		got[q.Name] = q.Stage
		if strings.TrimSpace(q.Cypher) == "" {
			t.Errorf("%s: empty Cypher from the real tool", q.Name)
		}
	}
	for name, stage := range want {
		if got[name] != stage {
			t.Errorf("query %q is at stage %q, want %q", name, got[name], stage)
		}
	}

	// And it reaches the panel unaltered.
	body := stripANSI(cypherPanelBody(phaseBeat4Record, qs, nil))
	for _, q := range qs {
		if q.Name != "record-proven-by" {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(q.Cypher), "\n") {
			if !strings.Contains(body, strings.TrimSpace(line)) {
				t.Errorf("panel is missing a line of the executed query: %q", line)
			}
		}
	}
}
