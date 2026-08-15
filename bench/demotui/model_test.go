package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestWKeyOpensTraceOnlyWhenKnown pins that "w" is a no-op until a Dagger
// Cloud trace link has actually been seen -- pressing it early must not panic
// or try to open an empty URL. The returned Cmd is checked for nil-ness only;
// it is never invoked here, so this never actually spawns a browser.
func TestWKeyOpensTraceOnlyWhenKnown(t *testing.T) {
	m := NewModel(&env{})
	wKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")}

	if _, cmd := m.Update(wKey); cmd != nil {
		t.Error("\"w\" with no trace URL known produced a Cmd; want a no-op")
	}

	m.lastTraceURL = "https://dagger.cloud/example/traces/abc123"
	if _, cmd := m.Update(wKey); cmd == nil {
		t.Error("\"w\" with a known trace URL produced no Cmd")
	}
}

// TestHandleStepRecordsTraceURL pins that a run's trace link survives into
// model state, which is what makes it available to a LATER "w" keypress --
// the transcript line alone is not enough, since it is arbitrary rendered
// text, not something Update can act on.
func TestHandleStepRecordsTraceURL(t *testing.T) {
	m := NewModel(&env{})
	m.phase = phaseBeat1Run

	tm, _ := m.handleStep(stepMsg{traceURL: "https://dagger.cloud/example/traces/first"})
	m = tm.(Model)
	if m.lastTraceURL != "https://dagger.cloud/example/traces/first" {
		t.Errorf("lastTraceURL = %q after first run", m.lastTraceURL)
	}

	// A later step with no trace URL of its own (e.g. Cloud briefly
	// unreachable) must not erase the most recent one that DID resolve.
	m.phase = phaseBeat2Run
	tm, _ = m.handleStep(stepMsg{})
	m = tm.(Model)
	if m.lastTraceURL != "https://dagger.cloud/example/traces/first" {
		t.Errorf("lastTraceURL = %q after a step with no trace URL; want the earlier one preserved", m.lastTraceURL)
	}

	m.phase = phaseBeat3Run
	tm, _ = m.handleStep(stepMsg{traceURL: "https://dagger.cloud/example/traces/second"})
	m = tm.(Model)
	if m.lastTraceURL != "https://dagger.cloud/example/traces/second" {
		t.Errorf("lastTraceURL = %q after second run; want it updated to the newer trace", m.lastTraceURL)
	}
}

// TestStepCounterClampsAtDone pins the bottom-right counter across the whole
// script, including the phase that has no step of its own.
//
// phaseDone is a Phase value but not a step -- it only prints the closing
// summary -- so an unclamped counter read one past the total on the final
// screen. The label says "step" rather than "beat" deliberately: there are more
// steps than there are BEAT headings, and the old wording put "beat 3/10" on
// screen directly beneath a heading reading "BEAT 1".
//
// The numbers are computed from phaseDone rather than written out, because they
// move every time a step is added -- as one just was, for the evidence
// predicate.
func TestStepCounterClampsAtDone(t *testing.T) {
	m := NewModel(&env{})
	m.width, m.height = 100, 40

	for p := phaseIntro; p <= phaseDone; p++ {
		m.phase = p
		view := stripANSI(m.View())
		want := fmt.Sprintf("step %d/%d", min(int(p)+1, totalSteps), totalSteps)
		if !strings.Contains(view, want) {
			t.Errorf("phase %d: view is missing %q", p, want)
		}
		if strings.Contains(view, "beat ") {
			t.Errorf("phase %d: view still says \"beat\" in the counter", p)
		}
	}

	m.phase = phaseDone
	unclamped := fmt.Sprintf("step %d/", int(phaseDone)+1)
	if got := stripANSI(m.View()); strings.Contains(got, unclamped) {
		t.Errorf("the closing summary counts as an extra step (%q); the counter is unclamped", unclamped)
	}
}

