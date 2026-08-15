package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestDaggerArgsForcePlainProgress pins the flag that makes dagger's output
// ours to read.
//
// Without it --progress defaults to "auto", and auto means dagger renders its
// TUI directly to the controlling terminal rather than to the stderr pipe
// this program captures -- so the "Full trace at …" line is never seen and no
// trace URL is ever recorded. That failure is invisible in an environment
// with no usable /dev/tty (where dagger already falls back to plain output),
// which is exactly why it survived a green end-to-end test run and only
// showed up in a real terminal.
func TestDaggerArgsForcePlainProgress(t *testing.T) {
	got := daggerArgs(false, "call", "orchestrator-dang", "run")

	if i := slices.Index(got, "--progress"); i == -1 {
		t.Fatalf("daggerArgs = %v, missing --progress", got)
	} else if got[i+1] != "plain" {
		t.Errorf("--progress = %q, want \"plain\"", got[i+1])
	}

	// The flag must lead, before the subcommand: dagger parses global options
	// ahead of the verb, and the caller's own args must survive in order.
	if want := []string{"call", "orchestrator-dang", "run"}; !slices.Equal(got[len(got)-3:], want) {
		t.Errorf("caller args = %v, want them preserved in order as %v", got[len(got)-3:], want)
	}
	if slices.Contains(got, "--web") {
		t.Error("--web present when web=false; it would open a browser tab nobody asked for")
	}
}

// TestDaggerArgsWeb pins that --web is forwarded when asked for, and still
// ahead of the subcommand where dagger parses global options.
func TestDaggerArgsWeb(t *testing.T) {
	got := daggerArgs(true, "call", "orchestrator-dang", "run")
	i, j := slices.Index(got, "--web"), slices.Index(got, "call")
	if i == -1 {
		t.Fatalf("daggerArgs = %v, missing --web", got)
	}
	if i > j {
		t.Errorf("--web at %d comes after the subcommand at %d", i, j)
	}
}

func TestDaggerTraceURL(t *testing.T) {
	stderr := `== TRACE ==  ✔ PASSED
orchestrator-dang straight-selected  1.3s

✔ connect 0.3s

Full trace at https://dagger.cloud/jpadams-demo/traces/e86d67584949a68b0d884c624de55794
`
	got := daggerTraceURL(stderr)
	want := "https://dagger.cloud/jpadams-demo/traces/e86d67584949a68b0d884c624de55794"
	if got != want {
		t.Errorf("daggerTraceURL = %q, want %q", got, want)
	}
}

// TestDaggerTraceURLWithoutFooter is the regression for the bug that actually
// shipped: anchoring the match on "Full trace at" found nothing in a real run
// whose 35KB of captured stderr ended mid-progress, footer never written. The
// URL was there the whole time on the early `cloud url=` line.
func TestDaggerTraceURLWithoutFooter(t *testing.T) {
	stderr := `1   : [0.0s] | cloud url=https://dagger.cloud/jpadams-demo/traces/588e7f6c35365c8bfd897aab9234c5f9
2   : [0.1s] | resolve image config
71  : ┆ File.contents DONE [0.1s]
`
	got := daggerTraceURL(stderr)
	want := "https://dagger.cloud/jpadams-demo/traces/588e7f6c35365c8bfd897aab9234c5f9"
	if got != want {
		t.Errorf("daggerTraceURL = %q, want %q -- the footer is absent, which is the real-world case", got, want)
	}
}

// TestDaggerTraceURLThroughANSI pins that colourised output still matches:
// the URL is found by shape after styling is stripped, not by raw prose.
func TestDaggerTraceURLThroughANSI(t *testing.T) {
	stderr := "Full trace at \x1b[36mhttps://dagger.cloud/org/traces/abc123def456\x1b[0m\n"
	if got, want := daggerTraceURL(stderr), "https://dagger.cloud/org/traces/abc123def456"; got != want {
		t.Errorf("daggerTraceURL = %q, want %q", got, want)
	}
}

// TestTrimHunkHeader pins both halves of the demo's tightest line: git's
// guessed function/heading context is dropped, and the reset code git puts at
// the END of the coloured header -- which the dropped tail would otherwise
// carry off with it, bleeding cyan down every line after the diff -- is not.
func TestTrimHunkHeader(t *testing.T) {
	got := trimHunkHeader("\x1b[36m@@ -28 +28,3 @@ That is the first golden test.\x1b[m")
	if plain := stripANSI(got); plain != "@@ -28 +28,3 @@" {
		t.Errorf("trimmed header = %q, want just the @@ marker", plain)
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("trimmed header = %q, want a trailing reset so colour cannot bleed", got)
	}

	// Anything that is not a hunk header passes through untouched -- the +/-
	// lines are the actual edit and must keep their own colouring intact.
	for _, line := range []string{"\x1b[32m+export function isPrivileged()\x1b[m", " context", ""} {
		if got := trimHunkHeader(line); got != line {
			t.Errorf("trimHunkHeader(%q) = %q, want it unchanged", line, got)
		}
	}
}

func TestDaggerTraceURLAbsent(t *testing.T) {
	if got := daggerTraceURL("no trace here, cloud not configured"); got != "" {
		t.Errorf("daggerTraceURL = %q, want empty", got)
	}
}

