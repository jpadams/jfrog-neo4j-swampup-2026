package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skipDirs are never walked: build output, dependency trees, and VCS metadata
// are not source and would make content hashes machine-dependent.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	".turbo":       true,
	"graphify-out": true,
}

// skipFiles are incremental-build sidecars. They are outputs that happen to sit
// beside sources, so unlike `produces` globs they cannot be declared per target.
// Counting them as inputs would rewrite a target's hash on every local
// typecheck and destroy cache reuse for no reason.
var skipFiles = []string{
	"**/*.tsbuildinfo",
	"**/.DS_Store",
}

// discoverTargets finds every monograph.toml under root and returns the targets
// sorted by descending root-path length, so a longest-prefix match on a file
// path hits the most specific target first.
func discoverTargets(root string) ([]Target, error) {
	var targets []Target

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "monograph.toml" {
			return nil
		}

		dir := filepath.Dir(path)
		rel, err := relSlash(root, dir)
		if err != nil {
			return err
		}
		m, err := readManifest(path)
		if err != nil {
			return err
		}
		t, err := targetFromManifest(rel, m)
		if err != nil {
			return err
		}
		targets = append(targets, t)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no monograph.toml found under %s", root)
	}

	seen := map[string]string{}
	for _, t := range targets {
		if prev, dup := seen[t.Name]; dup {
			return nil, fmt.Errorf("duplicate target name %q at %s and %s", t.Name, prev, t.Root)
		}
		seen[t.Name] = t.Root
	}

	sortTargetsBySpecificity(targets)
	return targets, nil
}

// sortTargetsBySpecificity orders targets so that deeper roots come first.
// Ties break on name for deterministic output.
func sortTargetsBySpecificity(targets []Target) {
	sort.Slice(targets, func(i, j int) bool {
		li, lj := len(targets[i].Root), len(targets[j].Root)
		if li != lj {
			return li > lj
		}
		return targets[i].Name < targets[j].Name
	})
}

// ownerOf returns the name of the target owning a repo-relative path, using
// longest-prefix matching. targets must already be sorted by specificity.
func ownerOf(targets []Target, relPath string) string {
	for _, t := range targets {
		if t.Root == "." {
			continue // the catch-all is only used as a fallback below
		}
		if relPath == t.Root || strings.HasPrefix(relPath, t.Root+"/") {
			return t.Name
		}
	}
	// Fall back to a root-level target if one exists (the workspace target).
	for _, t := range targets {
		if t.Root == "." {
			return t.Name
		}
	}
	return ""
}

// discoverFiles walks the tree and assigns every file to its owning target,
// hashing contents as it goes.
func discoverFiles(root string, targets []Target) ([]File, error) {
	var files []File

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		// A symlink DOES have content: the path it points at, which is exactly
		// what git stores for one. Skipping it left that out of the Merkle hash,
		// so repointing a committed symlink produced a byte-identical
		// targetHash — git saw a change, monograph did not, and with reuse
		// enabled the target matched an earlier PASSED run and was skipped.
		// Changed content, silently never tested.
		//
		// Anything else irregular (device, socket, fifo) still has no stable
		// content and is still skipped.
		isSymlink := d.Type()&fs.ModeSymlink != 0
		if !isSymlink && !d.Type().IsRegular() {
			return nil
		}

		rel, err := relSlash(root, path)
		if err != nil {
			return err
		}
		if matchAnyGlob(skipFiles, rel) {
			return nil
		}
		var sum string
		if isSymlink {
			dest, err := os.Readlink(path)
			if err != nil {
				return err
			}
			h := sha256.Sum256([]byte(dest))
			sum = hex.EncodeToString(h[:])
		} else {
			sum, err = hashFile(path)
			if err != nil {
				return err
			}
		}
		owner := ownerOf(targets, rel)
		files = append(files, File{
			Path:       rel,
			TargetName: owner,
			SHA256:     sum,
			Generated:  isGenerated(targets, owner, rel),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// isGenerated reports whether relPath matches a `produces` glob of the target
// that owns it.
func isGenerated(targets []Target, owner, relPath string) bool {
	if owner == "" {
		return false
	}
	for _, t := range targets {
		if t.Name == owner {
			return matchAnyGlob(t.Produces, relPath)
		}
	}
	return false
}

// filesByTarget groups files by owning target.
func filesByTarget(files []File) map[string][]File {
	m := map[string][]File{}
	for _, f := range files {
		if f.TargetName == "" {
			continue
		}
		m[f.TargetName] = append(m[f.TargetName], f)
	}
	return m
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// relSlash returns path relative to root with forward slashes.
func relSlash(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