// TestBeatStartsAtTopOfViewport pins the presentation property: advancing into a
// beat puts that beat's heading on the FIRST visible row, while the transcript
// stays one continuous scrollback that ↑/↓ can still reach.
//
// Asserted on viewport.View() -- what the audience actually sees -- rather than
// on YOffset, because the failure this guards against was invisible in the
// offset: viewport.SetYOffset clamps to len(lines)-Height, so a beat shorter
// than the screen silently kept the previous beat's tail on the top rows.
func TestBeatStartsAtTopOfViewport(t *testing.T) {
	m := NewModel(&env{})
	m.width, m.height = 100, 30
	m.resizeViewport()

	// A first section long enough that the next one has something above it.
	m.appendTranscript(boldLine("==> throwaway clone -- the real repository is untouched"))
	for i := 0; i < 12; i++ {
		m.appendTranscript(note(fmt.Sprintf("setup line %d", i)))
	}
	// Then a short beat -- shorter than the viewport, the case the clamp broke.
	m.appendTranscript(beatLine("BEAT 2 -- THE SAME COMMIT, GRAPH-SELECTED"))
	m.appendTranscript(note("graph selects 1 target(s) to run: docs"))

	firstRow := stripANSI(strings.SplitN(m.viewport.View(), "\n", 2)[0])
	if !strings.HasPrefix(strings.TrimRight(firstRow, " "), "BEAT 2") {
		t.Errorf("top visible row = %q, want the BEAT 2 heading", firstRow)
	}
	visible := stripANSI(m.viewport.View())
	if strings.Contains(visible, "setup line") {
		t.Error("the previous section is still on screen; the beat did not start at the top")
	}

	// The scrollback is intact: nothing was cleared to achieve the above.
	if !containsPlain(joinTranscript(m.transcript), "setup line 0") {
		t.Error("transcript lost earlier content; scrolling back would not reach it")
	}
	m.viewport.GotoTop()
	if top := stripANSI(m.viewport.View()); !strings.Contains(top, "throwaway clone") {
		t.Error("scrolling to the top does not reach the first section")
	}
}

// TestBeatAnchorYieldsToOverflow pins the fallback: a section taller than the
// viewport must follow its newest line instead of pinning its heading, or the
// run results a beat exists to show would scroll off the bottom unseen.
func TestBeatAnchorYieldsToOverflow(t *testing.T) {
	m := NewModel(&env{})
	m.width, m.height = 100, 14 // a deliberately short window
	m.resizeViewport()

	m.appendTranscript(beatLine("BEAT 1 -- NAIVE CI: the docs typo rebuilds everything"))
	for i := 0; i < 40; i++ {
		m.appendTranscript(note(fmt.Sprintf("target %d PASSED", i)))
	}

	visible := stripANSI(m.viewport.View())
	if !strings.Contains(visible, "target 39 PASSED") {
		t.Error("the newest line is off screen; an overflowing beat must follow its output")
	}
}

// TestStepStartDoesNotBlankTheScreen pins that entering a step leaves the
// previous output up until the new step actually writes something.
//
// startPhase records stepAnchor before the step's Cmd runs, and that Cmd can be
// a twenty-second Dagger call. Anchoring to a not-yet-written line would empty
// the transcript area for the whole run -- a blank screen while presenting.
func TestStepStartDoesNotBlankTheScreen(t *testing.T) {
	m := NewModel(&env{})
	m.width, m.height = 100, 12 // short, so the beat cannot also fit
	m.resizeViewport()

	m.appendTranscript(beatLine("BEAT 1 -- NAIVE CI: the docs typo rebuilds everything"))
	for i := 0; i < 30; i++ {
		m.appendTranscript(note(fmt.Sprintf("prior line %d", i)))
	}

	// Entering the next step: nothing written yet.
	m.stepAnchor = len(m.transcript)
	m.syncViewport()

	if visible := strings.TrimSpace(stripANSI(m.viewport.View())); visible == "" {
		t.Fatal("transcript area went blank on entering a step; there is nothing to talk over while the Cmd runs")
	}
	if !strings.Contains(stripANSI(m.viewport.View()), "prior line 29") {
		t.Error("the newest prior line is not visible while the step is in flight")
	}
}

