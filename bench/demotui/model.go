package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Phase is one step of the fixed four-beat script. The order here is the
// order bench/demo.sh runs them in; see beats.go for what each one does.
type Phase int

const (
	phaseIntro Phase = iota
	phaseDocsEdit
	phaseBeat1List
	phaseBeat1Run
	phaseBeat2Select
	phaseBeat2Run
	phaseCoreEdit
	phaseBeat3Select
	phaseBeat3Run
	phaseBeat3Record
	phaseBeat4Select
	phaseBeat4Record
	phaseEvidence
	phaseDone
)

// totalSteps is how many phases do work, i.e. every value up to and including
// phaseEvidence -- phaseDone only prints the summary.
//
// The counter says "step", not "beat", because there are more of these than
// there are BEATs: two steps set up an edit, each beat spans a select and a run,
// and the evidence step belongs to no beat at all. "beat 3/10" sat on screen
// directly under a heading reading "BEAT 1" and invited the audience to
// reconcile two numbers that were never the same count.
const totalSteps = int(phaseDone)

type Model struct {
	e *env

	phase   Phase
	diagram DiagramState

	changedDocs, changedCore string
	headSHA                  string
	lastTraceURL             string // most recent Dagger Cloud trace link seen; "w" opens it

	busy      bool
	busyLabel string
	busyStart time.Time
	spinner   spinner.Model

	transcript []string
	viewport   viewport.Model

	// anchor is the transcript line of the most recent section heading, and
	// stepAnchor the first line the current step wrote. The viewport scrolls one
	// of them to the top; see syncViewport for which and why. The transcript
	// itself stays ONE continuous scrollback -- nothing is ever cleared, ↑/↓
	// still reaches the whole demo -- so this only ever changes where the window
	// sits, never what it can reach.
	anchor     int
	stepAnchor int

	// sectionHead is how many rows the beat's OPENING step wrote -- its heading,
	// the commit line, and the diff. When the beat outgrows the screen those rows
	// are pinned above the scrolling area instead of scrolling away, so the change
	// under discussion is on screen for the whole beat. pinned is how many are
	// currently pinned (0 when the whole beat fits and no pinning is needed).
	sectionHead int
	pinned      int
	vpFull      int // rows available to transcript+pinned block together

	// The overlay: one panel, two modes. Its own viewport, so scrolling the panel
	// never touches the transcript's position or its anchors.
	cypher      []CypherQuery
	cypherErr   error
	evidence    string // the predicate the final step generated, verbatim
	evidenceCmd string // and the jf command that would upload it
	overlayOpen bool
	overlayMode overlayMode
	overlayVP   viewport.Model

	waiting  bool
	fatalErr error
	quitting bool

	width, height int
}

func NewModel(e *env) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorCyan).Background(colorBG)
	vp := viewport.New(80, 20)
	return Model{e: e, spinner: sp, viewport: vp, overlayVP: viewport.New(76, 18)}
}

func (m Model) Init() tea.Cmd {
	m2, cmd := m.startPhase(phaseIntro)
	return tea.Batch(cmd, m2.spinner.Tick, waitForTrace(m.e))
}

// busyLabels names each phase for the header while its Cmd is in flight.
var busyLabels = map[Phase]string{
	phaseIntro:       "cloning throwaway workspace",
	phaseDocsEdit:    "editing + committing docs typo",
	phaseBeat1List:   "listing the hardcoded naive target set",
	phaseBeat1Run:    "naive CI: running ALL targets",
	phaseBeat2Select: "extracting graph + resolving selection",
	phaseBeat2Run:    "graph-selected: running docs lint",
	phaseCoreEdit:    "editing + committing shared-lib change",
	phaseBeat3Select: "extracting graph + resolving selection",
	phaseBeat3Run:    "graph-selected: running 4 targets",
	phaseBeat3Record: "recording the run and its selection",
	phaseBeat4Select: "re-resolving selection on identical content",
	phaseBeat4Record: "recording the skip, citing what proved it",
	phaseEvidence:    "reading the run back as a JFrog Evidence predicate",
}