// TestTraceWatcherPublishesMidStream pins the property the whole change exists
// for: the URL is delivered when it appears in the stream, not when the stream
// ends. The write below is the first line or two of a run whose output will keep
// coming for another twenty seconds.
func TestTraceWatcherPublishesMidStream(t *testing.T) {
	ch := make(chan string, 4)
	var buf bytes.Buffer
	w := &traceWatcher{buf: &buf, ch: ch}

	if _, err := w.Write([]byte("1   : [0.0s] | connect\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case url := <-ch:
		t.Fatalf("published %q before any URL appeared", url)
	default:
	}

	w.Write([]byte("2   : [0.1s] | cloud url=https://dagger.cloud/acme/traces/abc123def456\n"))
	select {
	case got := <-ch:
		if want := "https://dagger.cloud/acme/traces/abc123def456"; got != want {
			t.Errorf("published %q, want %q", got, want)
		}
	default:
		t.Fatal("nothing published; \"w\" would stay dead until the run finished")
	}

	// Everything after the first hit is buffered but no longer scanned or resent.
	w.Write([]byte("3   : [0.2s] | cloud url=https://dagger.cloud/acme/traces/999999999999\n"))
	select {
	case got := <-ch:
		t.Errorf("published a second URL %q; one run has one trace", got)
	default:
	}

	if !strings.Contains(buf.String(), "999999999999") {
		t.Error("the watcher stopped teeing into the buffer after it found the URL")
	}
}

// TestTraceWatcherSurvivesASplitURL pins that a link straddling two writes is
// still found. Pipe reads chop wherever they like, and the URL sitting in the
// first ~200 bytes of the stream makes a split through it entirely possible.
func TestTraceWatcherSurvivesASplitURL(t *testing.T) {
	ch := make(chan string, 1)
	var buf bytes.Buffer
	w := &traceWatcher{buf: &buf, ch: ch}

	w.Write([]byte("cloud url=https://dagger.cl"))
	w.Write([]byte("oud/acme/traces/abc123def456\n"))

	select {
	case got := <-ch:
		if want := "https://dagger.cloud/acme/traces/abc123def456"; got != want {
			t.Errorf("published %q, want %q", got, want)
		}
	default:
		t.Fatal("a URL split across two writes was never found")
	}
}

// TestTraceWatcherNeverBlocksTheRun pins that dagger's output cannot be stalled
// by this: a nil channel (every test env) or a full one must drop the
// notification rather than deadlock the process writing to it.
func TestTraceWatcherNeverBlocksTheRun(t *testing.T) {
	line := []byte("cloud url=https://dagger.cloud/acme/traces/abc123def456\n")

	done := make(chan struct{})
	go func() {
		defer close(done)
		var b1, b2 bytes.Buffer
		(&traceWatcher{buf: &b1, ch: nil}).Write(line) // no channel at all
		full := make(chan string, 1)
		full <- "already in there"
		(&traceWatcher{buf: &b2, ch: full}).Write(line) // no room
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a write blocked; dagger's output would stall behind the UI")
	}
}

// TestTraceURLArrivesLongBeforeTheRunEnds is the claim end to end, against the
// real dagger binary: the link is usable while the run is still going.
//
// The unit tests above prove the watcher publishes on the right bytes; only this
// one proves those bytes actually show up early in a real invocation. Measured
// on a short call, the URL landed at ~0.3s against a run that returned at ~7.5s;
// the demo's own runs are three times longer than that.
//
// Skips rather than fails without dagger or a Cloud login, matching every other
// externally-dependent test here: absence of a trace link is a configuration
// fact, not a defect.
func TestTraceURLArrivesLongBeforeTheRunEnds(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("not inside a git repository: %v", err)
	}
	daggerBin := filepath.Join(root, ".bin", "dagger")
	if _, err := os.Stat(daggerBin); err != nil {
		t.Skipf("%s not present; see README Setup", daggerBin)
	}

	e := &env{root: root, repo: root, daggerBin: daggerBin, traceCh: make(chan string, 4)}

	start := time.Now()
	var url string
	var armedAt time.Duration
	go func() {
		u := <-e.traceCh
		url, armedAt = u, time.Since(start)
	}()

	// straight-selected is the cheapest call that still starts a real session.
	// It prints no JSON report, so runDagger returns an error -- irrelevant here,
	// since the watcher runs on the output either way.
	done := make(chan time.Duration, 1)
	go func() {
		_, _ = runDagger(e, filepath.Join(t.TempDir(), "out.json"),
			"call", "orchestrator-dang", "straight-selected")
		done <- time.Since(start)
	}()

	select {
	case finished := <-done:
		if url == "" {
			t.Skip("no trace URL produced -- fine if this CLI is not logged into Dagger Cloud")
		}
		if armedAt >= finished {
			t.Errorf(`"w" was armed at %v but the run ended at %v; the link is no earlier than it used to be`,
				armedAt, finished)
		}
		t.Logf(`"w" armed at %v, run ended at %v -- usable %v before the end`,
			armedAt.Round(time.Millisecond), finished.Round(time.Millisecond),
			(finished - armedAt).Round(time.Millisecond))
	case <-time.After(3 * time.Minute):
		t.Fatal("dagger never returned")
	}
}