// TestMultiLineEntryDoesNotShiftTheAnchor is the regression for a bug that was
// invisible in the anchor index and only ever showed up on screen: beat 2 opened
// with the last lines of beat 1 still above its heading.
//
// coloredDiff returns an entire hunk as ONE string, so one appended element can
// render as several rows, while every anchor and the padding in syncViewport
// count rows. Anything that stores such an entry unsplit puts every later anchor
// short by the number of embedded newlines.
//
// The diff entry here is not decoration -- it is the actual shape cmdDocsEdit
// appends, and a version of this test written with one line per element passes
// against the broken code.
func TestMultiLineEntryDoesNotShiftTheAnchor(t *testing.T) {
	m := NewModel(&env{})
	m.width, m.height = 100, 30
	m.resizeViewport()

	m.appendTranscript(beatLine("BEAT 1 -- NAIVE CI: the docs typo rebuilds everything"))
	m.appendTranscript(note("PR 1, commit abc1234   changed: docs/README.md"), "")
	m.appendTranscript("@@ -28,0 +29,2 @@\n+\n+<!-- demo 123: fixed a stray double  space -->")
	for i := 0; i < 12; i++ {
		m.appendTranscript(note(fmt.Sprintf("apps/target-%d     PASSED", i)))
	}

	if joined := strings.Join(m.transcript, "\n"); len(m.transcript) != strings.Count(joined, "\n")+1 {
		t.Errorf("transcript holds %d elements but renders %d rows; anchors count rows",
			len(m.transcript), strings.Count(joined, "\n")+1)
	}

	m.appendTranscript(beatLine("BEAT 2 -- THE SAME COMMIT, GRAPH-SELECTED"),
		note("graph selects 1 target(s) to run: docs"))

	firstRow := stripANSI(strings.SplitN(m.viewport.View(), "\n", 2)[0])
	if !strings.HasPrefix(strings.TrimRight(firstRow, " "), "BEAT 2") {
		t.Errorf("top visible row = %q, want the BEAT 2 heading", firstRow)
	}
	if strings.Contains(stripANSI(m.viewport.View()), "PASSED") {
		t.Error("beat 1's results are still on screen above beat 2's heading")
	}
}

// TestRecordStageIsVisiblyVisited pins that RECORD is lit as an executing stage
// rather than jumping Pending -> Done, and that the demo's final RECORD holds
// for the presenter instead of running on into the summary.
//
// Both used to be true at once: recording was a tail on the run step, so RECORD
// flipped to Done in the same frame the run finished, and beat 4 then chained
// straight to the closing summary. The last stage of the pipeline lit and
// vanished in one breath.
func TestRecordStageIsVisiblyVisited(t *testing.T) {
	m := NewModel(&env{})

	for _, p := range []Phase{phaseBeat3Record, phaseBeat4Record} {
		m2, cmd := m.startPhase(p)
		if got := m2.diagram.Statuses[StageRecord]; got != StatusActive {
			t.Errorf("phase %d: RECORD status = %d, want StatusActive (%d)", p, got, StatusActive)
		}
		if cmd == nil {
			t.Errorf("phase %d: no Cmd; the stage would light with no work behind it", p)
		}
	}

	// The last record must be followed by a step the presenter advances TO, not
	// by an automatic jump. That successor is the evidence step now, which is
	// still a hold: the demo does not end on the record itself.
	if next, ok := nextPhase(phaseBeat4Record); !ok || next != phaseEvidence {
		t.Errorf("nextPhase(phaseBeat4Record) = (%d, %v), want (phaseEvidence, true)", next, ok)
	}
	m.phase = phaseBeat4Record
	m.waiting = false
	tm, _ := m.handleStep(stepMsg{lines: []string{note("recorded")}})
	after := tm.(Model)
	if after.phase != phaseBeat4Record {
		t.Errorf("handleStep advanced to phase %d on its own; the presenter never got to look at RECORD", after.phase)
	}
	if !after.waiting {
		t.Error("the final RECORD does not wait for a key press")
	}
}