// startPhase sets the diagram's state for entering phase p and returns the Cmd
// that performs its work. Diagram updates happen here, BEFORE the work runs,
// so the active node lights up the instant the user advances rather than only
// once the (possibly slow) Cmd resolves.
func (m Model) startPhase(p Phase) (Model, tea.Cmd) {
	m.phase = p
	m.waiting = false

	// Where this step's output will begin, recorded before any of it is written.
	// syncViewport falls back to this when the whole beat no longer fits on
	// screen, so every step still opens at a top rather than mid-scroll.
	m.stepAnchor = len(m.transcript)

	// A new run means a new trace. Clearing here rather than letting the old link
	// linger is what keeps "w" from opening the PREVIOUS run's trace during the
	// second or so before this one publishes its own -- a wrong tab that looks
	// exactly like a right one, since the traces differ only by id. The hint in
	// the status line goes dark with it and comes back when the new link lands,
	// so the display never claims a trace it cannot open.
	if startsDaggerRun(p) {
		m.lastTraceURL = ""
		m.e.resetTrace()
	}

	switch p {
	case phaseIntro:
		m.diagram = DiagramState{}
	case phaseDocsEdit:
		m.diagram.Statuses[StageEdit] = StatusActive
	case phaseBeat1List:
		m.diagram.Statuses[StageExtract] = StatusSkipped
		m.diagram.Statuses[StageSelect] = StatusSkipped
		m.diagram.Bypass = true
	case phaseBeat1Run:
		m.diagram.Statuses[StageRun] = StatusActive
	case phaseBeat2Select:
		m.diagram.Bypass = false
		m.diagram.Statuses[StageExtract] = StatusActive
		m.diagram.Statuses[StageSelect] = StatusPending
		m.diagram.Statuses[StageRun] = StatusPending
	case phaseBeat2Run:
		m.diagram.Statuses[StageSelect] = StatusDone
		m.diagram.Statuses[StageRun] = StatusActive
	case phaseCoreEdit:
		m.diagram = DiagramState{}
		m.diagram.Statuses[StageEdit] = StatusActive
	case phaseBeat3Select:
		m.diagram.Statuses[StageExtract] = StatusActive
	case phaseBeat3Run:
		m.diagram.Statuses[StageSelect] = StatusDone
		m.diagram.Statuses[StageRun] = StatusActive
		m.diagram.Statuses[StageRecord] = StatusPending
	case phaseBeat3Record:
		m.diagram.Statuses[StageRun] = StatusDone
		m.diagram.Statuses[StageRecord] = StatusActive
	case phaseBeat4Select:
		m.diagram.Statuses[StageSelect] = StatusActive
		// The reuse loop appears HERE, with the beat that is about to demonstrate
		// it -- not at the end of beat 3, where it used to. Beat 3 records the
		// runs that make reuse possible, but nothing has been skipped yet at that
		// point, so drawing the feedback path there put a claim on screen a beat
		// before its evidence. It arrives dim, then goes green (ReuseActive) when
		// the selection comes back with nothing to run.
		m.diagram.ShowReuseLoop = true
	case phaseBeat4Record:
		// SELECT completes here, the same way RUN completes when RECORD starts:
		// in this diagram a stage stays lit while the transcript below is showing
		// its result, and the NEXT stage's start is what marks it done. Beat 4 was
		// the one place with no next stage to do that, so SELECT sat cyan --
		// reading as still executing -- through the end of the demo.
		m.diagram.Statuses[StageSelect] = StatusDone
		m.diagram.Statuses[StageRecord] = StatusActive
	case phaseEvidence:
		// RECORD stays Done and lit green: this step does not re-record anything,
		// it reads the run back out. What changes is the fork above it, which
		// appears now -- with the step that produces a predicate -- rather than at
		// the first RECORD, where it would have promised a second destination
		// three beats before anything went there.
		m.diagram.ShowEvidenceFork = true
	case phaseDone:
		m.busy = false
		m.appendTranscript(doneLines(m.e)...)
		m.waiting = true
		m.resizeViewport()
		return m, nil
	}

	var cmd tea.Cmd
	switch p {
	case phaseIntro:
		cmd = cmdIntro(m.e)
	case phaseDocsEdit:
		cmd = cmdDocsEdit(m.e)
	case phaseBeat1List:
		cmd = cmdBeat1List(m.e)
	case phaseBeat1Run:
		cmd = cmdBeat1Run(m.e)
	case phaseBeat2Select:
		cmd = cmdBeat2Select(m.e, m.headSHA, m.changedDocs)
	case phaseBeat2Run:
		cmd = cmdBeat2Run(m.e)
	case phaseCoreEdit:
		cmd = cmdCoreEdit(m.e)
	case phaseBeat3Select:
		cmd = cmdBeat3Select(m.e, m.changedCore)
	case phaseBeat3Run:
		cmd = cmdBeat3Run(m.e, m.headSHA)
	case phaseBeat3Record:
		cmd = cmdBeat3Record(m.e, m.headSHA)
	case phaseBeat4Select:
		cmd = cmdBeat4Select(m.e, m.headSHA, m.changedCore)
	case phaseBeat4Record:
		cmd = cmdBeat4Record(m.e, m.headSHA)
	case phaseEvidence:
		cmd = cmdEvidence(m.e)
	}

	m.busy = true
	m.busyLabel = busyLabels[p]
	m.busyStart = time.Now()
	m.resizeViewport()
	return m, atLeast(m.e.stageDwell, cmd)
}

