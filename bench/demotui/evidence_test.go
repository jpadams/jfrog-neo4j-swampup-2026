package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TestEvidenceForkAppearsOnlyAtTheFinalStep is the same discipline
// TestReuseLoopAppearsOnlyAtBeat4 pins, for the other annotation: the diagram
// must not promise a second destination for RECORD's output before anything has
// gone there.
//
// RECORD runs twice before this step, at beats 3 and 4, and both times it writes
// to Neo4j alone. A fork drawn then would be describing an intention.
func TestEvidenceForkAppearsOnlyAtTheFinalStep(t *testing.T) {
	m := NewModel(&env{})

	for p := phaseIntro; p < phaseEvidence; p++ {
		var cmd tea.Cmd
		m, cmd = m.startPhase(p)
		_ = cmd
		if m.diagram.ShowEvidenceFork {
			t.Fatalf("phase %d: the evidence fork is showing before the step that emits one", p)
		}
		tm, _ := m.handleStep(stepMsg{})
		m = tm.(Model)
		if m.diagram.ShowEvidenceFork {
			t.Fatalf("phase %d completed: the evidence fork appeared early", p)
		}
		if strings.Contains(stripANSI(RenderDiagram(m.diagram)), "JFrog") {
			t.Fatalf("phase %d: the rendered diagram names JFrog already", p)
		}
	}

	m, _ = m.startPhase(phaseEvidence)
	if !m.diagram.ShowEvidenceFork {
		t.Error("the evidence step started without its fork")
	}
	if m.diagram.EvidenceEmitted {
		t.Error("the JFrog arm is lit before a predicate exists")
	}
	frame := stripANSI(RenderDiagram(m.diagram))
	if !strings.Contains(frame, "JFrog Evidence") || !strings.Contains(frame, "Neo4j ◀——┬") {
		t.Errorf("the fork is missing from the rendered diagram:\n%s", frame)
	}
	// Dashed on the JFrog side, always: the predicate is emitted and not
	// uploaded, and a solid arrow would claim an attestation this repo does not
	// make.
	if !strings.Contains(frame, "╌╌▶ JFrog") {
		t.Errorf("the JFrog arm should be dashed -- nothing is uploaded:\n%s", frame)
	}

	tm, _ := m.handleStep(stepMsg{evidence: `{"predicateType":"x"}`})
	m = tm.(Model)
	if !m.diagram.EvidenceEmitted {
		t.Error("the JFrog arm stayed dim after a predicate was produced")
	}
}

// TestEvidenceForkDegradesOnNarrowTerminals pins the one annotation that can be
// wider than the node row.
//
// centerBlock centres the block on its widest LINE, so a fork label that
// overhangs the row shifts every box right -- the diagram would look
// mis-aligned, and on a narrow terminal it would wrap. The label shortens
// instead; "JFrog" still names the destination and the transcript carries the
// rest.
func TestEvidenceForkDegradesOnNarrowTerminals(t *testing.T) {
	wide := stripANSI(RenderDiagram(DiagramState{ShowEvidenceFork: true, MaxWidth: 200}))
	if !strings.Contains(wide, "JFrog Evidence") {
		t.Errorf("a wide terminal should get the full label:\n%s", wide)
	}

	narrow := stripANSI(RenderDiagram(DiagramState{ShowEvidenceFork: true, MaxWidth: 80}))
	if strings.Contains(narrow, "JFrog Evidence") {
		t.Errorf("an 80-column terminal cannot fit the full label:\n%s", narrow)
	}
	if !strings.Contains(narrow, "JFrog") {
		t.Errorf("the shortened label must still name the destination:\n%s", narrow)
	}
	if w := lipgloss.Width(RenderDiagram(DiagramState{ShowEvidenceFork: true, MaxWidth: 80})); w > 80 {
		t.Errorf("diagram is %d cells wide on an 80-column terminal; it will wrap", w)
	}
}

// TestEvidenceForkUsesTheSameArrowVocabulary pins the glyphs.
//
// renderArrow's comment explains at length why the row's shafts are em dashes
// (U+2014) and not the box-drawing horizontal (U+2500) they look like: Ghostty
// and Kitty synthesize U+2500 themselves, on the cell's centre line, a third the
// weight of the ▶ head that comes from the font. The fork's first version used
// U+2500 and looked exactly like that -- a thin arrow, off-centre against its
// head, next to five correct ones.
func TestEvidenceForkUsesTheSameArrowVocabulary(t *testing.T) {
	// The annotation rows only: the node boxes are drawn with a rounded border,
	// whose own horizontals ARE U+2500 -- legitimately, since lipgloss draws the
	// whole box in one vocabulary. The arrows are what must not use it.
	frame := stripANSI(RenderDiagram(DiagramState{ShowEvidenceFork: true}))
	forkRows := strings.Join(strings.Split(frame, "\n")[:2], "\n")

	if strings.ContainsRune(forkRows, '─') {
		t.Errorf("the fork uses U+2500; terminals synthesize that glyph and it will not match the row's arrows:\n%s", forkRows)
	}
	// The solid arm is em dashes, like the forward edge; the dashed arm is U+254C,
	// like the bypass edge. Both two shaft cells plus a head, as renderArrow does.
	if !strings.Contains(frame, "◀——") {
		t.Errorf("the Neo4j arm should use the em-dash shaft the forward arrows use:\n%s", frame)
	}
	if !strings.Contains(frame, "╌╌▶") {
		t.Errorf("the JFrog arm should use the same dashes as the bypass arrow:\n%s", frame)
	}
	if got, want := lipgloss.Width(forkLeftArm), lipgloss.Width(strings.TrimSpace(renderArrowPlain())); got != want {
		t.Errorf("fork arm is %d cells, the row's arrow is %d; they will not read as the same drawing", got, want)
	}
}

