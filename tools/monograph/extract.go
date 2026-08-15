package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// protoImportRe matches `import "path/to/other.proto";`
var protoImportRe = regexp.MustCompile(`(?m)^\s*import\s+(?:public\s+|weak\s+)?"([^"]+)"\s*;`)

// extractProtoEdges derives edges from .proto import statements. Paths are
// resolved relative to the target owning the importing file, then relative to
// the repo root.
func extractProtoEdges(root string, targets []Target, files []File) ([]Edge, []string, error) {
	var (
		edges    []Edge
		warnings []string
	)
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".proto") || f.TargetName == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path)))
		if err != nil {
			return nil, nil, err
		}
		for _, m := range protoImportRe.FindAllSubmatch(b, -1) {
			imported := string(m[1])
			candidates := []string{
				path.Clean(path.Join(path.Dir(f.Path), imported)),
				path.Clean(imported),
			}
			resolved := ""
			for _, c := range candidates {
				if fileExists(files, c) {
					resolved = c
					break
				}
			}
			if resolved == "" {
				warnings = append(warnings, fmt.Sprintf("%s: proto import %q not found in repo", f.Path, imported))
				continue
			}
			dep := ownerOf(targets, resolved)
			if dep != "" && dep != f.TargetName {
				edges = append(edges, Edge{From: f.TargetName, To: dep, Via: ViaProtoImport})
			}
		}
	}
	return edges, warnings, nil
}

func fileExists(files []File, relPath string) bool {
	for _, f := range files {
		if f.Path == relPath {
			return true
		}
	}
	return false
}

// Extract builds the whole graph for a monorepo rooted at root.
func Extract(repoName, root string) (*Graph, error) {
	targets, err := discoverTargets(root)
	if err != nil {
		return nil, err
	}
	files, err := discoverFiles(root, targets)
	if err != nil {
		return nil, err
	}

	g := &Graph{Repo: repoName, Targets: targets, Files: files}

	for _, extractor := range []func(string, []Target, []File) ([]Edge, []string, error){
		extractGoEdges,
		extractTSEdges,
		extractProtoEdges,
	} {
		edges, warnings, err := extractor(root, targets, files)
		if err != nil {
			return nil, err
		}
		g.Edges = append(g.Edges, edges...)
		g.Warnings = append(g.Warnings, warnings...)
	}

	g.Edges = dedupeEdges(g.Edges)
	if cycle := findCycle(g.Edges); cycle != nil {
		return nil, fmt.Errorf("dependency cycle detected: %s", strings.Join(cycle, " -> "))
	}

	sort.Strings(g.Warnings)
	return g, nil
}

// dedupeEdges collapses duplicates while keeping every distinct `via`, so the
// graph records all the reasons one target depends on another.
func dedupeEdges(edges []Edge) []Edge {
	seen := map[Edge]bool{}
	out := make([]Edge, 0, len(edges))
	for _, e := range edges {
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Via < out[j].Via
	})
	return out
}

// findCycle returns a cycle if the dependency graph has one. A cycle would make
// the Merkle hash ill-defined, so extraction refuses to emit one.
func findCycle(edges []Edge) []string {
	adj := map[string][]string{}
	nodes := map[string]bool{}
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
		nodes[e.From] = true
		nodes[e.To] = true
	}

	const (
		white = 0
		grey  = 1
		black = 2
	)
	state := map[string]int{}
	var stack []string

	var visit func(string) []string
	visit = func(n string) []string {
		state[n] = grey
		stack = append(stack, n)
		for _, next := range adj[n] {
			switch state[next] {
			case grey:
				// Trim the stack to where the cycle starts.
				for i, s := range stack {
					if s == next {
						return append(append([]string{}, stack[i:]...), next)
					}
				}
				return append(append([]string{}, stack...), next)
			case white:
				if c := visit(next); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = black
		return nil
	}

	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		if state[n] == white {
			if c := visit(n); c != nil {
				return c
			}
		}
	}
	return nil
}

// strconvUnquote is a thin alias so extract_go.go can unquote import literals
// without importing strconv directly alongside its other imports.
func strconvUnquote(s string) (string, error) { return strconv.Unquote(s) }