// TestStageDwellIsAFloorNotADelay pins that the minimum illumination never slows
// a step that was already slow enough -- a twenty-second Dagger run must not
// become twenty-two.
func TestStageDwellIsAFloorNotADelay(t *testing.T) {
	quick := tea.Cmd(func() tea.Msg { return stepMsg{} })

	start := time.Now()
	atLeast(150*time.Millisecond, quick)()
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("fast step returned after %v, want it held for at least 150ms", elapsed)
	}

	slow := tea.Cmd(func() tea.Msg { time.Sleep(120 * time.Millisecond); return stepMsg{} })
	start = time.Now()
	atLeast(50*time.Millisecond, slow)()
	if elapsed := time.Since(start); elapsed > 110*time.Millisecond+60*time.Millisecond {
		t.Errorf("slow step took %v; the dwell added time to a step that was already long enough", elapsed)
	}

	// Zero disables it entirely, which is how the tests and --dwell=0 run.
	start = time.Now()
	atLeast(0, quick)()
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("dwell=0 still waited %v", elapsed)
	}
}

// TestReuseLoopAppearsOnlyAtBeat4 walks the whole script and pins that the
// RECORD->SELECT feedback path is absent from every frame before beat 4, then
// present from beat 4 onward.
//
// It used to switch on when beat 3's record finished. Beat 3 is what MAKES reuse
// possible -- it writes the passing runs the later skip cites -- but nothing has
// been skipped at that point, so the loop drew a claim a full beat ahead of its
// evidence, in a diagram whose whole job is to not overstate what has happened.
func TestReuseLoopAppearsOnlyAtBeat4(t *testing.T) {
	m := NewModel(&env{})

	for p := phaseIntro; p < phaseBeat4Select; p++ {
		var cmd tea.Cmd
		m, cmd = m.startPhase(p)
		_ = cmd
		if m.diagram.ShowReuseLoop {
			t.Fatalf("phase %d: reuse loop is showing before beat 4", p)
		}
		// The completion half of the step, which is where it used to appear.
		tm, _ := m.handleStep(stepMsg{})
		m = tm.(Model)
		if m.diagram.ShowReuseLoop {
			t.Fatalf("phase %d completed: reuse loop appeared before beat 4 began", p)
		}
		if strings.Contains(stripANSI(RenderDiagram(m.diagram)), "reuse:") {
			t.Fatalf("phase %d: the rendered diagram carries the reuse label already", p)
		}
	}

	m, _ = m.startPhase(phaseBeat4Select)
	if !m.diagram.ShowReuseLoop {
		t.Error("beat 4 started without the reuse loop; the beat that demonstrates it must show it")
	}
	if m.diagram.ReuseActive {
		t.Error("the loop is lit green before the selection has proven anything")
	}
	if !strings.Contains(stripANSI(RenderDiagram(m.diagram)), "reuse: same content hash") {
		t.Error("the rendered diagram is missing the reuse label at beat 4")
	}
}

// TestNoStageIsLeftExecuting walks the whole script and pins that every stage
// which lights up also finishes -- by the last step the pipeline must read as
// five completed stages, not four plus one still running.
//
// SELECT was the hole. A stage stays Active while the transcript below shows its
// result, and the NEXT stage's start marks it Done; beat 4's SELECT had no next
// stage to do that, so it sat cyan from step 11 to the end of the demo, which on
// a diagram whose whole job is to say what happened reads as "still executing".
func TestNoStageIsLeftExecuting(t *testing.T) {
	m := NewModel(&env{})

	for p := phaseIntro; p <= phaseEvidence; p++ {
		var cmd tea.Cmd
		m, cmd = m.startPhase(p)
		_ = cmd
		tm, _ := m.handleStep(stepMsg{})
		m = tm.(Model)
	}
	m, _ = m.startPhase(phaseDone)

	for st := Stage(0); st < numStages; st++ {
		if got := m.diagram.Statuses[st]; got != StatusDone {
			t.Errorf("%s ends the demo with status %d, want StatusDone (%d)",
				stageLabel[st], got, StatusDone)
		}
	}

	// And specifically at the last step, before the summary.
	m2 := NewModel(&env{})
	for p := phaseIntro; p <= phaseEvidence; p++ {
		m2, _ = m2.startPhase(p)
		tm, _ := m2.handleStep(stepMsg{})
		m2 = tm.(Model)
	}
	if got := m2.diagram.Statuses[StageSelect]; got != StatusDone {
		t.Errorf("at the final step SELECT is status %d, want StatusDone -- it reads as still running", got)
	}
}

