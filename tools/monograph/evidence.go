package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// The JFrog Evidence predicate: what this repo hands to a control plane that
// cannot compute it.
//
// An AppTrust gate can verify that test evidence EXISTS. It cannot verify that
// the tests which ran were SUFFICIENT for what changed, because at any real
// scale CI runs a selected subset — the evidence is true and incomplete, and the
// gate cannot tell the difference. Answering that needs a dependency graph of
// first-party source targets, which dies at the repo boundary and is therefore
// not something Artifactory can hold. See docs/adr-003-jfrog-integration.md.
//
// Everything here is READ from the graph (EvidenceQuery). Nothing is written:
// per ADR-003 the graph computes the decision — mutable, cross-run, analytical —
// and Evidence records it — per-version, signed, immutable. Attestations do not
// go in Neo4j.

// EvidencePredicateType is the in-toto predicate type URI, used both as the
// `predicateType` field inside the body and as `jf evd create --predicate-type`.
// One constant for both, so the document and the command that uploads it cannot
// name different things.
const EvidencePredicateType = "https://jfrog.com/evidence/monograph/ci-coverage/v1"

// EvidenceResolution is one changed path and how it was classified.
type EvidenceResolution struct {
	Path   string `json:"path"`
	How    string `json:"how"`
	Target string `json:"target"`
}

// EvidenceAffected is one target the graph said the change reached.
//
// Runnable is carried explicitly because affected and runnable are different
// questions: `proto` and `workspace` legitimately appear in an affected set with
// nothing to execute, and a verifier that assumed otherwise would read them as
// unverified work.
type EvidenceAffected struct {
	Target     string `json:"target"`
	Reason     string `json:"reason"` // changed | dependent
	Executed   bool   `json:"executed"`
	Runnable   bool   `json:"runnable"`
	TargetHash string `json:"targetHash"`
}

// EvidenceExecuted is one target that actually ran, with its verdict.
//
// DurationMs is a pointer because a replayed exec has no honest duration: the
// number in the report is the earlier execution's, so RecordRun stores null. A
// predicate that turned that into 0 would assert a measurement nobody made.
type EvidenceExecuted struct {
	Target     string `json:"target"`
	TargetHash string `json:"targetHash"`
	Verdict    string `json:"verdict"`
	DurationMs *int64 `json:"durationMs"`
	CacheHit   bool   `json:"cacheHit"`
	Toolchain  string `json:"toolchain,omitempty"`
}

// EvidenceProof is the citation for work that did not happen: the earlier PASSED
// run whose targetHash matched.
type EvidenceProof struct {
	CIRun     string `json:"ciRun"`
	TargetRun string `json:"targetRun"`
	Verdict   string `json:"verdict"`
}

// EvidenceSkipped is one target that was selected and did not run.
//
// ProvenBy is nil when nothing justifies the skip. That case is a coverage gap
// and it is reported rather than omitted — an attestation that quietly dropped
// its own violations would be worse than no attestation.
type EvidenceSkipped struct {
	Target     string         `json:"target"`
	TargetHash string         `json:"targetHash"`
	Reason     string         `json:"reason"`
	ProvenBy   *EvidenceProof `json:"provenBy"`
}

// Evidence is the predicate body. Every collection serialises as [] rather than
// null when empty — see MarshalJSON.
type Evidence struct {
	PredicateType string `json:"predicateType"`
	RunID         string `json:"runId"`
	Repo          string `json:"repo"`
	SHA           string `json:"sha,omitempty"`
	Trigger       string `json:"trigger,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`

	Resolutions []EvidenceResolution `json:"resolutions"`
	Affected    []EvidenceAffected   `json:"affected"`
	Executed    []EvidenceExecuted   `json:"executed"`
	Skipped     []EvidenceSkipped    `json:"skipped"`

	// CoverageGaps is the serialisation of queries/coverage.cypher: targets the
	// graph said were affected, which nothing ran, and for which no earlier
	// PASSED run exists to justify the skip. Empty is the assertion —
	//
	//	affected ⊆ executed ∪ proven-reusable
	//
	// — and it is the one field a gate should actually key on.
	CoverageGaps []string `json:"coverageGaps"`

	// UnresolvedPaths are changed paths nothing in the repo owns. A selection
	// with unresolved paths is not a complete answer about what changed, so the
	// verifier is told rather than left to assume the set was total.
	UnresolvedPaths []string `json:"unresolvedPaths"`
}

