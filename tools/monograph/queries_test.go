package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// TestQueriesAreValidCypher runs EXPLAIN over every query `monograph queries`
// prints, which is the same constant the driver is handed at the call site.
//
// EXPLAIN parses and plans without executing, so this validates the exact text
// the tool runs -- and the text bench/demotui puts on a projector -- against a
// real server. It is the check that catches a mangled hoist: a query that lost a
// clause when it moved out of its call site would still compile in Go, still
// print, and still look plausible on screen.
func TestQueriesAreValidCypher(t *testing.T) {
	if os.Getenv("MONOGRAPH_SKIP_NEO4J") != "" {
		t.Skip("MONOGRAPH_SKIP_NEO4J set")
	}
	ctx := context.Background()
	d, err := connect(ctx)
	if err != nil {
		t.Skipf("Neo4j unavailable: %v", err)
	}
	t.Cleanup(func() { d.Close(ctx) })

	all := Queries()
	if len(all) == 0 {
		t.Fatal("Queries() is empty; the overlay would have nothing to show")
	}

	for _, q := range all {
		t.Run(q.Name, func(t *testing.T) {
			if strings.TrimSpace(q.Cypher) == "" {
				t.Fatal("empty Cypher")
			}
			for _, field := range []struct{ name, val string }{
				{"Title", q.Title}, {"When", q.When}, {"Stage", q.Stage}, {"Kind", q.Kind},
			} {
				if field.val == "" {
					t.Errorf("%s is empty; the panel would show an unlabelled query", field.name)
				}
			}
			session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
			defer session.Close(ctx)
			// EXPLAIN plans without running, so a write query is safe here.
			_, err := session.Run(ctx, "EXPLAIN "+q.Cypher, nil)
			if err != nil {
				t.Errorf("server rejected the query this tool runs and the demo displays: %v", err)
			}
		})
	}
}

// TestQueriesCoverBothStages pins that the overlay can never come up empty for a
// stage the demo has: a SELECT step and a RECORD step each need at least one.
func TestQueriesCoverBothStages(t *testing.T) {
	byStage := map[string]int{}
	for _, q := range Queries() {
		byStage[q.Stage]++
		if q.Kind != "read" && q.Kind != "write" {
			t.Errorf("%s: kind %q, want read or write", q.Name, q.Kind)
		}
	}
	for _, stage := range []string{"select", "record"} {
		if byStage[stage] == 0 {
			t.Errorf("no queries for stage %q", stage)
		}
	}
	fmt.Fprintln(os.Stderr, "stages:", byStage)
}