// TestTraceURLMsgArmsTheWKeyAndRearms pins the model half of mid-run trace
// delivery: a URL that arrives while a run is still going must reach
// lastTraceURL (which is what "w" reads) and the listener must be re-armed, or
// only the demo's FIRST run would ever light the key up.
func TestTraceURLMsgArmsTheWKey(t *testing.T) {
	e := &env{traceCh: make(chan string, 4)}
	m := NewModel(e)
	m.busy = true // mid-run, which is the case that matters

	wKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")}
	if _, cmd := m.Update(wKey); cmd != nil {
		t.Error(`"w" did something before any URL arrived`)
	}

	tm, cmd := m.Update(traceURLMsg("https://dagger.cloud/acme/traces/abc123"))
	m = tm.(Model)
	if m.lastTraceURL != "https://dagger.cloud/acme/traces/abc123" {
		t.Errorf("lastTraceURL = %q; \"w\" would still do nothing mid-run", m.lastTraceURL)
	}
	if cmd == nil {
		t.Error("the listener was not re-armed; later runs would never deliver their URL")
	}
	if _, cmd := m.Update(wKey); cmd == nil {
		t.Error(`"w" is still inert after a URL arrived`)
	}

	// And the hint has to be visible in the busy state -- mid-run is precisely
	// when it tells the presenter the trace is ready.
	m.width, m.height = 100, 30
	m.resizeViewport()
	if view := stripANSI(m.View()); !strings.Contains(view, "w: open trace") {
		t.Error("no trace hint while busy; nothing signals that the link has landed")
	}
}

// TestNewRunClearsTheTraceURL pins the lifecycle of the link "w" opens: empty at
// startup, cleared the moment a new run begins, set again when that run publishes
// its own, and never pointing at the previous run's trace in between.
//
// The failure this prevents is a quiet one. Traces differ only by id, so opening
// the previous run's tab looks exactly like opening the right one -- and the
// window where it could happen is the first second of a run, which is precisely
// when a presenter reaches for the key.
func TestNewRunClearsTheTraceURL(t *testing.T) {
	e := &env{traceCh: make(chan string, 4)}
	m := NewModel(e)

	if m.lastTraceURL != "" {
		t.Errorf("a fresh model already has a trace URL: %q", m.lastTraceURL)
	}

	// A run publishes; "w" is armed.
	tm, _ := m.Update(traceURLMsg("https://dagger.cloud/acme/traces/run1"))
	m = tm.(Model)
	if m.lastTraceURL == "" {
		t.Fatal("the first run's URL never landed")
	}

	// Steps that are not runs must leave it alone: the presenter is still
	// talking about the run whose results are on screen.
	for _, p := range []Phase{phaseBeat2Select, phaseBeat3Select, phaseBeat3Record, phaseBeat4Record} {
		m2, _ := m.startPhase(p)
		if m2.lastTraceURL == "" {
			t.Errorf("phase %d cleared the trace URL; that run's trace is still the current one", p)
		}
	}

	// Each of the three runs clears it on the way in.
	for _, p := range []Phase{phaseBeat1Run, phaseBeat2Run, phaseBeat3Run} {
		m.lastTraceURL = "https://dagger.cloud/acme/traces/previous"
		m2, _ := m.startPhase(p)
		if m2.lastTraceURL != "" {
			t.Errorf("phase %d starts a run but kept %q", p, m2.lastTraceURL)
		}
		if _, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")}); cmd != nil {
			t.Errorf("phase %d: \"w\" tried to open something with no URL known", p)
		}
		// The status line must not advertise a trace it cannot open.
		m2.width, m2.height = 100, 30
		m2.resizeViewport()
		if strings.Contains(stripANSI(m2.View()), "open trace") {
			t.Errorf("phase %d still offers \"w: open trace\" with no URL", p)
		}
	}
}

