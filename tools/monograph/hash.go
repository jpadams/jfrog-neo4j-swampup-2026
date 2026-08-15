package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// ComputeHashes fills in Target.TargetHash for every target, bottom-up.
//
//	targetHash(t) = H( sorted(file path + content hash for t)
//	                 + kind + image + testCmd
//	                 + sorted(dep name + dep targetHash) )
//
// The hash covers content and configuration, never commit SHAs or timestamps.
// That is the whole point: rebasing onto an unrelated main changes the commit
// SHA and leaves every targetHash identical, so previously-passed work is
// reusable.
func ComputeHashes(g *Graph) error {
	deps := map[string][]string{}
	for _, e := range g.Edges {
		deps[e.From] = append(deps[e.From], e.To)
	}

	byTarget := filesByTarget(g.Files)
	byName := map[string]*Target{}
	for i := range g.Targets {
		byName[g.Targets[i].Name] = &g.Targets[i]
	}

	// Depth-first with memoisation. Extract() already rejected cycles, but
	// guard anyway so a bug here fails loudly instead of hanging.
	const (
		unvisited = iota
		inProgress
		done
	)
	state := map[string]int{}

	var compute func(name string) (string, error)
	compute = func(name string) (string, error) {
		t, ok := byName[name]
		if !ok {
			return "", fmt.Errorf("edge references unknown target %q", name)
		}
		switch state[name] {
		case done:
			return t.TargetHash, nil
		case inProgress:
			return "", fmt.Errorf("cycle through target %q", name)
		}
		state[name] = inProgress

		h := sha256.New()

		// Own content, in a deterministic order. Generated files are skipped:
		// they are outputs, so hashing them would fold a derivative of the
		// inputs back into the hash. That would make the hash depend on whether
		// generated output happens to be present in the worktree, and a stale
		// checked-in artifact would silently change the key.
		own := byTarget[name]
		sort.Slice(own, func(i, j int) bool { return own[i].Path < own[j].Path })
		for _, f := range own {
			if f.Generated {
				continue
			}
			fmt.Fprintf(h, "file\x00%s\x00%s\n", f.Path, f.SHA256)
		}

		// Build configuration: a different image, test command, or codegen
		// command is different work, even over identical sources. The produces
		// globs are part of the identity too — changing what a target emits
		// changes what its consumers receive.
		fmt.Fprintf(h, "kind\x00%s\nimage\x00%s\ntestCmd\x00%s\ncodegenCmd\x00%s\n",
			t.Kind, t.Image, t.TestCmd, t.CodegenCmd)
		produces := append([]string{}, t.Produces...)
		sort.Strings(produces)
		for _, g := range produces {
			fmt.Fprintf(h, "produces\x00%s\n", g)
		}

		// Dependencies, by their own hashes — this is what makes it a Merkle DAG.
		depNames := append([]string{}, deps[name]...)
		sort.Strings(depNames)
		var lastDep string
		for _, d := range depNames {
			if d == lastDep {
				continue // same dep via several edge kinds; hash it once
			}
			lastDep = d
			dh, err := compute(d)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(h, "dep\x00%s\x00%s\n", d, dh)
		}

		t.TargetHash = hex.EncodeToString(h.Sum(nil))
		state[name] = done
		return t.TargetHash, nil
	}

	names := make([]string, 0, len(g.Targets))
	for _, t := range g.Targets {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := compute(n); err != nil {
			return err
		}
	}
	return nil
}
