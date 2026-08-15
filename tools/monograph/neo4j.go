package main

import (
	"context"
	"fmt"
	"os"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// connect opens a Neo4j session from environment configuration, matching the
// defaults in graph/docker-compose.yml.
//
// The default is announced rather than applied silently. Reuse is only correct
// against the graph that recorded the history, so quietly falling back to a
// local container when NEO4J_URI is unset is the most dangerous default here: if
// some unrelated Neo4j happens to be listening on 7687, `reusable` is answered
// from a foreign database and work is skipped — or repeated — on false evidence.
// The scripts resolve this properly by sourcing graph/neo4j-env.sh; a bare
// invocation gets told what it guessed.
func connect(ctx context.Context) (neo4j.DriverWithContext, error) {
	uri := envOr("NEO4J_URI", "neo4j://localhost:7687")
	user := envOr("NEO4J_USERNAME", "neo4j")
	pass := envOr("NEO4J_PASSWORD", "monograph2026")

	if os.Getenv("NEO4J_URI") == "" {
		fmt.Fprintf(os.Stderr, "monograph: NEO4J_URI unset, defaulting to %s\n", uri)
	}

	d, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(user, pass, ""))
	if err != nil {
		return nil, fmt.Errorf("neo4j driver: %w", err)
	}
	if err := d.VerifyConnectivity(ctx); err != nil {
		return nil, fmt.Errorf("neo4j at %s unreachable: %w", uri, err)
	}
	return d, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func database() string { return envOr("NEO4J_DATABASE", "neo4j") }

// LoadGraph MERGEs the extracted graph into Neo4j. Idempotent: re-running with
// the same input converges to the same graph rather than duplicating it.
//
// Stale DEPENDS_ON edges and File nodes for the repo are removed first, so a
// deleted dependency really disappears instead of lingering forever.
func LoadGraph(ctx context.Context, d neo4j.DriverWithContext, g *Graph) error {
	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		if _, err := tx.Run(ctx, `MERGE (r:Repo {name: $repo}) SET r.provenance = $prov`,
			map[string]any{"repo": g.Repo, "prov": ProvenanceExtracted}); err != nil {
			return nil, err
		}

		// Clear the derived structure for this repo before rewriting it.
		if _, err := tx.Run(ctx, `
			MATCH (t:Target {repo: $repo})-[e:DEPENDS_ON]->() DELETE e`,
			map[string]any{"repo": g.Repo}); err != nil {
			return nil, err
		}
		if _, err := tx.Run(ctx, `
			MATCH (f:File {repo: $repo}) DETACH DELETE f`,
			map[string]any{"repo": g.Repo}); err != nil {
			return nil, err
		}

		targets := make([]any, 0, len(g.Targets))
		for _, t := range g.Targets {
			targets = append(targets, map[string]any{
				"name":       t.Name,
				"kind":       t.Kind,
				"owner":      t.Owner,
				"image":      t.Image,
				"testCmd":    t.TestCmd,
				"root":       t.Root,
				"targetHash": t.TargetHash,
			})
		}
		if _, err := tx.Run(ctx, `
			UNWIND $targets AS t
			MERGE (target:Target {name: t.name})
			SET target.kind       = t.kind,
			    target.owner      = t.owner,
			    target.image      = t.image,
			    target.testCmd    = t.testCmd,
			    target.root       = t.root,
			    target.targetHash = t.targetHash,
			    target.repo       = $repo,
			    target.provenance = $prov
			WITH target
			MATCH (r:Repo {name: $repo})
			MERGE (r)-[:HAS_TARGET]->(target)
			WITH target
			WHERE target.owner <> ''
			MERGE (team:Team {name: target.owner})
			MERGE (target)-[:OWNED_BY]->(team)`,
			map[string]any{"targets": targets, "repo": g.Repo, "prov": ProvenanceExtracted}); err != nil {
			return nil, err
		}

		files := make([]any, 0, len(g.Files))
		for _, f := range g.Files {
			files = append(files, map[string]any{
				"path":   f.Path,
				"target": f.TargetName,
				"sha256": f.SHA256,
			})
		}
		if _, err := tx.Run(ctx, `
			UNWIND $files AS f
			MERGE (file:File {path: f.path})
			SET file.sha256     = f.sha256,
			    file.targetName = f.target,
			    file.repo       = $repo,
			    file.provenance = $prov
			WITH file, f
			WHERE f.target <> ''
			MATCH (target:Target {name: f.target})
			MERGE (file)-[:PART_OF]->(target)`,
			map[string]any{"files": files, "repo": g.Repo, "prov": ProvenanceExtracted}); err != nil {
			return nil, err
		}

		edges := make([]any, 0, len(g.Edges))
		for _, e := range g.Edges {
			edges = append(edges, map[string]any{"from": e.From, "to": e.To, "via": e.Via})
		}
		if _, err := tx.Run(ctx, `
			UNWIND $edges AS e
			MATCH (from:Target {name: e.from})
			MATCH (to:Target {name: e.to})
			MERGE (from)-[d:DEPENDS_ON {via: e.via}]->(to)
			SET d.provenance = $prov`,
			map[string]any{"edges": edges, "prov": ProvenanceExtracted}); err != nil {
			return nil, err
		}

		return nil, nil
	})
	return err
}

