package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Stage is one node in the persistent pipeline diagram. These mirror the real
// monograph verbs (extract / affected / record), not decorative labels, so the
// diagram stays an honest picture of what the tool actually does at each beat.
type Stage int

const (
	StageEdit Stage = iota
	StageExtract
	StageSelect
	StageRun
	StageRecord
	numStages
)

var stageLabel = [numStages]string{
	StageEdit:    "EDIT",
	StageExtract: "EXTRACT",
	StageSelect:  "SELECT",
	StageRun:     "RUN",
	StageRecord:  "RECORD",
}

// NodeStatus is a stage's visual state in the diagram at a point in time.
type NodeStatus int

const (
	StatusPending NodeStatus = iota
	StatusActive
	StatusDone
	StatusSkipped
)

// DiagramState is everything RenderDiagram needs to draw one frame. It carries
// no behaviour of its own -- beats.go decides these values, this file only
// paints them -- so the rendering is a pure function and testable without a
// running program.
type DiagramState struct {
	Statuses [numStages]NodeStatus

	// Bypass draws the EXTRACT->SELECT edge dashed and gray instead of solid
	// green, AND grays out the "DERIVED GRAPH (Neo4j)" callout feeding SELECT:
	// naive CI has no selection step, it just runs everything. This is how beat
	// 1 visually earns the phrase "no graph consulted" rather than asserting it
	// in text alone -- the graph is on screen the whole demo, so the beat that
	// does not use it has to be able to say so by going quiet.
	Bypass bool

	// Spinner is the current animation frame to show inside whichever node is
	// StatusActive, or "" for none. It lives here rather than being read from a
	// clock inside RenderDiagram so rendering stays a pure function of state:
	// the caller (Model.View, which already owns a ticking spinner for the
	// status line) decides what frame is current and when there is work to
	// animate at all, which is why the spinner can never be left spinning on a
	// node whose Cmd has already finished.
	Spinner string

	// ShowReuseLoop draws the dashed feedback path from RECORD back to SELECT.
	// It is switched on when BEAT 4 starts -- the beat that demonstrates reuse --
	// and not a moment before. Beat 3's record is what makes the skip possible,
	// but at that point nothing has been skipped, so drawing the loop there put
	// the claim on screen a whole beat ahead of its evidence.
	ShowReuseLoop bool
	// ReuseActive lights that loop green instead of leaving it dim purple --
	// true only once a skip has actually happened (beat 4), so the diagram
	// never claims a causal loop before the run that proves it.
	ReuseActive bool

	// ShowEvidenceFork draws the split above RECORD: the run's outcome goes to
	// Neo4j AND to a JFrog Evidence predicate. Switched on by the final step and
	// not before, the same discipline ShowReuseLoop follows -- the fork is a
	// claim about something that has happened, so it waits for the step that
	// does it.
	//
	// It renders on the two rows the graph callout already occupies rather than
	// below RECORD, for two reasons: below is where the reuse loop's right
	// vertical drops from, and these two rows cost no extra height.
	ShowEvidenceFork bool
	// EvidenceEmitted lights the JFrog arm once a predicate actually exists.
	// Before that it is dim, because at that point the tool is still reading the
	// graph and nothing has been produced to attest with.
	EvidenceEmitted bool

	// MaxWidth is the terminal width, or 0 for unbounded. The fork's right-hand
	// label is the only thing in this diagram that overhangs the node row, and
	// the block is centered on the ROW (see DiagramRowWidth), so an over-wide
	// label runs off the right edge instead of moving the boxes. Given a width,
	// the label shortens rather than either of those happening.
	MaxWidth int
}

// renderNode draws one box. Every status emits the SAME two-column marker
// prefix -- a blank one where there is no marker -- so a node's width is a
// function of its label alone and never of its state. Without that, a check
// mark appearing shifts that box, every box to its right, and the callout
// centered above SELECT, making the whole diagram twitch as the demo runs.
// TestRenderNodeWidthIsStatusIndependent pins it.
func renderNode(label string, status NodeStatus, spinner string) string {
	switch status {
	case StatusActive:
		return nodeActiveStyle.Render(activeMarker(spinner) + label)
	case StatusDone:
		return nodeDoneStyle.Render("✓ " + label)
	case StatusSkipped:
		return nodeSkippedStyle.Render("· " + label)
	default:
		return nodePendingStyle.Render("  " + label)
	}
}