// atLeast keeps a step on screen for a minimum duration, so the diagram stage it
// lights up is visibly visited rather than blinking past.
//
// Several steps are near-instant -- committing a one-line edit, resolving a
// selection, writing a record -- and at real speed their stage lit and cleared
// inside a single frame, which reads as the pipeline SKIPPING that stage rather
// than performing it. The wait happens inside the Cmd, which Bubble Tea runs off
// the event loop, so the UI stays live throughout: the spinner keeps turning in
// the active node and keys still work.
//
// A floor, never a delay: a step that already took longer than d returns the
// moment it is done. Zero disables it, which is what the tests run with.
func atLeast(d time.Duration, cmd tea.Cmd) tea.Cmd {
	if cmd == nil || d <= 0 {
		return cmd
	}
	return func() tea.Msg {
		start := time.Now()
		msg := cmd()
		if remaining := d - time.Since(start); remaining > 0 {
			time.Sleep(remaining)
		}
		return msg
	}
}

// nextPhase is the fixed script order. phaseBeat4Select has no successor that
// waits for input -- bench/demo.sh does not pause between the reuse beat and
// the closing summary, because that story needs no further evidence once beat
// 4's zero-targets result has printed.
func nextPhase(p Phase) (Phase, bool) {
	switch p {
	case phaseIntro:
		return phaseDocsEdit, true
	case phaseDocsEdit:
		return phaseBeat1List, true
	case phaseBeat1List:
		return phaseBeat1Run, true
	case phaseBeat1Run:
		return phaseBeat2Select, true
	case phaseBeat2Select:
		return phaseBeat2Run, true
	case phaseBeat2Run:
		return phaseCoreEdit, true
	case phaseCoreEdit:
		return phaseBeat3Select, true
	case phaseBeat3Select:
		return phaseBeat3Run, true
	case phaseBeat3Run:
		return phaseBeat3Record, true
	case phaseBeat3Record:
		return phaseBeat4Select, true
	case phaseBeat4Select:
		return phaseBeat4Record, true
	case phaseBeat4Record:
		return phaseEvidence, true
	case phaseEvidence:
		// Was: beat 4's selection ran straight into the closing summary with no
		// hold, so the demo's final RECORD lit and vanished in the same breath.
		// The summary is now a step the presenter advances to like any other.
		return phaseDone, true
	default:
		return phaseDone, false
	}
}