// renderArrowPlain is renderArrow's glyphs without styling, for measurement.
func renderArrowPlain() string { return " ——▶ " }

// TestEvidenceForkDoesNotMoveTheNodeRow is the regression for a bug that got
// past the first round of tests because it was asserted at the wrong layer.
//
// RenderDiagram's own output was stable -- the earlier test checked that and
// passed -- but Model.View CENTERS the block, and centering on the widest line
// meant the fork's overhang dragged every box left by half of it at the moment
// the fork appeared. The boxes visibly jumped on the demo's last step. The
// assertion has to be on the composed view, which is what the audience sees.
func TestEvidenceForkDoesNotMoveTheNodeRow(t *testing.T) {
	columnOfEdit := func(fork bool) int {
		m := NewModel(&env{})
		m.width, m.height = 150, 40
		m.diagram.Statuses[StageRecord] = StatusDone
		m.diagram.ShowEvidenceFork = fork
		m.diagram.EvidenceEmitted = fork
		m.resizeViewport()
		for _, line := range strings.Split(stripANSI(m.View()), "\n") {
			if i := strings.Index(line, "EDIT"); i >= 0 {
				return i
			}
		}
		return -1
	}

	without, with := columnOfEdit(false), columnOfEdit(true)
	if without < 0 || with < 0 {
		t.Fatalf("could not find the EDIT box: without=%d with=%d", without, with)
	}
	if without != with {
		t.Errorf("the node row moved from column %d to %d when the fork appeared; "+
			"the row is on screen the whole demo and must not jump", without, with)
	}
}

// TestEvidenceForkDoesNotMoveTheGraphCallout pins that the two annotations
// sharing these rows stay independent.
//
// Both are placed by absolute column now. If the fork's arrival shifted the
// "DERIVED GRAPH (Neo4j)" label -- by being concatenated rather than placed --
// the top of the diagram would jump on the final step, which is exactly the
// twitch renderNode's fixed-width markers exist to prevent one row lower.
func TestEvidenceForkDoesNotMoveTheGraphCallout(t *testing.T) {
	const label = "DERIVED GRAPH (Neo4j)"
	columnOf := func(s DiagramState) int {
		first := strings.SplitN(stripANSI(RenderDiagram(s)), "\n", 2)[0]
		return strings.Index(first, label)
	}
	without := columnOf(DiagramState{})
	with := columnOf(DiagramState{ShowEvidenceFork: true})
	if without < 0 || with < 0 {
		t.Fatalf("the graph callout is missing: without=%d with=%d", without, with)
	}
	if without != with {
		t.Errorf("the graph callout moved from column %d to %d when the fork appeared", without, with)
	}
}

// TestEvidenceTranscriptNamesBothDestinations pins the block the audience
// actually reads.
//
// Three properties, each of which the step would be misleading without: it names
// both destinations, it reports the set relation with real counts rather than
// asserting coverage, and it says the upload did not happen. The last one
// matters most -- the `jf evd create` command is on screen in full, and without
// that line the step reads as a record of something that ran.
func TestEvidenceTranscriptNamesBothDestinations(t *testing.T) {
	predicate := `{"affected":[{"target":"a"},{"target":"b"},{"target":"c"},{"target":"d"}],
	  "executed":[],
	  "skipped":[{"target":"a","provenBy":{"ciRun":"r1"}},{"target":"b","provenBy":{"ciRun":"r1"}},
	             {"target":"c","provenBy":{"ciRun":"r1"}},{"target":"d","provenBy":{"ciRun":"r1"}}],
	  "coverageGaps":[]}`
	out := stripANSI(strings.Join(evidenceLines(predicate, "jf evd create --predicate evidence.json", "/tmp/evidence.json"), "\n"))

	for _, want := range []string{
		"Neo4j", "JFrog Evidence",
		"affected 4  ⊆  executed 0  ∪  proven-reusable 4",
		"coverageGaps: 0",
		"jf evd create",
		"Not run:",
		"/tmp/evidence.json",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the transcript block is missing %q:\n%s", want, out)
		}
	}
}