// MarshalJSON emits empty collections as [] instead of null.
//
// The invariant lives here rather than in whoever built the value, because it is
// a property of the DOCUMENT: a Rego policy asking `count(coverageGaps) == 0`
// must not have to distinguish "no gaps" from "field missing", and those are the
// same JSON once a nil slice gets through. One predicate assembled by a code
// path that forgot to initialise a slice is all it takes.
func (e Evidence) MarshalJSON() ([]byte, error) {
	// A local type with no methods, so json.Marshal below cannot recurse into
	// this one.
	type predicate Evidence
	p := predicate(e)
	if p.Resolutions == nil {
		p.Resolutions = []EvidenceResolution{}
	}
	if p.Affected == nil {
		p.Affected = []EvidenceAffected{}
	}
	if p.Executed == nil {
		p.Executed = []EvidenceExecuted{}
	}
	if p.Skipped == nil {
		p.Skipped = []EvidenceSkipped{}
	}
	if p.CoverageGaps == nil {
		p.CoverageGaps = []string{}
	}
	if p.UnresolvedPaths == nil {
		p.UnresolvedPaths = []string{}
	}
	return json.Marshal(p)
}

// EvidenceFromGraph reads a recorded CI run back out of Neo4j as a predicate.
//
// It runs after `record`, never during a run, and it is the only consumer of
// EvidenceQuery. A run that was never recorded is an error rather than an empty
// predicate: attesting to a run the graph has never heard of is the one output
// this must not produce.
func EvidenceFromGraph(ctx context.Context, d neo4j.DriverWithContext, runID string) (Evidence, error) {
	session := d.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database()})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, EvidenceQuery, map[string]any{"id": runID})
		if err != nil {
			return nil, err
		}
		rec, err := res.Single(ctx)
		if err != nil {
			return nil, fmt.Errorf("run %q is not in the graph; record it before asking for evidence: %w", runID, err)
		}
		return rec.AsMap(), nil
	})
	if err != nil {
		return Evidence{}, err
	}
	row, _ := result.(map[string]any)

	ev := Evidence{
		PredicateType:   EvidencePredicateType,
		RunID:           runID,
		Repo:            stringOf(row["repo"]),
		SHA:             stringOf(row["sha"]),
		Trigger:         stringOf(row["trigger"]),
		CreatedAt:       timeOf(row["createdAt"]),
		UnresolvedPaths: stringsOf(row["unresolvedPaths"]),
	}

	for _, m := range mapsOf(row["resolutions"]) {
		ev.Resolutions = append(ev.Resolutions, EvidenceResolution{
			Path:   stringOf(m["path"]),
			How:    stringOf(m["how"]),
			Target: stringOf(m["target"]),
		})
	}
	for _, m := range mapsOf(row["affected"]) {
		ev.Affected = append(ev.Affected, EvidenceAffected{
			Target:     stringOf(m["target"]),
			Reason:     stringOf(m["reason"]),
			Executed:   boolOf(m["executed"]),
			Runnable:   boolOf(m["runnable"]),
			TargetHash: stringOf(m["targetHash"]),
		})
	}
	for _, m := range mapsOf(row["executed"]) {
		ev.Executed = append(ev.Executed, EvidenceExecuted{
			Target:     stringOf(m["target"]),
			TargetHash: stringOf(m["targetHash"]),
			Verdict:    stringOf(m["verdict"]),
			DurationMs: int64Of(m["durationMs"]),
			CacheHit:   boolOf(m["cacheHit"]),
			Toolchain:  stringOf(m["toolchain"]),
		})
	}
	for _, m := range mapsOf(row["skipped"]) {
		s := EvidenceSkipped{
			Target:     stringOf(m["target"]),
			TargetHash: stringOf(m["targetHash"]),
			Reason:     stringOf(m["reason"]),
		}
		// A proof is only a proof if the earlier run is identifiable. Half a
		// citation — a TargetRun with no CIRun to look it up in — is not
		// checkable by a third party, so it does not count as one.
		if tr := stringOf(m["provenByTargetRun"]); tr != "" {
			s.ProvenBy = &EvidenceProof{
				CIRun:     stringOf(m["provenByRun"]),
				TargetRun: tr,
				Verdict:   stringOf(m["provenByVerdict"]),
			}
		} else {
			ev.CoverageGaps = append(ev.CoverageGaps, s.Target)
		}
		ev.Skipped = append(ev.Skipped, s)
	}

	sortEvidence(&ev)
	return ev, nil
}

