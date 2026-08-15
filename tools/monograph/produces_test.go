package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"proto/gen/go/**", "proto/gen/go/userpb/user.pb.go", true},
		{"proto/gen/go/**", "proto/gen/go", true},
		{"proto/gen/go/**", "proto/gen/ts/src/index.ts", false},
		{"proto/gen/go/**", "proto/user.proto", false},
		// A trailing /** must not match a sibling sharing a name prefix.
		{"proto/gen/go/**", "proto/gen/golang/x.go", false},
		{"**/*.pb.go", "proto/gen/go/userpb/user.pb.go", true},
		{"**/*.pb.go", "proto/gen/go/userpb/user.go", false},
		{"proto/*.proto", "proto/user.proto", true},
		{"proto/*.proto", "proto/nested/user.proto", false},
		{"proto/**/*.go", "proto/gen/go/userpb/user.pb.go", true},
	}
	for _, tc := range cases {
		if got := matchGlob(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

// TestGeneratedFilesExcludedFromHash is the central property of `produces`:
// outputs must not contribute to their own target's content hash. Otherwise the
// hash depends on whether generated code happens to be present in the worktree,
// and a stale artifact silently changes the cache key.
func TestGeneratedFilesExcludedFromHash(t *testing.T) {
	withGen := loadTestGraph(t)

	var foundGenerated bool
	for _, f := range withGen.Files {
		if f.Generated {
			foundGenerated = true
			break
		}
	}
	if !foundGenerated {
		t.Skip("no generated files present; run scripts/generate-local.sh first")
	}

	// Same graph with every generated file dropped, as if codegen had not run.
	withoutGen := loadTestGraph(t)
	kept := withoutGen.Files[:0]
	for _, f := range withoutGen.Files {
		if !f.Generated {
			kept = append(kept, f)
		}
	}
	withoutGen.Files = kept
	for i := range withoutGen.Targets {
		withoutGen.Targets[i].TargetHash = ""
	}
	if err := ComputeHashes(withoutGen); err != nil {
		t.Fatalf("ComputeHashes: %v", err)
	}

	before := map[string]string{}
	for _, tg := range withGen.Targets {
		before[tg.Name] = tg.TargetHash
	}
	for _, tg := range withoutGen.Targets {
		if before[tg.Name] != tg.TargetHash {
			t.Errorf("%s hash changed when generated output was removed (%s vs %s); the hash is not input-only",
				tg.Name, before[tg.Name], tg.TargetHash)
		}
	}
}

// TestGeneratedFilesAreMarked checks the proto target really claims its output.
func TestGeneratedFilesAreMarked(t *testing.T) {
	g := loadTestGraph(t)

	var proto Target
	for _, tg := range g.Targets {
		if tg.Name == "proto" {
			proto = tg
		}
	}
	if len(proto.Produces) == 0 {
		t.Fatal("proto target declares no produces globs")
	}
	if proto.CodegenCmd == "" {
		t.Error("proto declares produces but no codegenCmd")
	}

	for _, f := range g.Files {
		wantGenerated := f.TargetName == "proto" && matchAnyGlob(proto.Produces, f.Path)
		if f.Generated != wantGenerated {
			t.Errorf("%s: generated = %v, want %v", f.Path, f.Generated, wantGenerated)
		}
	}
}

// writeManifest builds a throwaway target dir for validator tests.
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	sub := filepath.Join(dir, "thing")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, "monograph.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManifestValidation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "declared dependencies are rejected",
			body:    "name = \"thing\"\nkind = \"go-lib\"\ndeps = [\"other\"]\n",
			wantErr: "dependencies are derived from source",
		},
		{
			name:    "produces without codegenCmd is rejected",
			body:    "name = \"thing\"\nkind = \"proto\"\nimage = \"alpine:3\"\nproduces = [\"thing/gen/**\"]\n",
			wantErr: "no codegenCmd",
		},
		{
			name:    "codegenCmd without produces is rejected",
			body:    "name = \"thing\"\nkind = \"proto\"\nimage = \"alpine:3\"\ncodegenCmd = \"make gen\"\n",
			wantErr: "declares no produces",
		},
		{
			name:    "produces outside the target root is rejected",
			body:    "name = \"thing\"\nkind = \"proto\"\nimage = \"alpine:3\"\ncodegenCmd = \"x\"\nproduces = [\"elsewhere/**\"]\n",
			wantErr: "outside target root",
		},
		{
			name:    "codegenCmd without an image is rejected",
			body:    "name = \"thing\"\nkind = \"proto\"\ncodegenCmd = \"x\"\nproduces = [\"thing/gen/**\"]\n",
			wantErr: "no image",
		},
		{
			name:    "a valid producing target is accepted",
			body:    "name = \"thing\"\nkind = \"proto\"\nimage = \"alpine:3\"\ncodegenCmd = \"x\"\nproduces = [\"thing/gen/**\"]\n",
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := readManifest(writeManifest(t, tc.body))
			if err == nil {
				_, err = targetFromManifest("thing", m)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("expected error containing %q, got none", tc.wantErr)
			case tc.wantErr != "" && !contains(err.Error(), tc.wantErr):
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestManifestArrays covers the array forms the parser must accept.
func TestManifestArrays(t *testing.T) {
	body := `name = "thing"
kind = "proto"
image = "alpine:3"
codegenCmd = "x"
produces = [
  "thing/gen/go/**",   # trailing comment
  "thing/gen/ts/**",
]
`
	m, err := readManifest(writeManifest(t, body))
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	got := m.list("produces")
	want := []string{"thing/gen/go/**", "thing/gen/ts/**"}
	if len(got) != len(want) {
		t.Fatalf("produces = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("produces[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// TestSymlinkContentIsHashed pins that a symlink contributes the path it points
// at to its target's hash.
//
// This was a live defect with the worst possible failure direction. discoverFiles
// skipped every non-regular entry, so a symlink was never indexed and never
// hashed. Repointing a committed symlink therefore produced a byte-identical
// targetHash: git saw a content change, monograph did not, and with reuse enabled
// the target matched an earlier PASSED run and was skipped entirely. Changed
// code, silently never tested.
//
// Hashing the link destination is also what git does — it stores the target path
// as the blob content — so this keeps the two notions of "changed" aligned.
func TestSymlinkContentIsHashed(t *testing.T) {
	hashWithLink := func(dest string) (string, []File) {
		t.Helper()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := "name = \"pkg\"\nkind = \"generic\"\nimage = \"alpine:3\"\ntestCmd = \"true\"\n"
		if err := os.WriteFile(filepath.Join(root, "pkg", "monograph.toml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "pkg", "real.txt"), []byte("stable\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dest, filepath.Join(root, "pkg", "link.txt")); err != nil {
			t.Skipf("symlinks unavailable on this filesystem: %v", err)
		}
		g, err := Extract("t", root)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if err := ComputeHashes(g); err != nil {
			t.Fatalf("ComputeHashes: %v", err)
		}
		for _, tg := range g.Targets {
			if tg.Name == "pkg" {
				return tg.TargetHash, g.Files
			}
		}
		t.Fatal("target pkg not found")
		return "", nil
	}

	first, files := hashWithLink("./real.txt")

	var indexed bool
	for _, f := range files {
		if f.Path == "pkg/link.txt" {
			indexed = true
		}
	}
	if !indexed {
		t.Error("the symlink is not in the file index, so nothing about it can reach the hash")
	}

	second, _ := hashWithLink("./elsewhere.txt")
	if first == second {
		t.Errorf("targetHash unchanged (%s) after repointing the symlink; a repointed link would be reported reusable and skipped", first)
	}
}