// AffectedViaCypher answers the affected-target question with a graph query
// instead of an in-memory walk. Both paths exist on purpose: this one is the
// real thesis of the project, and TestCypherMatchesInMemory asserts they agree.
func AffectedViaCypher(ctx context.Context, d neo4j.DriverWithContext, repo string, changedFiles []string) ([]string, error) {
	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		// Reverse reachability. Note the variable-length pattern is written
		// dependent -> dependency with the changed target on the right, so
		// `affected` collects everything upstream of the change.
		res, err := tx.Run(ctx, AffectedViaCypherQuery,
			map[string]any{"changed": normalisePaths(changedFiles), "repo": repo})
		if err != nil {
			return nil, err
		}
		var names []string
		for res.Next(ctx) {
			if v, ok := res.Record().Get("name"); ok {
				if s, ok := v.(string); ok {
					names = append(names, s)
				}
			}
		}
		return names, res.Err()
	})
	if err != nil {
		return nil, err
	}
	names, _ := result.([]string)
	return names, nil
}

// MarkReusable sets Reusable on plan entries whose targetHash has already
// passed in some previous TargetRun. This is what makes a rebase free.
func MarkReusable(ctx context.Context, d neo4j.DriverWithContext, plan *Plan) error {
	hashes := make([]any, 0, len(plan.Targets))
	for _, t := range plan.Targets {
		if t.TargetHash != "" {
			hashes = append(hashes, t.TargetHash)
		}
	}
	if len(hashes) == 0 {
		return nil
	}

	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, MarkReusableQuery, map[string]any{"hashes": hashes})
		if err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for res.Next(ctx) {
			rec := res.Record()
			h, _ := rec.Get("hash")
			r, _ := rec.Get("reusable")
			hs, ok1 := h.(string)
			rb, ok2 := r.(bool)
			if ok1 && ok2 {
				out[hs] = rb
			}
		}
		return out, res.Err()
	})
	if err != nil {
		return err
	}

	reusable, _ := result.(map[string]bool)
	for i := range plan.Targets {
		if reusable[plan.Targets[i].TargetHash] {
			plan.Targets[i].Reusable = true
		}
	}
	return nil
}

