package main

import "github.com/charmbracelet/lipgloss"

// Neon-on-black palette. Every color is a true-color hex so it renders the same
// on any terminal that supports 24-bit color, rather than drifting through a
// 256-color approximation.
const (
	colorBG      = lipgloss.Color("#0a0a0f")
	colorCyan    = lipgloss.Color("#00f5ff")
	colorGreen   = lipgloss.Color("#39ff14")
	colorMagenta = lipgloss.Color("#ff2fe0")
	colorPurple  = lipgloss.Color("#b026ff")
	colorDim     = lipgloss.Color("#4a4a5a")
	colorWhite   = lipgloss.Color("#f0f0ff")
	colorRed     = lipgloss.Color("#ff3860")
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorCyan).
			Background(colorBG)

	beatStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorMagenta).
			Background(colorBG)

	noteStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Background(colorBG)

	dimStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Background(colorBG)

	successStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorGreen).
			Background(colorBG)

	linkStyle = lipgloss.NewStyle().
			Underline(true).
			Foreground(colorCyan).
			Background(colorBG)

	failStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorRed).
			Background(colorBG)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Background(colorBG).
			Padding(0, 1)

	// nodeActiveStyle is the box style for the diagram node currently executing:
	// bright cyan border with a bold label, the "in progress" neon glow.
	nodeActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorCyan).
			Background(colorBG).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorCyan).
			Padding(0, 1)

	// nodeDoneStyle marks a stage already completed this beat: green border,
	// dimmer than active so the eye still finds the active node first.
	nodeDoneStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorGreen).
			Background(colorBG).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorGreen).
			Padding(0, 1)

	// nodePendingStyle is a stage not yet reached: dim purple, present but quiet.
	nodePendingStyle = lipgloss.NewStyle().
				Foreground(colorPurple).
				Background(colorBG).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorPurple).
				Padding(0, 1)

	// nodeSkippedStyle marks a stage the current beat deliberately bypasses
	// (e.g. naive CI has no SELECT step): dim gray, struck-through label.
	nodeSkippedStyle = lipgloss.NewStyle().
				Foreground(colorDim).
				Background(colorBG).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorDim).
				Padding(0, 1)

	fillStyle = lipgloss.NewStyle().Background(colorBG)

	// Deliberately NOT bold: the bold face draws the em-dash shaft 1px lower
	// relative to the ▶ head than the regular face does, re-introducing exactly
	// the off-center join renderArrow's glyph choice exists to remove. Regular
	// weight already renders the shaft ~3x thicker than the hairline this
	// replaced, so nothing is lost by dropping it.
	arrowForwardStyle = lipgloss.NewStyle().Foreground(colorGreen).Background(colorBG)

	// arrowBypassStyle is GRAY, not magenta, so the diagram speaks one colour
	// language: gray means "not part of this beat". The bypassed edge now
	// matches the two boxes it spans (nodeSkippedStyle) and the grayed-out
	// graph callout above them, instead of magenta drawing the eye to the one
	// thing beat 1 is claiming does NOT happen.
	arrowBypassStyle = lipgloss.NewStyle().Foreground(colorDim).Background(colorBG)

	labelFeedStyle = lipgloss.NewStyle().Foreground(colorGreen).Background(colorBG)

	// labelSkippedStyle grays the "DERIVED GRAPH (Neo4j)" callout and its arrow
	// for a beat that never consults the graph.
	labelSkippedStyle = lipgloss.NewStyle().Foreground(colorDim).Background(colorBG)

	// evidenceStyle marks the JFrog arm of the RECORD fork. Magenta rather than
	// the green used for graph writes, deliberately: the two destinations are not
	// the same kind of thing. Green is a database this pipeline writes and reads
	// back; magenta is a signed document it hands to someone who does not trust
	// it. Colouring them alike would flatten exactly the distinction the step
	// exists to make.
	evidenceStyle = lipgloss.NewStyle().Foreground(colorMagenta).Background(colorBG)
)

// overlayStyle frames the Cypher panel. A cyan border because the panel is a
// read of the graph rather than a stage of the pipeline, so it should not read as
// one of the diagram's nodes.
var overlayStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colorCyan).
	Background(colorBG).
	Padding(0, 1)

// cypherStyle is the query text itself: plain, high contrast, no attempt at
// syntax colouring. The queries already carry Cypher's own shape, and inventing
// a highlighting scheme here would be one more thing to keep true.
var cypherStyle = lipgloss.NewStyle().Foreground(colorGreen).Background(colorBG)
