package main

// The graph model. This is the on-disk contract (graph.json) between
// `monograph extract` and everything downstream, so it is deliberately plain.

// Provenance records how a fact came to be known. Everything this tool emits is
// "extracted" — parsed from source, never inferred.
//
// A second value, "graphify", is reserved for a semantic layer that is NOT built
// (see the README's "Not built" section). Nothing writes it today.
const ProvenanceExtracted = "extracted"

// Edge kinds, one per way a real dependency is expressed in the source.
const (
	ViaGoImport    = "go-import"
	ViaGoModule    = "go-module"
	ViaTSWorkspace = "ts-workspace-dep"
	ViaTSReference = "ts-reference"
	ViaTSExtends   = "ts-extends"
	ViaProtoImport = "proto-import"
)

// Target is one buildable/testable unit, identified by the directory holding
// its monograph.toml.
type Target struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Owner   string `json:"owner,omitempty"`
	Image   string `json:"image,omitempty"`
	TestCmd string `json:"testCmd,omitempty"`

	// Root is the target's directory, relative to the monorepo root, using
	// forward slashes. "." for the workspace target.
	Root string `json:"root"`

	// Produces lists globs this target generates. Files matching them are
	// OUTPUTS: they are excluded from the target's content hash, because a hash
	// over generated output would be a hash over a derivative of its own
	// inputs. Declaring outputs is not declaring dependencies — consumer edges
	// are still derived from real imports.
	Produces []string `json:"produces,omitempty"`

	// CodegenCmd creates the Produces globs. Required if Produces is set.
	CodegenCmd string `json:"codegenCmd,omitempty"`

	// TargetHash is the Merkle content hash, filled in by `monograph hash`.
	TargetHash string `json:"targetHash,omitempty"`
}

// Runnable reports whether this target has anything to execute. Targets like
// docs and proto legitimately appear in an affected set with no work to do.
func (t Target) Runnable() bool { return t.TestCmd != "" }

// File is a source file assigned to exactly one target (longest-prefix match).
type File struct {
	Path       string `json:"path"`       // relative to monorepo root
	TargetName string `json:"targetName"` // "" only if no target owns it
	SHA256     string `json:"sha256"`

	// Generated is true when this path matches a `produces` glob of its owning
	// target. Generated files are excluded from that target's content hash, so
	// the hash covers inputs only and stays stable whether or not generated
	// output happens to be present in the worktree.
	Generated bool `json:"generated,omitempty"`
}

// Edge means From depends on To. Direction is dependent -> dependency.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Via  string `json:"via"`
}

// Graph is the whole extraction result.
type Graph struct {
	Repo    string   `json:"repo"`
	Targets []Target `json:"targets"`
	Files   []File   `json:"files"`
	Edges   []Edge   `json:"edges"`

	// Warnings records things that looked wrong during extraction — e.g. a
	// source file importing a workspace package its package.json does not
	// declare. Surfaced rather than swallowed, so gaps stay visible.
	Warnings []string `json:"warnings,omitempty"`
}

// TargetByName indexes targets for lookup.
func (g *Graph) TargetByName() map[string]Target {
	m := make(map[string]Target, len(g.Targets))
	for _, t := range g.Targets {
		m[t.Name] = t
	}
	return m
}