// sortEvidence puts every collection in a stable order.
//
// Not cosmetic: this document gets signed. Neo4j makes no ordering promise, so
// two reads of an unchanged run would otherwise produce two different byte
// streams — different digests for identical facts, and a diff between two
// attestations that shows churn where nothing changed.
func sortEvidence(ev *Evidence) {
	sort.Slice(ev.Resolutions, func(i, j int) bool {
		if ev.Resolutions[i].Path != ev.Resolutions[j].Path {
			return ev.Resolutions[i].Path < ev.Resolutions[j].Path
		}
		return ev.Resolutions[i].Target < ev.Resolutions[j].Target
	})
	sort.Slice(ev.Affected, func(i, j int) bool { return ev.Affected[i].Target < ev.Affected[j].Target })
	sort.Slice(ev.Executed, func(i, j int) bool { return ev.Executed[i].Target < ev.Executed[j].Target })
	sort.Slice(ev.Skipped, func(i, j int) bool { return ev.Skipped[i].Target < ev.Skipped[j].Target })
	sort.Strings(ev.CoverageGaps)
	sort.Strings(ev.UnresolvedPaths)
}

// Covered reports the safety property as a single boolean:
//
//	affected ⊆ executed ∪ proven-reusable
//
// Same relation queries/coverage.cypher checks, evaluated over the predicate the
// verifier is holding rather than over the database it cannot reach.
func (e Evidence) Covered() bool { return len(e.CoverageGaps) == 0 }

// ProvenSkips is how many skips carry a citation.
func (e Evidence) ProvenSkips() int {
	n := 0
	for _, s := range e.Skipped {
		if s.ProvenBy != nil {
			n++
		}
	}
	return n
}

// EvidenceSubject is what `jf evd create` needs beyond the predicate itself.
//
// The subject is an Artifactory repo path plus a sha256, which is why the
// Merkle targetHash goes INSIDE the predicate rather than being used as the
// subject: a synthetic content hash is not addressable as one.
type EvidenceSubject struct {
	PredicateFile string
	RepoPath      string
	SHA256        string
	Key           string
	KeyAlias      string
}

// EvidenceCommand renders the `jf evd create` invocation that would upload a
// predicate.
//
// It lives in the tool rather than in the demo for the same reason `monograph
// queries` exists: bench/demotui puts this command on screen, and a demo that
// displays a hand-copied approximation of its own command invites the audience
// to check a claim against text that no longer matches anything.
//
// monograph never RUNS it. Whether Evidence signing is available on a given tier
// is listed in ADR-003 under "not verified, and therefore not designed on", and
// a branch that shelled out when `jf` happened to be on PATH would be designing
// on it.
func EvidenceCommand(s EvidenceSubject) string {
	s = s.withDefaults()
	return strings.Join([]string{
		"jf evd create --predicate " + s.PredicateFile + " \\",
		"  --predicate-type " + EvidencePredicateType + " \\",
		"  --subject-repo-path " + s.RepoPath + " \\",
		"  --subject-sha256 " + s.SHA256 + " \\",
		"  --key " + s.Key + " --key-alias " + s.KeyAlias,
	}, "\n")
}

// withDefaults fills unset fields with visibly fake placeholders.
//
// Angle brackets on purpose: a plausible-looking default (`libs-release-local`,
// `default-key`) would be pasted straight into a terminal and fail somewhere
// less obvious, or worse, succeed against the wrong subject.
func (s EvidenceSubject) withDefaults() EvidenceSubject {
	if s.PredicateFile == "" {
		s.PredicateFile = "evidence.json"
	}
	if s.RepoPath == "" {
		s.RepoPath = "<artifactory-repo>/<path-to-artifact>"
	}
	if s.SHA256 == "" {
		s.SHA256 = "<artifact-sha256>"
	}
	if s.Key == "" {
		s.Key = "<private-key-or-path>"
	}
	if s.KeyAlias == "" {
		s.KeyAlias = "<key-alias>"
	}
	return s
}

// ---------------------------------------------------------------- decoding
//
// The driver hands back `any`. These helpers do the narrowing in one place and
// return zero values rather than panicking: a property this tool did not write
// (an older run, a hand-edited node) should degrade one field, not the whole
// attestation.

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

func int64Of(v any) *int64 {
	n, ok := v.(int64)
	if !ok {
		return nil
	}
	return &n
}

func stringsOf(v any) []string {
	out := []string{}
	list, ok := v.([]any)
	if !ok {
		return out
	}
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func mapsOf(v any) []map[string]any {
	var out []map[string]any
	list, ok := v.([]any)
	if !ok {
		return out
	}
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// timeOf renders a Neo4j datetime as RFC 3339. CreatedAt is stamped server-side
// by RecordRun, so this is the run's own clock rather than the attester's.
func timeOf(v any) string {
	switch t := v.(type) {
	case time.Time:
		return t.Format(time.RFC3339)
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}
