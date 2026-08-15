package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
)

// TestRenderDiagramContainsExpectedLabels pins that every stage label appears
// somewhere in the rendered frame, regardless of status -- a status change
// must never make a node vanish, only change how it looks.
func TestRenderDiagramContainsExpectedLabels(t *testing.T) {
	s := DiagramState{
		Statuses: [numStages]NodeStatus{
			StageEdit:    StatusDone,
			StageExtract: StatusDone,
			StageSelect:  StatusActive,
			StageRun:     StatusPending,
			StageRecord:  StatusPending,
		},
	}
	out := RenderDiagram(s)
	for _, label := range stageLabel {
		if !strings.Contains(out, label) {
			t.Errorf("rendered diagram missing stage label %q\n%s", label, out)
		}
	}
}

// TestRenderDiagramBypassDashesExtractSelectEdge pins that naive mode (no
// selection consulted) renders visibly differently from graph mode: the
// EXTRACT->SELECT edge must use the dashed bypass arrow, not the solid one.
func TestRenderDiagramBypassDashesExtractSelectEdge(t *testing.T) {
	bypassed := RenderDiagram(DiagramState{Bypass: true})
	normal := RenderDiagram(DiagramState{Bypass: false})
	if bypassed == normal {
		t.Error("bypass mode rendered identically to normal mode; the naive-CI story is not visible")
	}
	if !strings.Contains(bypassed, "╌╌▶") {
		t.Error("bypass mode did not render the dashed bypass arrow")
	}
}

// TestRenderDiagramReuseLoopOnlyWhenRequested pins that the feedback loop
// annotation is absent until beats.go asks for it -- the diagram must not
// claim "reuse" causality before any run has actually proven it.
func TestRenderDiagramReuseLoopOnlyWhenRequested(t *testing.T) {
	without := RenderDiagram(DiagramState{ShowReuseLoop: false})
	if strings.Contains(without, "reuse:") {
		t.Error("reuse loop label present when ShowReuseLoop was false")
	}
	with := RenderDiagram(DiagramState{ShowReuseLoop: true})
	if !strings.Contains(with, "reuse:") {
		t.Error("reuse loop label absent when ShowReuseLoop was true")
	}
}

// TestRenderNodeWidthIsStatusIndependent pins that a node's rendered width
// depends only on its label, never on its status.
//
// Status markers (✓ for done, · for skipped) are drawn into a fixed
// two-column prefix that is blank when there is no marker. If any status
// rendered a different width, that box and every box to its right would shift
// the moment a stage completed -- and so would the callout centered above
// SELECT. The diagram is on screen for the whole demo; it must not twitch.
// The spinner frame drawn into an active node shares those same two columns, so
// every frame is checked here too: an animation that changed the box's width
// would make the whole row jitter eight times a second.
func TestRenderNodeWidthIsStatusIndependent(t *testing.T) {
	const label = "EXTRACT"
	widths := map[NodeStatus]int{}
	for _, st := range []NodeStatus{StatusPending, StatusActive, StatusDone, StatusSkipped} {
		widths[st] = lipgloss.Width(renderNode(label, st, ""))
	}
	want := widths[StatusPending]
	for st, got := range widths {
		if got != want {
			t.Errorf("renderNode(%q, status %d) width = %d, want %d (same as every other status)",
				label, st, got, want)
		}
	}

	for _, frame := range append(spinner.Dot.Frames, "", "⣾⣽", "漢", "✓") {
		if got := lipgloss.Width(renderNode(label, StatusActive, frame)); got != want {
			t.Errorf("renderNode(%q, active, frame %q) width = %d, want %d",
				label, frame, got, want)
		}
	}
}

