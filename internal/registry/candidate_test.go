package registry

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A local rehearsal must export the packaged bytes without inventing publication
// evidence. Reading the exported catalogue back checks its paths, metadata and
// digest; changing the input afterward must not alter the installable snapshot.
func TestCandidateExportsSnapshotWithoutPublication(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "plugin.wasm")
	body := []byte(sampleBody())
	if err := os.WriteFile(input, body, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "candidate")
	candidate, err := ExportCandidate(t.Context(), input, output, EmptyIndex())
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Artifact.SHA256 != sampleSubmission().SHA256 {
		t.Fatal("wrong snapshot digest")
	}
	var catalog Catalog
	if err = readRecord(filepath.Join(output, "index.json"), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Source != "candidate" || catalog.Statuses != "" || len(catalog.Plugins) != 1 {
		t.Fatalf("unexpected catalogue: %+v", catalog)
	}
	var manifest CandidateManifest
	manifestPath := filepath.Join(output, filepath.FromSlash(catalog.Plugins[0].Manifest))
	if err = readRecord(manifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Source != "candidate" || manifest.Name != "github.com/publisher/plugin" || len(manifest.Versions) != 1 || manifest.Versions[0].Version != "v1.0.0" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	var record Candidate
	if err = readRecord(filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(manifest.Versions[0].Record)), &record); err != nil {
		t.Fatal(err)
	}
	if record.Source != "candidate" || record.Artifact.SHA256 != candidate.Artifact.SHA256 {
		t.Fatal("record differs from inspected bytes")
	}
	if err = os.WriteFile(input, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(record.URL)))
	if err != nil || !bytes.Equal(snapshot, body) {
		t.Fatal("candidate did not snapshot input", err)
	}
	if _, err = (Store{Directory: filepath.Join(output, "plugins")}).Load(); err == nil {
		t.Fatal("candidate accepted as publication records")
	}
	if err = os.WriteFile(input, body, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(output, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ExportCandidate(t.Context(), input, output, EmptyIndex()); err == nil {
		t.Fatal("replaced existing output")
	}
	after, err := os.ReadFile(filepath.Join(output, "index.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed export changed catalogue")
	}
}

// Existing registrations must constrain a candidate before a release is made.
// These cases reject a changed plugin identity and replacement bytes for an
// accepted version, without creating a catalogue that CI could accidentally use.
func TestCandidateRejectsRegisteredConflicts(t *testing.T) {
	for _, mutation := range []string{"identity", "version"} {
		t.Run(mutation, func(t *testing.T) {
			entry := acceptedFixture(t)
			body := sampleBody()
			if mutation == "identity" {
				body = strings.Replace(body, `"id":"example"`, `"id":"changed"`, 1)
			} else {
				body = strings.Replace(body, "Example plugin", "Changed plugin", 1)
			}
			root := t.TempDir()
			input, output := filepath.Join(root, "plugin.wasm"), filepath.Join(root, "candidate")
			if err := os.WriteFile(input, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ExportCandidate(t.Context(), input, output, Index{SchemaVersion: 1, Entries: []Entry{entry}}); err == nil {
				t.Fatal("registered conflict accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("rejected candidate left a catalogue")
			}
		})
	}
}
