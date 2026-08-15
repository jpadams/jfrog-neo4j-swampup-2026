package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type packageJSON struct {
	Name            string            `json:"name"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

type tsConfig struct {
	Extends    string `json:"extends"`
	References []struct {
		Path string `json:"path"`
	} `json:"references"`
}

// jsonCommentRe strips // line comments and /* */ blocks so real-world
// tsconfig files (which allow comments) parse with encoding/json.
var (
	lineCommentRe  = regexp.MustCompile(`(?m)^\s*//.*$`)
	blockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	trailingComma  = regexp.MustCompile(`,(\s*[}\]])`)
)

func readJSONC(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	clean := blockCommentRe.ReplaceAll(b, nil)
	clean = lineCommentRe.ReplaceAll(clean, nil)
	clean = trailingComma.ReplaceAll(clean, []byte("$1"))
	if err := json.Unmarshal(clean, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// tsImportRe finds bare module specifiers in import/export statements. It is
// used only to cross-check package.json, never as the source of truth.
var tsImportRe = regexp.MustCompile(`(?m)(?:import|export)[^"'\n]*?from\s*["']([^"']+)["']`)

// extractTSEdges derives edges from the three places a TypeScript monorepo
// really records its dependencies: workspace deps in package.json, project
// references in tsconfig.json, and the shared config a tsconfig extends.
//
// Source imports are then cross-checked against package.json and any
// discrepancy is reported as a warning rather than silently turned into an edge
// — an undeclared import is a bug in the monorepo, not a fact about it.
func extractTSEdges(root string, targets []Target, files []File) ([]Edge, []string, error) {
	var (
		edges    []Edge
		warnings []string
	)

	// package name -> owning target, built from every package.json.
	pkgOwner := map[string]string{}
	pkgDirOf := map[string]string{}
	for _, f := range files {
		if path.Base(f.Path) != "package.json" || f.TargetName == "" {
			continue
		}
		var pj packageJSON
		if err := readJSONC(filepath.Join(root, filepath.FromSlash(f.Path)), &pj); err != nil {
			return nil, nil, err
		}
		if pj.Name == "" {
			continue
		}
		pkgOwner[pj.Name] = f.TargetName
		pkgDirOf[pj.Name] = path.Dir(f.Path)
	}

	// 1. Workspace dependencies declared in package.json.
	for _, f := range files {
		if path.Base(f.Path) != "package.json" || f.TargetName == "" {
			continue
		}
		var pj packageJSON
		if err := readJSONC(filepath.Join(root, filepath.FromSlash(f.Path)), &pj); err != nil {
			return nil, nil, err
		}
		for depName, constraint := range pj.Dependencies {
			if !strings.HasPrefix(constraint, "workspace:") {
				continue
			}
			dep, ok := pkgOwner[depName]
			if !ok {
				warnings = append(warnings, fmt.Sprintf("%s: workspace dependency %q has no matching package.json", f.Path, depName))
				continue
			}
			if dep != f.TargetName {
				edges = append(edges, Edge{From: f.TargetName, To: dep, Via: ViaTSWorkspace})
			}
		}
	}

	// 2. tsconfig project references and 3. the extended base config.
	for _, f := range files {
		if path.Base(f.Path) != "tsconfig.json" || f.TargetName == "" {
			continue
		}
		var tc tsConfig
		if err := readJSONC(filepath.Join(root, filepath.FromSlash(f.Path)), &tc); err != nil {
			return nil, nil, err
		}
		dir := path.Dir(f.Path)

		for _, ref := range tc.References {
			refPath := path.Clean(path.Join(dir, ref.Path))
			dep := ownerOf(targets, refPath)
			if dep == "" {
				warnings = append(warnings, fmt.Sprintf("%s: reference %q resolves to %q, which no target owns", f.Path, ref.Path, refPath))
				continue
			}
			if dep != f.TargetName {
				edges = append(edges, Edge{From: f.TargetName, To: dep, Via: ViaTSReference})
			}
		}

		if tc.Extends != "" && strings.HasPrefix(tc.Extends, ".") {
			basePath := path.Clean(path.Join(dir, tc.Extends))
			dep := ownerOf(targets, basePath)
			if dep != "" && dep != f.TargetName {
				edges = append(edges, Edge{From: f.TargetName, To: dep, Via: ViaTSExtends})
			}
		}
	}

	// Cross-check: every workspace package a source file imports should be
	// declared by its package.json.
	declared := map[string]map[string]bool{} // target -> dep package names
	for _, f := range files {
		if path.Base(f.Path) != "package.json" || f.TargetName == "" {
			continue
		}
		var pj packageJSON
		if err := readJSONC(filepath.Join(root, filepath.FromSlash(f.Path)), &pj); err != nil {
			return nil, nil, err
		}
		set := map[string]bool{}
		for d := range pj.Dependencies {
			set[d] = true
		}
		for d := range pj.DevDependencies {
			set[d] = true
		}
		declared[f.TargetName] = set
	}

	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".ts") || f.TargetName == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path)))
		if err != nil {
			return nil, nil, err
		}
		for _, m := range tsImportRe.FindAllSubmatch(b, -1) {
			spec := string(m[1])
			if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "node:") {
				continue
			}
			owner, isWorkspacePkg := pkgOwner[spec]
			if !isWorkspacePkg || owner == f.TargetName {
				continue
			}
			if !declared[f.TargetName][spec] {
				warnings = append(warnings, fmt.Sprintf("%s: imports workspace package %q but its package.json does not declare it", f.Path, spec))
			}
		}
	}

	return edges, warnings, nil
}
