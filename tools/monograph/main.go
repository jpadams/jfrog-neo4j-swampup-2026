// Command monograph builds and queries the monorepo context graph.
//
//	monograph extract  --repo <dir>            emit graph.json on stdout
//	monograph hash     [--in graph.json]       fill in Merkle target hashes
//	monograph load     [--in graph.json]       MERGE the graph into Neo4j
//	monograph affected --changed a,b | --sha X  emit the selection plan
//	monograph record   --plan p --results r     write run outcomes back
//	monograph evidence --run-id X               emit the JFrog Evidence predicate
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "extract":
		err = cmdExtract(os.Args[2:])
	case "hash":
		err = cmdHash(os.Args[2:])
	case "load":
		err = cmdLoad(os.Args[2:])
	case "affected":
		err = cmdAffected(os.Args[2:])
	case "record":
		err = cmdRecord(os.Args[2:])
	case "evidence":
		err = cmdEvidence(os.Args[2:])
	case "queries":
		err = cmdQueries(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "monograph: unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "monograph:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `monograph builds and queries the monorepo context graph.

  extract  --repo <dir>                  walk the monorepo, emit graph.json
  hash     [--in <file>]                 fill in Merkle target hashes
  load     [--in <file>]                 MERGE the graph into Neo4j
  affected --changed <paths> | --sha <s> emit the selection plan
  record   --plan <f> --results <f>      write run outcomes back to the graph
  evidence --run-id <id>                 emit the JFrog Evidence predicate for a
                                         recorded run, read back from the graph
                                         (--command prints the jf evd create
                                         invocation; monograph never runs it)
  queries  [--stage select|record|evidence]  print the Cypher this tool runs

Deletions need the tree from BEFORE the change, because nothing in the current
tree can own a path that is gone. Two ways to supply it:

  affected --base-in <file>   a graph.json extracted at the base commit
  affected --base-sha <sha>   the file index recorded by 'load --sha' at that
                              commit — no second extract, no second upload

--base-sha versions the file index only, so it resolves a deleted FILE but
refuses a deleted PACKAGE: reaching the surviving consumers of a removed target
needs that commit's dependency edges, which --base-in carries and the recorded
index does not. It refuses rather than returning an empty plan.

Neo4j connection comes from NEO4J_URI, NEO4J_USERNAME, NEO4J_PASSWORD.
`)
}

func cmdExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	repo := fs.String("repo", "monorepo", "path to the monorepo root")
	name := fs.String("name", "", "repo name recorded in the graph (defaults to the directory name)")
	withHashes := fs.Bool("hash", true, "compute Merkle target hashes during extraction")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repoName := *name
	if repoName == "" {
		repoName = baseName(*repo)
	}

	g, err := Extract(repoName, *repo)
	if err != nil {
		return err
	}
	if *withHashes {
		if err := ComputeHashes(g); err != nil {
			return err
		}
	}

	for _, w := range g.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	return writeJSON(os.Stdout, g)
}

func cmdHash(args []string) error {
	fs := flag.NewFlagSet("hash", flag.ExitOnError)
	in := fs.String("in", "-", "graph.json to read (- for stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g, err := readGraph(*in)
	if err != nil {
		return err
	}
	if err := ComputeHashes(g); err != nil {
		return err
	}
	return writeJSON(os.Stdout, g)
}

func readGraph(pathOrDash string) (*Graph, error) {
	r := io.Reader(os.Stdin)
	if pathOrDash != "-" {
		f, err := os.Open(pathOrDash)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	var g Graph
	if err := json.NewDecoder(r).Decode(&g); err != nil {
		return nil, fmt.Errorf("reading graph: %w", err)
	}
	return &g, nil
}

// readPlan reads a selection plan as emitted by `monograph affected`. The plan
// is the only artefact that records WHY a target was selected, so recording a
// run without it leaves the graph able to say what ran and not what it was for.
func readPlan(pathOrDash string) (Plan, error) {
	var p Plan
	r, closeFn, err := openInput(pathOrDash)
	if err != nil {
		return p, err
	}
	defer closeFn()
	if err := decodeJSON(r, &p); err != nil {
		return p, fmt.Errorf("reading plan: %w", err)
	}
	return p, nil
}

// openInput returns a reader for a path or stdin, plus a close func.
func openInput(pathOrDash string) (io.Reader, func(), error) {
	if pathOrDash == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(pathOrDash)
	if err != nil {
		return nil, func() {}, err
	}
	return f, func() { f.Close() }, nil
}

func decodeJSON(r io.Reader, v any) error {
	if err := json.NewDecoder(r).Decode(v); err != nil {
		return fmt.Errorf("decoding JSON input: %w", err)
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
