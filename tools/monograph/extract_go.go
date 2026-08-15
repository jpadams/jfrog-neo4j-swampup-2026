package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// goModulePath reads the module path from a go.mod.
//
// Only the `module` line is needed, so this avoids depending on
// golang.org/x/mod for a one-line lookup.
var goModuleRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)

func goModulePath(goModFile string) (string, error) {
	b, err := os.ReadFile(goModFile)
	if err != nil {
		return "", err
	}
	m := goModuleRe.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("%s: no module directive", goModFile)
	}
	return string(m[1]), nil
}

// extractGoEdges derives dependency edges from real Go import statements.
//
// Imports are read with go/parser (stdlib) rather than go/packages: import
// paths are all that is needed for the graph, and this keeps the tool free of
// a heavyweight type-checking dependency. The tradeoff is that build-tag
// -gated imports are all treated as present, which over-approximates the graph
// — safe for CI selection (never misses a dependent) but worth stating.
func extractGoEdges(root string, targets []Target, files []File) ([]Edge, []string, error) {
	goModRel := ""
	for _, f := range files {
		if path.Base(f.Path) == "go.mod" {
			// Only the top-level module is modelled; nested modules would each
			// need their own prefix map.
			if goModRel == "" || strings.Count(f.Path, "/") < strings.Count(goModRel, "/") {
				goModRel = f.Path
			}
		}
	}
	if goModRel == "" {
		return nil, nil, nil // no Go in this repo
	}

	modulePath, err := goModulePath(filepath.Join(root, filepath.FromSlash(goModRel)))
	if err != nil {
		return nil, nil, err
	}
	moduleOwner := ownerOf(targets, goModRel)

	var (
		edges    []Edge
		warnings []string
		fset     = token.NewFileSet()
	)

	// Every Go file's directory maps to a target; an import path inside the
	// module maps back to a directory, and from there to the owning target.
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".go") || f.TargetName == "" {
			continue
		}

		af, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(f.Path)), nil, parser.ImportsOnly)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", f.Path, err)
		}

		for _, spec := range af.Imports {
			importPath, err := strconvUnquote(spec.Path.Value)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: unparsable import %s", f.Path, spec.Path.Value))
				continue
			}
			if !strings.HasPrefix(importPath, modulePath) {
				continue // stdlib or external; not part of this graph
			}

			relDir := strings.TrimPrefix(strings.TrimPrefix(importPath, modulePath), "/")
			// The imported package dir is relative to the go.mod directory.
			base := path.Dir(goModRel)
			if base == "." {
				base = ""
			}
			targetDir := path.Join(base, relDir)

			dep := ownerOf(targets, targetDir)
			if dep == "" {
				warnings = append(warnings, fmt.Sprintf("%s: import %q resolves to %q, which no target owns", f.Path, importPath, targetDir))
				continue
			}
			if dep == f.TargetName {
				continue // intra-target import
			}
			edges = append(edges, Edge{From: f.TargetName, To: dep, Via: ViaGoImport})
		}
	}

	// Every Go target belongs to the module declared in go.mod, so shared
	// toolchain config genuinely affects it.
	if moduleOwner != "" {
		for _, t := range targets {
			if !strings.HasPrefix(t.Kind, "go-") || t.Name == moduleOwner {
				continue
			}
			edges = append(edges, Edge{From: t.Name, To: moduleOwner, Via: ViaGoModule})
		}
	}

	return edges, warnings, nil
}