// RecordRun writes a CIRun and one TargetRun per executed target back into the
// history layer. Without this, every history query is decoration.
func RecordRun(ctx context.Context, d neo4j.DriverWithContext, run RunReport) error {
	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)

	results := make([]any, 0, len(run.Results))
	for _, r := range run.Results {
		results = append(results, map[string]any{
			"id":         run.ID + ":" + r.Target,
			"target":     r.Target,
			"status":     r.Status,
			"durationMs": r.DurationMs,
			"targetHash": r.TargetHash,
			"toolchain":  r.Toolchain,
		})
	}
	// Note there is no "cacheHit" here. An orchestrator cannot know whether the
	// engine replayed its exec, so the value is derived below from the graph
	// instead of being reported by a component that would have to guess.

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		// createdAt is stamped by the SERVER, ON CREATE only, and it replaced a
		// `startedAt` this same query used to copy out of the report.
		//
		// That field was the same mistake as the old hardcoded cacheHit, one layer
		// up: the report's `startedAt` was a slot no component ever filled -- the
		// Dang orchestrator's reportJson does not emit it -- so all 29 runs in the
		// graph carried startedAt: "". A run history with no usable time meant
		// "which run was the latest?" could not be asked at all, while an index on
		// the dead property advertised that it could.
		//
		// ON CREATE rather than SET: re-recording a run id must not restamp it, or
		// createdAt would drift to mean "last touched" and the ordering it exists
		// to provide would quietly stop being creation order.
		if _, err := tx.Run(ctx, `
			MERGE (run:CIRun {id: $id})
			ON CREATE SET run.createdAt = datetime()
			SET run.trigger     = $trigger,
			    run.orchestrator = $orchestrator,
			    run.repo        = $repo,
			    run.provenance  = $prov
			WITH run
			FOREACH (_ IN CASE WHEN $sha = '' THEN [] ELSE [1] END |
			  MERGE (c:Commit {sha: $sha})
			  MERGE (run)-[:FOR_COMMIT]->(c)
			)`,
			map[string]any{
				"id": run.ID, "trigger": run.Trigger,
				"orchestrator": run.Orchestrator, "repo": run.Repo, "sha": run.SHA,
				"prov": ProvenanceExtracted,
			}); err != nil {
			return nil, err
		}

		// cacheHit and durationMs are derived here, not trusted from the report.
		//
		// If some earlier TargetRun already recorded this exact targetHash, then
		// this execution cannot have been fresh work: either monograph reused it
		// or the engine replayed a cached exec. In that case the reported
		// duration is the *earlier* execution's number replayed out of a cached
		// results file, so it is stored as null. An unknown duration is honest;
		// a stale one silently corrupts any slow-test or flakiness analysis
		// built on top of it.
		if _, err := tx.Run(ctx, RecordTargetRunsQuery,
			map[string]any{"id": run.ID, "results": results, "prov": ProvenanceExtracted}); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return err
}

