// Command demotui is bench/demo.sh's four-beat story (naive CI vs
// graph-selected CI), rebuilt as a Bubble Tea program instead of scrolling
// terminal output. It performs the exact same real work -- a throwaway git
// clone, real edits, real commits, real monograph/dagger invocations -- and
// adds a persistent neon flow diagram plus a live elapsed timer while a
// Dagger call is in flight.
//
// This directory is its own Go module (separate from tools/monograph, so
// Charm's dependencies never touch the core tool), which means `go run
// ./bench/demotui` from the repo root fails with "cannot find main module":
// the repo root itself is not a module. Run it from inside this directory:
//
//	cd bench/demotui
//	go run .                # warm engine, default
//	go run . --fresh        # destroy the engine before each run
//	go run . --cold         # prune the engine cache only
//	go run . --no-pause     # auto-advance instead of Enter
//	go run . --dwell=0      # no minimum stage illumination (default 2s)
//	go run . --web          # dagger opens each run's Cloud trace in a browser
//
// Without --web the trace link is still shown in the transcript, and "w"
// opens the most recent one on demand -- which is usually what you want
// mid-demo, since --web opens a tab for every run. "w" is armed as soon as
// dagger prints the link, a second or two into a run, not when the run ends:
// mid-run is when a trace is worth opening.
//
// "c" opens a panel over the transcript showing the Cypher the current stage
// runs: the history read at SELECT, the writes at RECORD, and at the final step
// the read that produces the Evidence predicate. Its content comes from
// `monograph queries --json`, so it is the same text the tool hands the driver
// rather than a copy that can drift.
//
// "e" opens the same panel on the JFrog Evidence predicate the final step
// generated, followed by the `jf evd create` command that would upload it. Both
// come from `monograph evidence`, and neither is uploaded -- see
// docs/adr-003-jfrog-integration.md. The key does nothing until that step has
// run, and the status line does not advertise it until then either.
//
// See bench/demo.sh's header for why --fresh is the regime to quote and why
// the default does not lead with wall clock.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	fresh := flag.Bool("fresh", false, "destroy the engine container before each run (the regime to quote)")
	cold := flag.Bool("cold", false, "prune the engine cache only; images stay warm (inflates the ratio)")
	noPause := flag.Bool("no-pause", false, "auto-advance between beats instead of waiting for Enter")
	web := flag.Bool("web", false, "pass dagger's -w so each run opens its Dagger Cloud trace in a browser")
	dwell := flag.Duration("dwell", 2*time.Second, "minimum time a step stays on screen, so its diagram stage is visibly visited")
	flag.Parse()
	if *fresh {
		*cold = false // --fresh supersedes --cold; never both
	}

	if err := runMain(*fresh, *cold, *noPause, *web, *dwell); err != nil {
		fmt.Fprintln(os.Stderr, "demotui:", err)
		os.Exit(1)
	}
}

func runMain(fresh, cold, noPause, web bool, dwell time.Duration) error {
	root, err := repoRoot()
	if err != nil {
		return fmt.Errorf("finding repository root: %w", err)
	}

	monographBin := filepath.Join(root, ".bin", "monograph")
	daggerBin := filepath.Join(root, ".bin", "dagger")
	for _, bin := range []string{monographBin, daggerBin} {
		if _, err := os.Stat(bin); err != nil {
			return fmt.Errorf("%s not found; see the README's Setup section (build monograph, install dagger)", bin)
		}
	}

	work, err := os.MkdirTemp("", "demotui-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	target, warning, err := resolveNeo4jEnv(root)
	if err != nil {
		return err
	}
	if warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}

	e := &env{
		root:         root,
		work:         work,
		repo:         filepath.Join(work, "repo"),
		monographBin: monographBin,
		daggerBin:    daggerBin,
		nonce:        fmt.Sprintf("%d-%d", os.Getpid(), time.Now().Unix()),
		fresh:        fresh,
		cold:         cold,
		neo4jTarget:  target,
		web:          web,
		stageDwell:   dwell,
		// Buffered so a run publishing its trace URL never waits on the UI, and
		// deep enough that all three runs could publish before any is consumed.
		traceCh: make(chan string, 8),
	}
	e.noPause = noPause

	p := tea.NewProgram(NewModel(e), tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository (run from within jfrog-2026): %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