// startsDaggerRun reports whether entering this phase kicks off a Dagger run
// that will publish its own trace URL.
//
// Only the three runs the demo is about. phaseBeat1List also calls dagger, but
// as a metadata lookup that never reports a trace, and the record steps talk to
// Neo4j rather than Dagger -- clearing on those would blank a link the presenter
// is still discussing while the beat's results are on screen.
func startsDaggerRun(p Phase) bool {
	switch p {
	case phaseBeat1Run, phaseBeat2Run, phaseBeat3Run:
		return true
	}
	return false
}

// isSectionHeading reports whether a transcript line opens a new section, which
// is the same thing as "should be at the top of the screen from here on".
//
// Derived from the line itself rather than from a list of phases kept in this
// file: the headings live in beats.go, they have already moved once (BEAT 1 used
// to print two steps later than it does now), and a duplicated list of which
// phase owns which heading is a list that goes stale silently. Both markers are
// section openers -- "BEAT n" for the four beats, "==> " for the clone and the
// closing summary -- and nothing else in the transcript uses either, since the
// per-PR lines are notes now.
func isSectionHeading(line string) bool {
	plain := stripANSI(line)
	return strings.HasPrefix(plain, "BEAT ") || strings.HasPrefix(plain, "==> ")
}

// appendTranscript stores one SCREEN ROW per element, splitting any entry that
// carries several lines.
//
// The normalisation is what makes every anchor here correct. A single appended
// entry can render as several rows -- coloredDiff hands back a whole hunk as one
// string -- while anchor, stepAnchor, and syncViewport's padding all count rows.
// Storing that hunk as one element put every later anchor two rows short, so
// beat 2 opened with the last two lines of beat 1 still above its heading: the
// off-by-N was invisible in the index and only showed up on screen.
func (m *Model) appendTranscript(lines ...string) {
	opened := -1
	for _, entry := range lines {
		for _, row := range strings.Split(entry, "\n") {
			if isSectionHeading(row) {
				m.anchor = len(m.transcript)
				opened = m.anchor
			}
			m.transcript = append(m.transcript, row)
		}
	}
	// A beat's head block is everything its opening step wrote: the heading, the
	// commit line, the diff. Captured here rather than guessed at by scanning for
	// blank lines, because only the caller knows where one step's output ends.
	if opened >= 0 {
		m.sectionHead = len(m.transcript) - opened
	}
	m.syncViewport()
}