// activeMarker is the executing node's two-column prefix: the spinner's current
// frame plus a space, or two blanks when there is no frame to draw.
//
// It takes the frame's FIRST single-cell rune and nothing else. The two columns
// are the same two that hold ✓ and · for the other statuses, and the whole
// diagram's geometry is built on a node's width depending only on its label
// (see TestRenderNodeWidthIsStatusIndependent) -- so a frame that arrived as a
// multi-rune string, or as one double-width rune, has to be refused here rather
// than be allowed to widen the RUN box by a column on every animation tick.
func activeMarker(frame string) string {
	for _, r := range frame {
		if s := string(r); lipgloss.Width(s) == 1 {
			return s + " "
		}
		break
	}
	return "  "
}

// renderArrow draws one edge. Both shafts are 5 cells wide regardless of
// style, so switching an edge to bypass never moves a box.
//
// The forward shaft is an EM DASH (U+2014), not the box-drawing light
// horizontal (U+2500) it reads like it should be. Terminals that draw box
// characters themselves rather than taking them from the font -- Ghostty and
// Kitty both do, for crisp line art -- substitute their own hairline for
// U+2500, positioned on the CELL's center. The ▶ head comes from the font and
// sits on the font's center, and in JetBrains Mono those two centers are ~3.5px
// apart at demo font size: the shaft visibly met the head above its point,
// while the synthesized hairline was a third the head's weight.
//
// U+2014 has no box-drawing meaning, so no terminal substitutes for it. It
// comes from the same font as the head, which puts the two within a pixel of
// each other, and it tiles seamlessly into the head with no gap. This is also
// why the bypass shaft (U+254C) always looked right: that one, unlike U+2500,
// was already being drawn from the font.
func renderArrow(bypass bool) string {
	if bypass {
		return arrowBypassStyle.Render(" ╌╌▶ ")
	}
	return arrowForwardStyle.Render(" ——▶ ")
}

// RenderDiagram draws the full persistent flow: a top callout showing what
// feeds SELECT, the node row itself, and (once reached) the dashed reuse loop
// from RECORD back into SELECT -- the same two annotation shapes as the
// reference sketch (a labelled arrow feeding a box from above, a labelled
// dashed loop feeding back into an earlier box from below).
func RenderDiagram(s DiagramState) string {
	boxes, arrows, centers, rowWidth := nodeGeometry(s)

	joinArgs := make([]string, 0, int(numStages)*2-1)
	for st := Stage(0); st < numStages; st++ {
		if st > 0 {
			joinArgs = append(joinArgs, arrows[st-1])
		}
		joinArgs = append(joinArgs, boxes[st])
	}
	row := lipgloss.JoinHorizontal(lipgloss.Center, joinArgs...)

	var out strings.Builder
	out.WriteString(annotations(centers[StageSelect], centers[StageRecord], rowWidth, s))
	out.WriteString(row)
	out.WriteString("\n")
	if s.ShowReuseLoop {
		out.WriteString(reuseLoop(centers[StageSelect], centers[StageRecord], s.ReuseActive))
	}
	return out.String()
}

