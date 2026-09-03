package main

import (
	"encoding/json"
	"errors"
	"os"
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

	// The real queries are the ones that ship, so the paste-safety rule is
	// asserted against them and not only against the fixtures: every line the
	// panel shows is either a Cypher comment or part of a statement, and the
	// terminator is added exactly once even if the tool starts emitting its own.
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "READ --") || strings.HasPrefix(line, "WRITE --") || strings.HasPrefix(line, "runs:") {
			t.Errorf("uncommented prose in the panel: %q", line)
		}
	}
	yanked := overlayYankBody(modeCypher, phaseBeat4Record, qs, "")
	if strings.Contains(yanked, ";;") {
		t.Errorf("doubled terminator -- the tool now emits its own:\n%s", yanked)
	}
	if want := len(cypherFor(phaseBeat4Record, qs)); strings.Count(yanked, ";") != want {
		t.Errorf("want %d terminators for %d real queries:\n%s", want, want, yanked)
	}
}

// TestYankBodyIsPasteable is the property the "y" key exists for: what lands on
// the clipboard must be text a person can paste into Neo4j Browser or jq, not a
// screenshot of a panel.
//
// The traps are ANSI escapes and the border glyphs -- both arrive the moment
// anyone reaches for the rendered body instead of the source values.
func TestYankBodyIsPasteable(t *testing.T) {
	body := overlayYankBody(modeCypher, phaseBeat4Record, fakeQueries(), "")
	if body == "" {
		t.Fatal("beat 4 record yanked nothing")
	}
	if strings.Contains(body, "\x1b") {
		t.Errorf("yanked body carries ANSI escapes:\n%q", body)
	}
	for _, glyph := range []string{"│", "─", "╭", "╮", "╰", "╯"} {
		if strings.Contains(body, glyph) {
			t.Errorf("yanked body carries border glyph %q:\n%s", glyph, body)
		}
	}
	// Every non-comment line has to be Cypher: the reading aids the panel shows
	// above each query are a syntax error when pasted, so they must be commented.
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.Contains(line, "runs: ") || strings.HasPrefix(line, "READ --") || strings.HasPrefix(line, "WRITE --") {
			t.Errorf("uncommented header leaked into the yank: %q", line)
		}
	}
	if !strings.Contains(body, "MERGE (run)-[pb:PROVEN_BY]->(proof)") {
		t.Errorf("yank dropped the query the step actually runs:\n%s", body)
	}
	// Most stages yank more than one query, and Cypher's only statement
	// separator is the semicolon: without it the second statement starts on the
	// line after the first ends and the whole paste fails to parse.
	if n := len(cypherFor(phaseBeat4Record, fakeQueries())); n < 2 {
		t.Fatalf("this test needs a multi-query stage; beat 4 record has %d", n)
	}
	if got := strings.Count(body, ";"); got != 2 {
		t.Errorf("want one terminator per query, got %d:\n%s", got, body)
	}
	if !strings.HasSuffix(body, ";") {
		t.Errorf("last query is unterminated:\n%s", body)
	}
	// The same filtering as the panel, or the clipboard and the screen disagree
	// about what the step ran.
	if strings.Contains(overlayYankBody(modeCypher, phaseBeat3Record, fakeQueries(), ""), "PROVEN_BY") {
		t.Error("beat 3 yanked a query it did not run")
	}
	if got := overlayYankBody(modeCypher, phaseBeat1Run, fakeQueries(), ""); got != "" {
		t.Errorf("a phase with no graph work yanked %q", got)
	}
}

// TestYankEvidenceStaysJSON: the predicate is the thing most likely to be
// copied, and a header line above it means it no longer parses.
func TestYankEvidenceStaysJSON(t *testing.T) {
	pred := "{\n  \"targets\": [\"a\", \"b\"],\n  \"reused\": 1\n}"
	got := overlayYankBody(modeEvidence, phaseEvidence, fakeQueries(), pred)
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("yanked predicate does not parse as JSON: %v\n%s", err, got)
	}
	if len(v) != 2 {
		t.Errorf("yanked predicate lost fields: %v", v)
	}
}

// TestYankKeyReportsWhatItDid covers the two things the "y" key promises beyond
// putting text somewhere: that the status line says which of the two outcomes
// happened, and that a machine with no clipboard loses nothing.
//
// The fallback is the branch worth the fixture. It never runs on a laptop -- the
// developer's pbcopy always succeeds -- so the one place it can be exercised is
// here, and a silent failure on a presenter's ssh session is the exact outcome
// the branch exists to prevent.
func TestYankKeyReportsWhatItDid(t *testing.T) {
	saved := clipboardWrite
	defer func() { clipboardWrite = saved }()

	newModel := func() Model {
		m := NewModel(&env{})
		m.width, m.height = 100, 30
		m.resizeViewport()
		m.cypher = fakeQueries()
		m.phase = phaseBeat4Record
		m.overlayOpen, m.overlayMode = true, modeCypher
		m.syncOverlay()
		return m
	}
	press := func(m Model, key string) Model {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		return out.(Model)
	}

	// Success: the clipboard gets the raw body, and the panel stays open so the
	// presenter can keep talking over the query they just copied.
	var got string
	clipboardWrite = func(s string) error { got = s; return nil }
	m := press(newModel(), "y")
	if want := overlayYankBody(modeCypher, phaseBeat4Record, fakeQueries(), ""); got != want {
		t.Errorf("clipboard got the wrong text:\n%q", got)
	}
	if !m.overlayOpen {
		t.Error("y closed the panel")
	}
	if !strings.Contains(m.yankMsg, "Copied") {
		t.Errorf("status line does not report the copy: %q", m.yankMsg)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, m.yankMsg) {
		t.Errorf("the report never reaches the screen:\n%s", view)
	}

	// Failure: the text lands in a file, the status line names it, and the
	// transcript keeps the full path.
	clipboardWrite = func(string) error { return errors.New("no clipboard") }
	before := newModel()
	m = press(before, "y")
	if strings.Contains(m.yankMsg, "Copied") {
		t.Errorf("a failed write reported success: %q", m.yankMsg)
	}
	if !strings.Contains(m.yankMsg, "No clipboard") {
		t.Errorf("status line hides the failure: %q", m.yankMsg)
	}
	if len(m.transcript) != len(before.transcript)+1 {
		t.Fatalf("the spill file's path was not recorded: %d -> %d",
			len(before.transcript), len(m.transcript))
	}
	line := stripANSI(m.transcript[len(m.transcript)-1])
	path := strings.TrimSpace(line[strings.LastIndex(line, " ")+1:])
	spilled, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("status line names a file that is not there (%q): %v", path, err)
	}
	defer os.Remove(path)
	if want := overlayYankBody(modeCypher, phaseBeat4Record, fakeQueries(), ""); strings.TrimSpace(string(spilled)) != want {
		t.Errorf("spill file does not hold the query:\n%s", spilled)
	}

	// And the key is discoverable: a panel that can be copied has to say so.
	if view := stripANSI(newModel().View()); !strings.Contains(view, "y: copy") {
		t.Errorf("the open panel never advertises the key:\n%s", view)
	}
}