// syncViewport rebuilds the viewport's content and scroll position from the
// transcript. Both together, because the two are coupled: reaching an anchor
// depends on there being enough content below it to scroll against.
//
// Three tiers, most-context-first:
//
//  1. the current BEAT's heading, whenever the whole beat still fits on screen
//  2. failing that, the first line of the current STEP
//  3. failing that, the newest line
//
// Tier 2 exists because tier 1 cannot always hold: beats 1 and 3 run 29 and 23
// transcript lines against roughly 22 rows on a 30-row terminal, so pinning
// their headings would push the run results they exist to show off the bottom.
// Falling straight from there to tier 3 was worse than it sounds -- it put the
// audience mid-list, top row reading "apps/admin", which is the same
// scrolled-mid-stream clutter the anchor was added to remove. Anchoring the
// STEP instead keeps every screen starting at a beginning: the beat's, when the
// beat fits, and otherwise the step's.
func (m *Model) syncViewport() {
	lines := m.transcript

	// Pin the beat's head block once the beat outgrows the screen.
	//
	// Without this, a long beat falls back to showing only the current step, and
	// the change the beat is ABOUT scrolls off the top exactly when its results
	// arrive -- which is when it is being explained. Beat 3 crossed that line the
	// day RECORD became its own step: 25 rows against a 23-row viewport, and the
	// diff vanished. Trimming content buys a row or two and breaks again on the
	// next terminal; pinning removes the failure mode instead of postponing it.
	//
	// Only when the head block leaves room to actually read the tail, and never
	// when the whole beat already fits (the anchor handles that case better,
	// since it keeps the beat contiguous).
	full := m.vpFull
	if full <= 0 {
		full = m.viewport.Height
	}
	m.pinned = 0
	if m.anchor > 0 && m.sectionHead > 0 &&
		len(m.transcript)-m.anchor > full && m.sectionHead+3 <= full &&
		m.anchor+m.sectionHead <= len(m.transcript) {
		m.pinned = m.sectionHead
	}
	m.viewport.Height = full - m.pinned

	// from < len(transcript) is the guard against anchoring to a line that does
	// not exist yet: startPhase records stepAnchor BEFORE the step writes
	// anything, and a step's Cmd can be a twenty-second Dagger call. Anchoring
	// there would blank the transcript for the whole run instead of leaving the
	// previous output up to talk over.
	fits := func(from int) bool {
		return from > 0 && from < len(m.transcript) &&
			len(m.transcript)-from <= m.viewport.Height
	}
	anchor, anchored := 0, false
	switch {
	case fits(m.anchor):
		anchor, anchored = m.anchor, true
	case fits(m.stepAnchor):
		anchor, anchored = m.stepAnchor, true
	}

	if anchored {
		// SetYOffset clamps to len(lines)-Height, so a section shorter than the
		// viewport cannot be scrolled to the top at all: the clamp holds the
		// previous section's tail on screen, which is the exact distraction the
		// anchor exists to remove. These trailing blanks are what the viewport
		// scrolls against, and they sit below the newest line where there is
		// nothing to read anyway.
		if pad := anchor + m.viewport.Height - len(lines); pad > 0 {
			lines = append(append(make([]string, 0, len(lines)+pad), lines...),
				make([]string, pad)...)
		}
	}

	m.viewport.SetContent(strings.Join(lines, "\n"))
	if anchored {
		m.viewport.SetYOffset(anchor)
	} else {
		m.viewport.GotoBottom()
	}
}

// resizeViewport fills whatever room is left below the diagram (its height
// varies -- the reuse loop adds three lines once it appears) down to the
// bottom status line. The diagram itself stays pinned at the top always; only
// the viewport grows or shrinks with the window and with the diagram's height.
func (m *Model) resizeViewport() {
	if m.height == 0 {
		return // no WindowSizeMsg yet
	}
	// The fork's label is the one annotation that can be wider than the node row,
	// so the diagram is told the terminal width and shortens it rather than
	// pushing every box off-centre. Set before measuring: the height below has to
	// be the height of the same frame View will draw.
	m.diagram.MaxWidth = m.width
	diagramH := lipgloss.Height(RenderDiagram(m.diagram))
	vpH := m.height - diagramH - 2 // 1 blank separator + 1 bottom status line
	if vpH < 3 {
		vpH = 3
	}
	m.viewport.Width = m.width
	// vpFull, not viewport.Height: these rows are shared between the transcript
	// and any pinned head block, and syncViewport divides them. Writing the
	// viewport's height here instead would make it the input to its own next
	// calculation, so every pinned frame would shrink it again -- a viewport that
	// loses rows the longer the demo runs.
	m.vpFull = vpH
	m.viewport.Height = vpH
	m.syncOverlay()
	// Height feeds the padding, the fits-in-view test, and how many rows a pinned
	// head block may take, so the scroll position is recomputed here rather than
	// just restored to the bottom.
	m.syncViewport()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeViewport()
		return m, nil

	case tea.KeyMsg:
		// While the panel is up it owns the keyboard, apart from quitting. In
		// particular Enter must NOT advance: the presenter is talking over a query
		// with the panel covering the transcript, and a step that ran behind it
		// would be missed entirely.
		if m.overlayOpen {
			switch msg.String() {
			case "ctrl+c", "q":
				m.quitting = true
				return m, tea.Quit
			case "c", "e", "esc", "enter", " ":
				// Both openers close, whichever mode is up. A presenter who hits "e"
				// on the evidence panel means "put this away", and leaving it open
				// because the key does not match the current mode reads as the demo
				// having hung.
				m.overlayOpen = false
				return m, nil
			default:
				var cmd tea.Cmd
				m.overlayVP, cmd = m.overlayVP.Update(msg)
				return m, cmd
			}
		}

		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "c":
			m.overlayOpen, m.overlayMode = true, modeCypher
			m.syncOverlay()
			return m, nil
		case "e":
			// Nothing to show until the final step has generated one. Opening an
			// empty panel would advertise a feature by covering the transcript with
			// a blank box.
			if m.evidence == "" {
				return m, nil
			}
			m.overlayOpen, m.overlayMode = true, modeEvidence
			m.syncOverlay()
			return m, nil
		case "enter", " ":
			return m.advance()
		case "w":
			if m.lastTraceURL == "" {
				return m, nil
			}
			url := m.lastTraceURL
			return m, func() tea.Msg { _ = openURL(url); return nil }
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case traceURLMsg:
		// Arrives while the run is still in flight. Re-arm for the next run.
		m.lastTraceURL = string(msg)
		return m, waitForTrace(m.e)

	case autoAdvanceMsg:
		return m.advance()

	case stepMsg:
		return m.handleStep(msg)
	}

	return m, nil
}