// RecordSelection writes what a run selected and why, and returns the number of
// selected targets recorded.
//
// Until this existed the graph could say what RAN but not what the run was FOR.
// The `resolutions` array — the audit trail the plan deliberately carries — lived
// only in an ephemeral plan file and was discarded once the run finished, so
// "why did apps/admin run in that run?" became unanswerable the moment CI exited.
//
// Three relationships, each answering a question the graph could not:
//
//	(CIRun)-[:SELECTED {reason, executed}]->(Target)
//	    the affected set, with `reason` distinguishing a target whose own files
//	    changed from one reached transitively. This is what makes the fan-out
//	    auditable after the fact rather than only at selection time.
//
//	(CIRun)-[:CHANGED_PATH {path, how}]->(Target)
//	    the resolution audit trail, preserved. `how` is the classification
//	    (file/directory/deleted/ignored), so a surprising selection can be
//	    traced back to the path that caused it.
//
//	(CIRun)-[:PROVEN_BY {targetHash}]->(TargetRun)
//	    for a target skipped as reusable, the earlier PASSED run that justifies
//	    the skip. This is the important one: it turns "we skipped it because it
//	    was already green" from an assertion by the tool into a fact with a
//	    citation. Nothing else in the graph recorded WHY work was not done.
//
// Unresolved paths are stored as a property on the run rather than a
// relationship, because by definition they point at no target.
func RecordSelection(ctx context.Context, d neo4j.DriverWithContext, runID string, plan Plan) (int, error) {
	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)

	changed := map[string]bool{}
	for _, name := range plan.ChangedTargets {
		changed[name] = true
	}

	selected := make([]map[string]any, 0, len(plan.Targets))
	reused := make([]map[string]any, 0)
	for _, t := range plan.Targets {
		reason := "dependent"
		if changed[t.Name] {
			reason = "changed"
		}
		selected = append(selected, map[string]any{
			"name":       t.Name,
			"reason":     reason,
			"executed":   t.Runnable && !t.Reusable,
			"targetHash": t.TargetHash,
		})
		if t.Runnable && t.Reusable {
			reused = append(reused, map[string]any{
				"name": t.Name, "targetHash": t.TargetHash,
			})
		}
	}

	paths := make([]map[string]any, 0, len(plan.Resolutions))
	var unresolved []string
	for _, r := range plan.Resolutions {
		if r.How == ResolvedUnknown {
			unresolved = append(unresolved, r.Path)
			continue
		}
		for _, target := range r.Targets {
			paths = append(paths, map[string]any{
				"path": r.Path, "how": r.How, "target": target,
			})
		}
	}
	if unresolved == nil {
		unresolved = []string{}
	}

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		// The run must already exist: RecordRun creates it. Recording a selection
		// for a run nobody recorded would produce a CIRun with no outcomes.
		res, err := tx.Run(ctx, `
			MATCH (run:CIRun {id: $id})
			SET run.unresolvedPaths = $unresolved,
			    run.selectedCount   = $selectedCount
			WITH run
			FOREACH (_ IN CASE WHEN $sha = '' THEN [] ELSE [1] END |
			  MERGE (c:Commit {sha: $sha})
			  MERGE (run)-[:FOR_COMMIT]->(c)
			)
			RETURN count(run) AS found`,
			map[string]any{
				"id": runID, "unresolved": unresolved, "sha": plan.SHA,
				"selectedCount": len(selected),
			})
		if err != nil {
			return nil, err
		}
		rec, err := res.Single(ctx)
		if err != nil {
			return nil, fmt.Errorf("run %q not found; record the report before its selection: %w", runID, err)
		}
		if n, _ := rec.Get("found"); n == int64(0) {
			return nil, fmt.Errorf("run %q not found; record the report before its selection", runID)
		}

		if _, err := tx.Run(ctx, `
			MATCH (run:CIRun {id: $id})
			UNWIND $selected AS s
			MATCH (t:Target {name: s.name})
			MERGE (run)-[sel:SELECTED]->(t)
			SET sel.reason     = s.reason,
			    sel.executed   = s.executed,
			    sel.targetHash = s.targetHash,
			    sel.provenance = $prov`,
			map[string]any{"id": runID, "selected": selected, "prov": ProvenanceExtracted}); err != nil {
			return nil, err
		}

		if len(paths) > 0 {
			if _, err := tx.Run(ctx, `
				MATCH (run:CIRun {id: $id})
				UNWIND $paths AS p
				MATCH (t:Target {name: p.target})
				MERGE (run)-[cp:CHANGED_PATH {path: p.path}]->(t)
				SET cp.how = p.how, cp.provenance = $prov`,
				map[string]any{"id": runID, "paths": paths, "prov": ProvenanceExtracted}); err != nil {
				return nil, err
			}
		}

		// collect(prior)[0] rather than a subquery with LIMIT: LIMIT in an UNWIND
		// pipeline applies to the whole stream, not per row, so it would attach a
		// single proof to only one reused target. Aggregating groups by run and r.
		if len(reused) > 0 {
			if _, err := tx.Run(ctx, RecordProvenByQuery,
				map[string]any{"id": runID, "reused": reused, "prov": ProvenanceExtracted}); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	return len(selected), err
}