// nodeGeometry computes the node row: its boxes, the arrows between them, each
// box's center column, and the row's total width.
//
// Split out because two callers need the same numbers. RenderDiagram draws the
// row and positions the annotations over it, and Model.View needs the row's
// width to CENTER the block on -- see DiagramRowWidth.
func nodeGeometry(s DiagramState) (boxes []string, arrows []string, centers []int, rowWidth int) {
	boxes = make([]string, numStages)
	widths := make([]int, numStages)
	for st := Stage(0); st < numStages; st++ {
		boxes[st] = renderNode(stageLabel[st], s.Statuses[st], s.Spinner)
		widths[st] = lipgloss.Width(boxes[st])
	}

	arrows = make([]string, numStages-1)
	arrowWidths := make([]int, numStages-1)
	for i := 0; i < int(numStages)-1; i++ {
		// Only one bypass edge in this pipeline: EXTRACT -> SELECT.
		arrows[i] = renderArrow(s.Bypass && Stage(i) == StageExtract)
		arrowWidths[i] = lipgloss.Width(arrows[i])
	}

	centers = make([]int, numStages)
	x := 0
	for st := Stage(0); st < numStages; st++ {
		if st > 0 {
			x += arrowWidths[st-1]
		}
		centers[st] = x + widths[st]/2
		x += widths[st]
	}
	return boxes, arrows, centers, x
}

// DiagramRowWidth is the width of the node row alone, ignoring annotations.
//
// This is what the block must be centered on. Centering on the widest LINE --
// which is what a generic block-centering helper does -- means the evidence
// fork, the one annotation that overhangs the row, drags every box left by half
// its overhang the moment it appears. The row is the thing that has to hold
// still: it is on screen for the entire demo, and the fork arrives at the last
// step of it.
func DiagramRowWidth(s DiagramState) int {
	_, _, _, w := nodeGeometry(s)
	return w
}

func fill(n int) string {
	if n <= 0 {
		return ""
	}
	return fillStyle.Render(strings.Repeat(" ", n))
}

// calloutStyle is green while the graph feeds SELECT and gray while a beat
// bypasses it. Split out so the choice is assertable directly, rather than by
// pattern-matching escape codes out of rendered output.
func calloutStyle(dim bool) lipgloss.Style {
	if dim {
		return labelSkippedStyle
	}
	return labelFeedStyle
}

// annotationRow places styled segments at absolute columns on one line.
//
// Two annotations share these rows now -- the graph callout over SELECT and the
// evidence fork over RECORD -- and each knows only its own column. Placing by
// column rather than by concatenation is what lets them be written
// independently; a segment asking for a column already used is pushed right
// rather than overlapping, so the worst case on a cramped row is two labels
// touching, never one drawn through the other.
type annotationRow struct {
	b   strings.Builder
	col int
}

func (r *annotationRow) place(col int, text string, style lipgloss.Style) {
	if col < r.col {
		col = r.col
	}
	r.b.WriteString(fill(col - r.col))
	r.b.WriteString(style.Render(text))
	r.col = col + lipgloss.Width(text)
}

func (r *annotationRow) String() string { return r.b.String() + "\n" }

// annotations draws the two rows above the node row: what feeds SELECT, and
// (once the final step reaches it) where RECORD's outcome goes.
//
//	DERIVED GRAPH (Neo4j)             Neo4j ◀——┬╌╌▶ JFrog Evidence
//	          ▼                                 ▲
//
// The asymmetry in that fork is the argument, not decoration. The left arm is
// solid: the graph was genuinely written, and it holds the mutable, cross-run,
// analytical view. The right arm is DASHED because the predicate is emitted and
// not uploaded -- signing needs a subject in Artifactory and a key, so the demo
// stops at the document. A solid arrow there would claim an attestation this
// repo does not make.
func annotations(selectCenter, recordCenter, rowWidth int, s DiagramState) string {
	const graphLabel = "DERIVED GRAPH (Neo4j)"
	graph := calloutStyle(s.Bypass)

	labels := &annotationRow{}
	arrows := &annotationRow{}

	labelStart := selectCenter - len(graphLabel)/2
	if labelStart < 0 {
		labelStart = 0
	}
	labels.place(labelStart, graphLabel, graph.Bold(true))
	arrows.place(selectCenter, "▼", graph)

	if s.ShowEvidenceFork {
		rightArm := evidenceArm(recordCenter, rowWidth, s.MaxWidth)
		jfrog := evidenceArmStyle(s.EvidenceEmitted)

		// The arms use the SAME glyphs as the row's edges, for the same reasons
		// renderArrow documents at length: an em-dash shaft (U+2014) comes from the
		// font, so it meets the ▶/◀ head on the head's own centre line, while the
		// box-drawing horizontal (U+2500) is synthesized by Ghostty and Kitty at
		// the cell's centre -- visibly thinner and a few pixels off. The first
		// version of this fork used U+2500 and looked like a different diagram.
		start := recordCenter - lipgloss.Width(forkLabel) - lipgloss.Width(forkLeftArm)
		if start < 0 {
			start = 0
		}
		labels.place(start, forkLabel, labelFeedStyle)
		labels.place(start+lipgloss.Width(forkLabel), forkLeftArm, arrowForwardStyle)
		labels.place(recordCenter, "┬", arrowForwardStyle)
		labels.place(recordCenter+1, rightArm, jfrog)
		arrows.place(recordCenter, "▲", arrowForwardStyle)
	}

	return labels.String() + arrows.String()
}