// autoAdvanceDelay is how long a --no-pause run lingers on a finished beat
// before moving on -- long enough to read the headline number, short enough
// that the demo does not need a hand on the keyboard at all.
const autoAdvanceDelay = 1200 * time.Millisecond

type autoAdvanceMsg struct{}

// traceURLMsg carries a Dagger Cloud link that arrived MID-RUN, from the
// traceWatcher on the running dagger process's output.
type traceURLMsg string

// waitForTrace parks on the channel until a run publishes its trace URL. It is
// re-armed after every delivery, so each of the demo's three runs lights up "w"
// as soon as its own link appears rather than when the run ends.
func waitForTrace(e *env) tea.Cmd {
	if e == nil || e.traceCh == nil {
		return nil
	}
	return func() tea.Msg { return traceURLMsg(<-e.traceCh) }
}

func autoAdvanceCmd() tea.Cmd {
	return tea.Tick(autoAdvanceDelay, func(time.Time) tea.Msg { return autoAdvanceMsg{} })
}

// advance is the one path from "waiting for the next beat" to "running it" --
// shared by an explicit Enter/Space and, under --no-pause, the timer that
// fires in its place. It is a no-op unless the model is actually idle, so a
// stray keypress or a delayed timer message can never double-advance.
func (m Model) advance() (tea.Model, tea.Cmd) {
	if !m.waiting || m.busy {
		return m, nil
	}
	if m.phase == phaseDone {
		m.quitting = true
		return m, tea.Quit
	}
	np, ok := nextPhase(m.phase)
	if !ok {
		m.quitting = true
		return m, tea.Quit
	}
	m2, cmd := m.startPhase(np)
	return m2, tea.Batch(cmd, m2.spinner.Tick)
}