// RecordCommitFiles writes the file index as of a commit, so a later run can ask
// what the tree looked like then.
//
// This is what turns `--base-in` (a second on-disk extract, produced by whoever
// is calling) into `--base-sha` (a graph lookup). It writes to :FileVersion, NOT
// :File — see the schema comment. :File is the current snapshot that `load`
// deletes and rewrites, and it must stay one node per path or the
// `MATCH (f:File {repo, path})` in AffectedViaCypher stops being single-valued.
//
// targetName lives on the relationship rather than the node because ownership can
// change while content does not: adding a nested monograph.toml re-parents files
// whose bytes never moved.
func RecordCommitFiles(ctx context.Context, d neo4j.DriverWithContext, sha string, g *Graph) (int, error) {
	if sha == "" {
		return 0, fmt.Errorf("a commit sha is required to record a file index")
	}
	files := make([]any, 0, len(g.Files))
	for _, f := range g.Files {
		files = append(files, map[string]any{
			"path": f.Path, "sha256": f.SHA256, "target": f.TargetName,
		})
	}

	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		if _, err := tx.Run(ctx, `
			MERGE (c:Commit {sha: $sha})
			SET c.repo = $repo, c.provenance = $prov`,
			map[string]any{"sha": sha, "repo": g.Repo, "prov": ProvenanceExtracted}); err != nil {
			return nil, err
		}
		// Detach this commit's old CONTAINS edges first, so re-loading the same
		// sha after amending converges instead of accumulating a union of both
		// trees — which would make a deleted file look like it still existed.
		if _, err := tx.Run(ctx, `
			MATCH (:Commit {sha: $sha})-[c:CONTAINS]->(:FileVersion) DELETE c`,
			map[string]any{"sha": sha}); err != nil {
			return nil, err
		}
		if _, err := tx.Run(ctx, `
			MATCH (c:Commit {sha: $sha})
			UNWIND $files AS f
			MERGE (fv:FileVersion {path: f.path, sha256: f.sha256})
			SET fv.provenance = $prov
			MERGE (c)-[rel:CONTAINS]->(fv)
			SET rel.targetName = f.target`,
			map[string]any{"sha": sha, "files": files, "prov": ProvenanceExtracted}); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return len(files), err
}

// BaseGraphAtCommit rebuilds enough of a base graph from recorded history for
// deletion resolution: the file index as of that commit.
//
// The returned Graph has Files populated and Edges EMPTY, and that limitation is
// load-bearing rather than laziness. Deletion resolution needs only path ->
// owner, which is what CONTAINS records. Reaching the surviving dependents of a
// *whole deleted target* additionally needs that commit's edge set, which is not
// versioned — so `--base-sha` handles a deleted file and `--base-in`, which
// carries a real extracted graph, is still the answer for a deleted package.
//
// Returns ok=false when the commit was never recorded, so the caller can say so
// instead of silently behaving as though nothing was deleted.
func BaseGraphAtCommit(ctx context.Context, d neo4j.DriverWithContext, repo, sha string) (*Graph, bool, error) {
	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)

	out, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, `
			MATCH (c:Commit {sha: $sha})-[rel:CONTAINS]->(fv:FileVersion)
			RETURN fv.path AS path, fv.sha256 AS sha256, rel.targetName AS targetName`,
			map[string]any{"sha": sha})
		if err != nil {
			return nil, err
		}
		g := &Graph{Repo: repo}
		for res.Next(ctx) {
			rec := res.Record()
			p, _ := rec.Get("path")
			s, _ := rec.Get("sha256")
			tn, _ := rec.Get("targetName")
			ps, _ := p.(string)
			ss, _ := s.(string)
			tns, _ := tn.(string)
			g.Files = append(g.Files, File{Path: ps, SHA256: ss, TargetName: tns})
		}
		return g, res.Err()
	})
	if err != nil {
		return nil, false, err
	}
	g, _ := out.(*Graph)
	if g == nil || len(g.Files) == 0 {
		return nil, false, nil
	}
	return g, true, nil
}