// TestEvidenceTranscriptSurvivesAnUnreadablePredicate: the summary line is
// derived from the document, so a document that will not parse costs the
// summary. It must not cost the step -- the predicate and the command are
// already in hand and are the substance of what is being shown.
func TestEvidenceTranscriptSurvivesAnUnreadablePredicate(t *testing.T) {
	out := stripANSI(strings.Join(evidenceLines("not json", "jf evd create", "/tmp/x.json"), "\n"))
	if !strings.Contains(out, "could not summarise") {
		t.Errorf("an unparseable predicate should say so rather than print a count from nothing:\n%s", out)
	}
	if !strings.Contains(out, "jf evd create") {
		t.Errorf("the command disappeared along with the summary:\n%s", out)
	}
}

// TestEvidencePanelShowsTheDocumentVerbatim: the panel exists so the audience
// can read the actual predicate, not a rendering of it. A summary would be a
// second implementation of the document, and the one on screen would be the one
// nobody signs.
func TestEvidencePanelShowsTheDocumentVerbatim(t *testing.T) {
	predicate := `{
  "predicateType": "https://jfrog.com/evidence/monograph/ci-coverage/v1",
  "executed": [],
  "coverageGaps": []
}`
	command := "jf evd create --predicate evidence.json \\\n  --key-alias <key-alias>"

	body := stripANSI(evidencePanelBody(predicate, command))
	for _, line := range strings.Split(predicate, "\n") {
		if !strings.Contains(body, strings.TrimSpace(line)) {
			t.Errorf("panel is missing a line of the predicate: %q", line)
		}
	}
	for _, line := range strings.Split(command, "\n") {
		if !strings.Contains(body, strings.TrimSpace(line)) {
			t.Errorf("panel is missing a line of the upload command: %q", line)
		}
	}
	// It has to say the upload did not happen. The command is on screen in full;
	// without that line the panel reads as a record of something that ran.
	if !strings.Contains(body, "not run") {
		t.Errorf("panel does not say the upload was not run:\n%s", body)
	}
}

// TestEvidencePanelBeforeThereIsOne pins that the panel explains itself rather
// than rendering an empty box, for the case where the key is pressed early.
func TestEvidencePanelBeforeThereIsOne(t *testing.T) {
	body := stripANSI(evidencePanelBody("", ""))
	if !strings.Contains(body, "No predicate yet") {
		t.Errorf("empty panel should say why it is empty:\n%s", body)
	}
}

// TestEvidenceKeyIsInertUntilAPredicateExists: "e" is advertised in the status
// line only once the final step has produced something, so the key must also do
// nothing before then. Opening an empty panel would cover the transcript with a
// blank box mid-demo.
func TestEvidenceKeyIsInertUntilAPredicateExists(t *testing.T) {
	m := NewModel(&env{})
	m.waiting = true

	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = tm.(Model)
	if m.overlayOpen {
		t.Error("\"e\" opened a panel with no predicate to show")
	}
	if hint := m.evidenceHint(); hint != "" {
		t.Errorf("the status line advertises %q with no predicate", hint)
	}

	m.evidence = `{"predicateType":"x"}`
	if m.evidenceHint() == "" {
		t.Error("the status line does not advertise \"e\" once a predicate exists")
	}
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = tm.(Model)
	if !m.overlayOpen || m.overlayMode != modeEvidence {
		t.Fatalf("\"e\" did not open the evidence panel: open=%v mode=%v", m.overlayOpen, m.overlayMode)
	}

	// And it closes on the same key. A presenter pressing "e" again means "put
	// this away"; leaving it up because the mode does not match reads as a hang.
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = tm.(Model)
	if m.overlayOpen {
		t.Error("\"e\" did not close the evidence panel")
	}
}

// TestEvidencePanelSwallowsAdvance is TestOverlaySwallowsAdvance for the second
// mode: Enter must not advance the demo behind a panel the presenter is talking
// over.
func TestEvidencePanelSwallowsAdvance(t *testing.T) {
	m := NewModel(&env{})
	m.waiting = true
	m.evidence = `{"predicateType":"x"}`
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = tm.(Model)

	before := m.phase
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(Model)
	if m.phase != before {
		t.Errorf("Enter advanced from phase %d to %d with the panel open", before, m.phase)
	}
}

// TestEvidenceStageShowsItsQuery pins that "c" at the final step offers the read
// that produced the predicate. That query is what makes "a serialisation of the
// graph" checkable rather than asserted, so it is the one place the panel has to
// have something to show.
func TestEvidenceStageShowsItsQuery(t *testing.T) {
	if got := cypherStage(phaseEvidence); got != "evidence" {
		t.Errorf("cypherStage(phaseEvidence) = %q, want \"evidence\"", got)
	}
	qs := []CypherQuery{
		{Name: "evidence-predicate", Stage: "evidence", Kind: "read", Title: "read it back", Cypher: "MATCH (run:CIRun)"},
		{Name: "reuse-lookup", Stage: "select", Kind: "read", Title: "reuse", Cypher: "MATCH (tr:TargetRun)"},
	}
	body := stripANSI(cypherPanelBody(phaseEvidence, qs, nil))
	if !strings.Contains(body, "MATCH (run:CIRun)") {
		t.Errorf("the evidence stage's query is missing from the panel:\n%s", body)
	}
	if strings.Contains(body, "MATCH (tr:TargetRun)") {
		t.Errorf("the panel shows a query this step does not run:\n%s", body)
	}
}