func (m Model) handleStep(msg stepMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	m.appendTranscript(msg.lines...)

	if msg.err != nil {
		m.appendTranscript(failStyle.Render("    error: " + msg.err.Error()))
		m.waiting = true
		m.resizeViewport()
		return m, nil
	}

	if msg.traceURL != "" {
		m.lastTraceURL = msg.traceURL
	}

	if msg.queries != nil || msg.queriesErr != nil {
		m.cypher, m.cypherErr = msg.queries, msg.queriesErr
	}

	if msg.evidence != "" {
		m.evidence, m.evidenceCmd = msg.evidence, msg.evidenceCmd
	}

	switch m.phase {
	case phaseIntro:
		m.headSHA = msg.sha
	case phaseDocsEdit:
		m.headSHA = msg.sha
		m.changedDocs = msg.changedCSV
		m.diagram.Statuses[StageEdit] = StatusDone
	case phaseBeat1Run:
		m.diagram.Statuses[StageRun] = StatusDone
	case phaseBeat2Select:
		m.diagram.Statuses[StageExtract] = StatusDone
		m.diagram.Statuses[StageSelect] = StatusActive
	case phaseBeat2Run:
		m.diagram.Statuses[StageRun] = StatusDone
	case phaseCoreEdit:
		m.headSHA = msg.sha
		m.changedCore = msg.changedCSV
		m.diagram.Statuses[StageEdit] = StatusDone
	case phaseBeat3Select:
		m.diagram.Statuses[StageExtract] = StatusDone
		m.diagram.Statuses[StageSelect] = StatusActive
	case phaseBeat3Run:
		m.diagram.Statuses[StageRun] = StatusDone
	case phaseBeat3Record:
		m.diagram.Statuses[StageRecord] = StatusDone
	case phaseBeat4Select:
		m.diagram.ReuseActive = true
	case phaseBeat4Record:
		m.diagram.Statuses[StageRecord] = StatusDone
	case phaseEvidence:
		m.diagram.EvidenceEmitted = true
	}

	m.waiting = true
	m.resizeViewport()
	if m.e.noPause {
		return m, autoAdvanceCmd()
	}
	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	// The spinner is handed to the diagram only while a Cmd is actually in
	// flight, and stripped of its own styling so the ACTIVE NODE's colour wins
	// -- the frame is drawn inside that box, so it has to read as part of the
	// box rather than as the cyan status line leaking upward.
	d := m.diagram
	if m.busy {
		d.Spinner = stripANSI(m.spinner.View())
	}
	header := centerBlock(RenderDiagram(d), m.width, DiagramRowWidth(d))

	// The panel takes the transcript's slot, exactly its size, so nothing below
	// it moves. See cypherPanel.
	body := m.viewport.View()
	if m.pinned > 0 {
		body = strings.Join(m.transcript[m.anchor:m.anchor+m.pinned], "\n") + "\n" + body
	}
	if m.overlayOpen {
		body = cypherPanel(m.overlayVP.View(), m.viewport.Width, m.viewport.Height)
	}

	traceHint := ""
	if m.lastTraceURL != "" {
		traceHint = " -- w: open trace"
	}

	var status string
	switch {
	case m.busy:
		elapsed := time.Since(m.busyStart).Round(time.Second)
		status = fmt.Sprintf("%s %s (%s elapsed)", m.spinner.View(), m.busyLabel, elapsed)
		status = lipgloss.NewStyle().Foreground(colorCyan).Background(colorBG).Render(status)
		// The hint belongs here most of all: mid-run is exactly when the trace is
		// worth opening, and its appearing is the signal that the link has landed.
		status += helpStyle.Render(traceHint)
	case m.overlayOpen && m.overlayMode == modeEvidence:
		status = helpStyle.Render("The Evidence predicate, as the tool emitted it -- ↑/↓ to scroll -- e or esc to close.")
	case m.overlayOpen:
		status = helpStyle.Render("Cypher for this stage -- ↑/↓ to scroll -- c or esc to close.")
	case m.phase == phaseDone:
		status = helpStyle.Render("Press Enter or q to exit. -- c: cypher" + m.evidenceHint() + traceHint)
	case m.waiting:
		status = helpStyle.Render("Enter: continue -- ↑/↓: scroll -- c: cypher" + m.evidenceHint() + " -- q: quit" + traceHint)
	}
	// phaseDone is a Phase value but not a step -- the closing summary belongs to
	// the last one, so the counter clamps instead of reading one past the total
	// on the final screen.
	n := int(m.phase) + 1
	if n > totalSteps {
		n = totalSteps
	}
	stepLabel := helpStyle.Render(fmt.Sprintf("step %d/%d", n, totalSteps))
	bottom := justify(status, stepLabel, m.width)

	// Pinned at the top always -- the viewport (sized in resizeViewport) is
	// what absorbs the terminal's extra height, not this block floating in
	// the middle of it.
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, bottom)
}