// TestActiveMarkerRefusesUnusableFrames pins the guard rather than the happy
// path. A single-cell frame is shown; a multi-rune frame is narrowed to its
// first cell so it still animates; and a frame whose first rune is double-width
// falls back to blanks, because that one cannot be narrowed without widening
// the box it sits in.
func TestActiveMarkerRefusesUnusableFrames(t *testing.T) {
	if got := activeMarker("⣾"); got != "⣾ " {
		t.Errorf("activeMarker(\"⣾\") = %q, want the frame plus a space", got)
	}
	if got := activeMarker("⣾⣽"); got != "⣾ " {
		t.Errorf("activeMarker(\"⣾⣽\") = %q, want it narrowed to the first cell", got)
	}
	for _, bad := range []string{"", "漢"} {
		if got := activeMarker(bad); got != "  " {
			t.Errorf("activeMarker(%q) = %q, want two blanks", bad, got)
		}
	}
}

// TestBypassGraysTheGraphCallout pins that the beat which does not consult the
// graph says so at the callout too, not just on the edge below it. Asserted on
// the style rather than on escape codes in the output.
func TestBypassGraysTheGraphCallout(t *testing.T) {
	if got := calloutStyle(true).GetForeground(); got != colorDim {
		t.Errorf("bypassed callout foreground = %v, want the dim gray %v", got, colorDim)
	}
	if got := calloutStyle(false).GetForeground(); got != colorGreen {
		t.Errorf("live callout foreground = %v, want green %v", got, colorGreen)
	}
	// The bypassed edge must be the same gray, so "not this beat" reads as one
	// colour across the callout, the edge, and the two skipped boxes.
	if got := arrowBypassStyle.GetForeground(); got != colorDim {
		t.Errorf("bypass arrow foreground = %v, want the same dim gray %v as the callout", got, colorDim)
	}
}

// TestRenderArrowWidthsMatch pins that the forward and bypass shafts occupy
// the same number of cells, and that each occupies exactly five.
//
// The forward shaft is an em dash rather than a box-drawing horizontal (see
// renderArrow for why the box character had to go). Em dash is East Asian
// Ambiguous width, so a terminal configured for CJK-wide ambiguous characters
// would draw it two cells wide while this measurement still says one -- every
// box right of the first arrow would then sit one column left of where the
// callout and the reuse loop think it is. Five is therefore not decoration: it
// is the number the column geometry in RenderDiagram is computed from.
func TestRenderArrowWidthsMatch(t *testing.T) {
	fwd, byp := lipgloss.Width(renderArrow(false)), lipgloss.Width(renderArrow(true))
	if fwd != byp {
		t.Errorf("forward arrow is %d cells, bypass is %d; boxes shift when an edge changes style", fwd, byp)
	}
	if fwd != 5 {
		t.Errorf("forward arrow is %d cells, want 5 (2 shaft + head + 2 pad)", fwd)
	}
}

// TestRenderDiagramWidthStableAcrossProgress is the same property at the
// whole-diagram level: advancing every stage from pending to done must not
// change the rendered width of a single line.
func TestRenderDiagramWidthStableAcrossProgress(t *testing.T) {
	var allPending, allDone DiagramState
	for st := Stage(0); st < numStages; st++ {
		allPending.Statuses[st] = StatusPending
		allDone.Statuses[st] = StatusDone
	}
	if a, b := lipgloss.Width(RenderDiagram(allPending)), lipgloss.Width(RenderDiagram(allDone)); a != b {
		t.Errorf("diagram width = %d all-pending vs %d all-done; the row shifts as stages complete", a, b)
	}
}

// TestRenderDiagramDeterministic pins that rendering is a pure function of
// its input -- callers (beats.go) can safely re-render every frame without
// tracking any hidden state inside diagram.go itself.
func TestRenderDiagramDeterministic(t *testing.T) {
	s := DiagramState{ShowReuseLoop: true, ReuseActive: true}
	a := RenderDiagram(s)
	b := RenderDiagram(s)
	if a != b {
		t.Error("RenderDiagram returned different output for identical input")
	}
}