// The fork's two arms, in the same glyph vocabulary as renderArrow: an em-dash
// shaft for a real edge, U+254C dashes for one that is not taken. Both are two
// shaft cells plus a head, exactly like the row's ` ——▶ ` and ` ╌╌▶ `, so the
// annotation reads as part of the same drawing rather than as a second style.
const (
	forkLabel   = "Neo4j "
	forkLeftArm = "◀——"
	forkFull    = "╌╌▶ JFrog Evidence"
	forkShort   = "╌╌▶ JFrog"
)

// evidenceArm is the fork's right-hand label, shortened when the full one would
// push the diagram past the terminal. "JFrog" alone still names the destination;
// the transcript below carries the rest.
//
// The bound accounts for the centering pad, because that is where the label
// actually lands: Model.View centers the block on the NODE ROW, so the fork's
// right end sits at pad + recordCenter + 1 + width. Measuring from column zero
// instead said the full label fit on terminals where it ran off the edge.
func evidenceArm(recordCenter, rowWidth, maxWidth int) string {
	if maxWidth <= 0 {
		return forkFull
	}
	pad := (maxWidth - rowWidth) / 2
	if pad < 0 {
		pad = 0
	}
	if pad+recordCenter+1+lipgloss.Width(forkFull) > maxWidth {
		return forkShort
	}
	return forkFull
}

// evidenceArmStyle is dim while the predicate is still being read out of the
// graph and magenta once it exists -- the same "no claim before its evidence"
// rule the reuse loop follows. Magenta rather than green so the two arms do not
// read as the same kind of store: one is a database this pipeline writes, the
// other is a signed document it hands off.
func evidenceArmStyle(emitted bool) lipgloss.Style {
	if emitted {
		return lipgloss.NewStyle().Foreground(colorMagenta).Background(colorBG)
	}
	return lipgloss.NewStyle().Foreground(colorPurple).Background(colorBG)
}

// reuseLoop is the "same content, skip the work" feedback path: verticals
// dropping from RECORD and rising into SELECT, joined by a dashed run below
// the row, with a label centered under the span.
func reuseLoop(selectCenter, recordCenter int, active bool) string {
	style := labelFeedStyle
	if !active {
		style = lipgloss.NewStyle().Foreground(colorPurple).Background(colorBG)
	}
	left, right := selectCenter, recordCenter
	if left > right {
		left, right = right, left
	}

	var verticals strings.Builder
	verticals.WriteString(fill(left))
	verticals.WriteString(style.Render("╎"))
	verticals.WriteString(fill(right - left - 1))
	verticals.WriteString(style.Render("╎"))
	verticals.WriteString("\n")

	var dashes strings.Builder
	dashes.WriteString(fill(left))
	dashes.WriteString(style.Render("╰" + strings.Repeat("╌", max(0, right-left-1)) + "╯"))
	dashes.WriteString("\n")

	const label = "reuse: same content hash -> skip"
	mid := (left + right) / 2
	labelStart := mid - len(label)/2
	if labelStart < 0 {
		labelStart = 0
	}
	var labelLine strings.Builder
	labelLine.WriteString(fill(labelStart))
	labelLine.WriteString(style.Render(label))
	labelLine.WriteString("\n")

	return verticals.String() + dashes.String() + labelLine.String()
}