// evidenceHint advertises "e" only once a predicate exists, the same way the
// trace hint waits for a link. A key listed in the status line that does nothing
// when pressed is worse than an undiscovered one.
func (m Model) evidenceHint() string {
	if m.evidence == "" {
		return ""
	}
	return " -- e: evidence"
}

// centerBlock shifts every line of a multi-line block by the SAME amount, so
// the block's internal alignment survives being centered. lipgloss.
// PlaceHorizontal cannot be used for this: it centers each line independently
// against its own width, which is correct for prose but wrong for a diagram
// whose lines (the "DERIVED GRAPH" label, its arrow, the node row) are only
// meaningful relative to each other -- independent per-line centering was
// exactly what put that label over the wrong node.
//
// anchorWidth is what the block is centered ON, and it is the NODE ROW's width
// rather than the block's widest line. Those were the same number until the
// evidence fork arrived: it overhangs the row to the right, so centering on the
// widest line shifted every box left by half the overhang at the exact moment
// the fork appeared -- the boxes visibly jumped on the demo's last step. The row
// is the thing that must hold still; annotations are free to overhang it.
// Pass 0 to fall back to the widest line.
func centerBlock(block string, width, anchorWidth int) string {
	lines := strings.Split(block, "\n")
	maxW := anchorWidth
	if maxW <= 0 {
		for _, l := range lines {
			if w := lipgloss.Width(l); w > maxW {
				maxW = w
			}
		}
	}
	pad := (width - maxW) / 2

	// Last resort, for a terminal too narrow for the row plus its widest
	// annotation even after the label has shortened: give back enough of the pad
	// to keep that line on screen. This DOES move the boxes, which is the thing
	// the anchor exists to prevent -- but only where the alternative is drawing
	// off the right edge, and only on a terminal narrower than the demo needs.
	if anchorWidth > 0 {
		widest := 0
		for _, l := range lines {
			if w := lipgloss.Width(l); w > widest {
				widest = w
			}
		}
		if over := pad + widest - width; over > 0 {
			pad -= over
		}
	}

	if pad <= 0 {
		return block
	}
	margin := fill(pad)
	for i, l := range lines {
		lines[i] = margin + l
	}
	return strings.Join(lines, "\n")
}

// justify puts left flush against the left edge and right flush against the
// right edge of one line, width wide -- how "step N/10" ends up on the same
// line as the Enter/quit hint instead of on a line of its own underneath it.
func justify(left, right string, width int) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	gap := width - lw - rw
	if gap < 1 {
		gap = 1
	}
	return left + fill(gap) + right
}

// syncOverlay sizes the panel's viewport to the transcript's slot and fills it
// with the queries for the current phase.
//
// It deliberately touches only overlayVP. The transcript's own viewport, its
// anchor and its stepAnchor are left exactly as they were, so opening the panel
// mid-beat and closing it again returns to the same screen rather than jumping.
func (m *Model) syncOverlay() {
	w, h := overlayInner(m.viewport.Width, m.viewport.Height)
	m.overlayVP.Width, m.overlayVP.Height = w, h
	if m.overlayMode == modeEvidence {
		m.overlayVP.SetContent(evidencePanelBody(m.evidence, m.evidenceCmd))
	} else {
		m.overlayVP.SetContent(cypherPanelBody(m.phase, m.cypher, m.cypherErr))
	}
	m.overlayVP.GotoTop()
}