// TestResetTraceDrainsStaleURLs pins the other half: a URL published by an
// earlier run but not yet consumed must not survive into the next run and land
// in lastTraceURL a moment after it was cleared.
func TestResetTraceDrainsStaleURLs(t *testing.T) {
	e := &env{traceCh: make(chan string, 4)}
	e.traceCh <- "https://dagger.cloud/acme/traces/stale1"
	e.traceCh <- "https://dagger.cloud/acme/traces/stale2"

	e.resetTrace()

	select {
	case url := <-e.traceCh:
		t.Errorf("a stale URL survived the reset: %q", url)
	default:
	}

	// Safe on an env with no channel at all, which is what the pure tests build.
	(&env{}).resetTrace()
	var nilEnv *env
	nilEnv.resetTrace()
}

// TestBeatHeadStaysOnScreenWhenTheBeatOverflows pins the property the demo kept
// losing: the change a beat is ABOUT must be visible for the whole beat, not
// just until its results arrive.
//
// Beat 3 crossed the viewport the day RECORD became its own step -- 25 rows
// against 23 -- and the anchor fell back to the current step, scrolling the diff
// away exactly when the beat's evidence landed. Trimming content bought two rows
// and would have broken again on a smaller terminal, so the head block is pinned
// instead.
func TestBeatHeadStaysOnScreenWhenTheBeatOverflows(t *testing.T) {
	m := NewModel(&env{})
	m.width, m.height = 100, 24 // deliberately short: the beat cannot fit
	m.resizeViewport()

	// A beat opens with its heading and the change it is about, in one step.
	m.appendTranscript(note("earlier beat"))
	m.appendTranscript(
		beatLine("BEAT 3 -- A REAL CHANGE: scoped, not trivial"),
		note("PR 2, commit 2b273db   changed: libs/core/src/index.ts"),
		"@@ -27,0 +28,5 @@\n+export function isPrivileged() {\n+}",
	)
	head := m.sectionHead
	if head < 4 {
		t.Fatalf("sectionHead = %d; the opening step wrote more rows than that", head)
	}

	// Then the beat's later steps bury it.
	for i := 0; i < 25; i++ {
		m.appendTranscript(note(fmt.Sprintf("target-%d PASSED", i)))
	}

	view := stripANSI(m.View())
	if !strings.Contains(view, "BEAT 3 -- A REAL CHANGE") {
		t.Error("the beat heading scrolled off; the audience cannot see which beat this is")
	}
	if !strings.Contains(view, "isPrivileged") {
		t.Error("the diff scrolled off; the beat is discussing a change nobody can see")
	}
	if !strings.Contains(view, "target-24 PASSED") {
		t.Error("the newest output is off screen; pinning must not cost the results")
	}
	if m.pinned != head {
		t.Errorf("pinned = %d rows, want the head block's %d", m.pinned, head)
	}
}

// TestViewportDoesNotShrinkAcrossPinnedFrames is the regression for a bug that
// only shows up over time: if the pinned rows are subtracted from the viewport's
// own height, that height becomes the input to the next calculation and the
// transcript area loses rows on every frame. Observed as a 23-row viewport
// wasting away to 7.
func TestViewportDoesNotShrinkAcrossPinnedFrames(t *testing.T) {
	m := NewModel(&env{})
	m.width, m.height = 100, 30
	m.resizeViewport()
	full := m.vpFull
	if full <= 0 {
		t.Fatal("vpFull was never set; syncViewport would fall back to the viewport's own height")
	}

	for round := 0; round < 5; round++ {
		m.appendTranscript(beatLine(fmt.Sprintf("BEAT %d -- x", round)), note("change"), note("more"))
		for i := 0; i < 30; i++ {
			m.appendTranscript(note("output"))
		}
		if got := m.viewport.Height + m.pinned; got != full {
			t.Fatalf("round %d: viewport %d + pinned %d = %d rows, want the full %d -- the area is shrinking",
				round, m.viewport.Height, m.pinned, got, full)
		}
	}
}
