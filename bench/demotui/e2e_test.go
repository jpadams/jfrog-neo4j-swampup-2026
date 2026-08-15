package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestFullDemoPipeline drives every phase of the real script -- the same
// clone/edit/commit/extract/affected/run/record sequence bench/demo.sh
// performs -- with no tty and no bubbletea Program involved. tea.Cmd is just
// `func() tea.Msg`, so each phase's Cmd is invoked directly here and its
// result fed into the same Update/handleStep code path a running program
// would use. This is the thing that matters: it exercises the real monograph
// and dagger binaries, a real throwaway git clone, and (if configured) a real
// Neo4j -- not a mock of any of them.
//
// Skips itself, rather than failing, when the prerequisites bench/demo.sh
// already requires (git repo, built .bin/monograph and .bin/dagger, a running
// Dagger engine) are not present -- the same convention tools/monograph's
// Neo4j-dependent tests use.
func TestFullDemoPipeline(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("not inside a git repository: %v", err)
	}
	monographBin := filepath.Join(root, ".bin", "monograph")
	daggerBin := filepath.Join(root, ".bin", "dagger")
	for _, bin := range []string{monographBin, daggerBin} {
		if _, err := os.Stat(bin); err != nil {
			t.Skipf("%s not built; see README Setup", bin)
		}
	}

	work := t.TempDir()
	target, warning, err := resolveNeo4jEnv(root)
	if err != nil {
		t.Skipf("resolving Neo4j env: %v", err)
	}
	if warning != "" {
		t.Log(warning)
	}

	// Deliberately NOT requiring `main` to be checked out at root: this test
	// runs from whatever branch is actually active (often a feature branch
	// mid-development), which is exactly the case cloneWorkspace's
	// origin/main fallback exists to handle. See work.go's cloneWorkspace.
	e := &env{
		root:         root,
		work:         work,
		repo:         filepath.Join(work, "repo"),
		monographBin: monographBin,
		daggerBin:    daggerBin,
		// Unique per test invocation -- see bench/demo.sh's header on why a
		// stable nonce would let a later run find beat 3's content already
		// "reusable" and silently test the reuse story instead of the real one.
		nonce:       fmt.Sprintf("e2e-test-%d", time.Now().UnixNano()),
		neo4jTarget: target,
	}

	m := NewModel(e)
	var cmd tea.Cmd
	m, cmd = m.startPhase(phaseIntro)

	seen := map[Phase]bool{}
	deadline := time.Now().Add(4 * time.Minute)

	for {
		if time.Now().After(deadline) {
			t.Fatalf("pipeline did not reach phaseDone within the deadline; stuck at phase %d", m.phase)
		}
		processedPhase := m.phase
		if cmd != nil {
			msg := cmd()
			sm, ok := msg.(stepMsg)
			if !ok {
				t.Fatalf("phase %d: Cmd produced %T, want stepMsg", m.phase, msg)
			}
			if sm.err != nil {
				t.Fatalf("phase %d failed: %v\ntranscript so far:\n%s", m.phase, sm.err, joinTranscript(m.transcript))
			}
			// handleStep may itself chain phaseBeat4Select -> phaseDone (no hold
			// between beat 4 and the closing summary, matching bench/demo.sh), so
			// m.phase after this call can already be the NEXT phase. Record what
			// the message was actually for, captured above, not what is left.
			var tm tea.Model
			tm, cmd = m.handleStep(sm)
			m = tm.(Model)
		} else {
			cmd = nil
		}
		seen[processedPhase] = true

		if m.phase == phaseDone {
			break
		}
		np, ok := nextPhase(m.phase)
		if !ok {
			t.Fatalf("no successor phase for %d before reaching phaseDone", m.phase)
		}
		m, cmd = m.startPhase(np)
	}

	for p := phaseIntro; p <= phaseEvidence; p++ {
		if !seen[p] {
			t.Errorf("phase %d was never reached", p)
		}
	}

	// The causal claim beat 4 exists to prove: after beat 3's run, re-asking the
	// identical selection must come back with nothing left to run.
	if !m.diagram.ReuseActive {
		t.Error("ReuseActive is false after beat 4; the reuse loop never got its causal proof")
	}
	if len(m.changedDocs) == 0 {
		t.Error("changedDocs was never populated from the docs-edit phase")
	}
	if len(m.changedCore) == 0 {
		t.Error("changedCore was never populated from the core-edit phase")
	}

	// Beat 2 must show the change it is talking about, between its heading and
	// its selection. Beat 3 gets this for free -- its heading lives on the edit
	// step, so the diff is still on screen when the selection prints -- but beat
	// 2's own heading re-anchors the viewport and scrolls beat 1's diff away, so
	// it has to re-print it. Asserted structurally on the real transcript,
	// because the failure is silent: the demo still reads correctly, it just
	// discusses a change the audience can no longer see.
	{
		plain := stripANSI(joinTranscript(m.transcript))
		head := strings.Index(plain, "BEAT 2 --")
		sel := strings.Index(plain, "graph selects 1 target(s)")
		if head < 0 || sel < 0 || sel < head {
			t.Errorf("beat 2's heading/selection not found in order (head=%d sel=%d)", head, sel)
		} else if between := plain[head:sel]; !strings.Contains(between, "@@") {
			t.Errorf("no diff between beat 2's heading and its selection; the change being discussed is off screen:\n%s", between)
		}
	}

	// The Cypher panel's content is loaded during the intro step. A failure there
	// is deliberately non-fatal at runtime -- the demo must not die over a panel
	// nobody may press -- which is exactly why it has to be asserted here.
	if m.cypherErr != nil {
		t.Errorf("the overlay's queries failed to load: %v", m.cypherErr)
	}
	if len(m.cypher) == 0 {
		t.Error("no Cypher queries reached the model; \"c\" would show an empty panel")
	}

	full := joinTranscript(m.transcript)
	for _, want := range []string{
		"BEAT 1 -- NAIVE CI",
		"BEAT 2 -- THE SAME COMMIT, GRAPH-SELECTED",
		"BEAT 3 -- A REAL CHANGE",
		"BEAT 4 -- REUSE",
		"demo complete",
	} {
		if !containsPlain(full, want) {
			t.Errorf("transcript missing expected beat marker %q", want)
		}
	}

	// Whether a trace link appears at all depends on the CLI being logged into
	// Dagger Cloud on whatever machine runs this test, so its ABSENCE cannot
	// be a hard failure. Its presence, though, must reach m.lastTraceURL and
	// not merely the transcript: the "w" key reads that field, and a trace
	// that renders as text while leaving the field empty is exactly the bug
	// that shipped once already.
	if containsPlain(full, "https://dagger.cloud/") {
		if m.lastTraceURL == "" {
			t.Error("a trace link reached the transcript but lastTraceURL is empty; \"w\" would do nothing")
		} else {
			t.Logf("trace link recorded for the w key: %s", m.lastTraceURL)
		}
	} else {
		t.Log("no Dagger Cloud trace link in transcript -- fine if this CLI isn't logged into Dagger Cloud")
	}
}

func joinTranscript(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

// containsPlain checks for a substring while ignoring ANSI styling, since
// every transcript line is already lipgloss-rendered by this point.
func containsPlain(s, substr string) bool {
	return strings.Contains(stripANSI(s), substr)
}
