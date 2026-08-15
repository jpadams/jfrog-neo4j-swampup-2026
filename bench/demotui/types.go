package main

// The JSON contract this file decodes is owned by tools/monograph (Plan) and by
// whatever the Dang orchestrator prints on its `run`/`straight` calls
// (RunReport). Both are duplicated here deliberately rather than imported: this
// is a separate Go module from tools/monograph specifically so Charm's
// dependencies never touch the core tool, and these structs carry only the
// fields this UI actually renders -- not the full contract.

// PathResolution is one changed path's classification, as monograph's plan
// carries it under "resolutions".
type PathResolution struct {
	Path    string   `json:"path"`
	How     string   `json:"how"`
	Targets []string `json:"targets"`
}

// PlanTarget is one target's entry in the selection plan.
type PlanTarget struct {
	Name     string `json:"name"`
	Runnable bool   `json:"runnable"`
	Reusable bool   `json:"reusable"`
}

// CodegenStep is a producer target that must run before the plan's targets.
type CodegenStep struct {
	Name string `json:"name"`
}

// Plan is `monograph affected`'s output -- only the fields show_selection (in
// the original bash) rendered.
type Plan struct {
	Resolutions []PathResolution `json:"resolutions"`
	Targets     []PlanTarget     `json:"targets"`
	Codegen     []CodegenStep    `json:"codegen"`
}

// Runnable is the set with real work to do that is not already reusable.
func (p Plan) Runnable() []string {
	var out []string
	for _, t := range p.Targets {
		if t.Runnable && !t.Reusable {
			out = append(out, t.Name)
		}
	}
	return out
}

// Reused is the set skipped because this exact content already passed.
func (p Plan) Reused() []string {
	var out []string
	for _, t := range p.Targets {
		if t.Runnable && t.Reusable {
			out = append(out, t.Name)
		}
	}
	return out
}

// TargetResult is one target's outcome in a Dang run report.
type TargetResult struct {
	Target     string `json:"target"`
	Status     string `json:"status"`
	DurationMs *int64 `json:"durationMs"`
}

// RunReport is what `dagger call orchestrator-dang run|straight` prints as its
// last JSON line.
type RunReport struct {
	Results []TargetResult `json:"results"`
}

// Evidence is the part of `monograph evidence`'s predicate this UI counts.
//
// Only the shape of the collections, not their contents: the panel shows the
// document verbatim, so decoding a field here would be a second, weaker copy of
// something already on screen. What the transcript needs is the set relation --
// affected ⊆ executed ∪ proven-reusable -- and that is arithmetic over lengths.
type Evidence struct {
	Affected []struct {
		Target string `json:"target"`
	} `json:"affected"`
	Executed []struct {
		Target string `json:"target"`
	} `json:"executed"`
	Skipped []struct {
		Target   string `json:"target"`
		ProvenBy *struct {
			CIRun string `json:"ciRun"`
		} `json:"provenBy"`
	} `json:"skipped"`
	CoverageGaps []string `json:"coverageGaps"`
}

// ProvenSkips is how many skipped targets carry a citation. Counted rather than
// assumed equal to len(Skipped): the difference between the two IS the coverage
// gap, and a demo that displayed them as the same number would be asserting the
// property it is supposed to be demonstrating.
func (e Evidence) ProvenSkips() int {
	n := 0
	for _, s := range e.Skipped {
		if s.ProvenBy != nil {
			n++
		}
	}
	return n
}

// CypherQuery mirrors what `monograph queries --json` emits. Decoded rather than
// copied: the whole reason that subcommand exists is that the demo must show the
// same string the tool hands the driver, so a hardcoded query here would defeat
// the point of asking for it.
type CypherQuery struct {
	Name   string `json:"name"`
	Stage  string `json:"stage"` // select | record
	Kind   string `json:"kind"`  // read | write
	Title  string `json:"title"`
	When   string `json:"when"` // when this query ACTUALLY runs
	Cypher string `json:"cypher"`
}
