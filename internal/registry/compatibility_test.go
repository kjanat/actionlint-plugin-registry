package registry

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"registry.actionlint.kjanat.dev/internal/compatibility"
)

func observation(entry Entry, outcome string) compatibility.Evidence {
	detail := ""
	if outcome != "passed" {
		detail = "This checker cannot run that contract."
	}
	return compatibility.Evidence{Report: compatibility.Report{SchemaVersion: 1, ArtifactSHA256: entry.Submission.SHA256, Host: compatibility.Host{Name: "test-host", Revision: "revision-a"}, Policy: "smoke/1", Outcome: outcome, Checks: []string{"host-abi"}, Detail: detail}, CheckerSHA256: strings.Repeat("a", 64), ObservedAt: time.Now().UTC()}
}
func TestCompatibilityEvidenceDoesNotRewriteRelease(t *testing.T) {
	entry := acceptedFixture(t)
	index := EmptyIndex()
	if err := index.Add(entry); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	releases := Store{Directory: filepath.Join(directory, "plugins")}
	if err := releases.Merge(index); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(releases.Directory, entry.Receipt.PluginKey(), "versions", key(entry.Submission.Tag)+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store := EvidenceStore{Directory: filepath.Join(directory, "compatibility")}
	for _, outcome := range []string{"passed", "failed", "unsupported"} {
		e := observation(entry, outcome)
		if err = store.Add(index, e); err != nil {
			t.Fatal(err)
		}
		if err = store.Add(index, e); err != nil {
			t.Fatal("duplicate report not idempotent:", err)
		}
	}
	observations, err := store.Load(index)
	if err != nil || len(observations) != 3 {
		t.Fatalf("observations: %v, %v", observations, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("compatibility check rewrote release")
	}
	output := t.TempDir()
	if err = Export(index, output, observations...); err != nil {
		t.Fatal(err)
	}
	var manifest PluginManifest
	manifestPath := filepath.Join(output, "plugins", entry.Receipt.PluginKey(), "index.json")
	if err = readRecord(manifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Versions[0].Compatibility) != 3 {
		t.Fatal("missing compatibility links")
	}
	for _, link := range manifest.Versions[0].Compatibility {
		if _, err = os.Stat(filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(link.Report))); err != nil {
			t.Fatal("broken report link:", err)
		}
	}
	forged := observation(entry, "passed")
	forged.Report.ArtifactSHA256 = strings.Repeat("b", 64)
	if err = store.Add(index, forged); err == nil {
		t.Fatal("evidence attached to unknown artifact")
	}
	// Altering a record in place breaks its content-derived identity.
	name, err := evidenceName(observations[0])
	if err != nil {
		t.Fatal(err)
	}
	changed := observations[0]
	changed.Report.Host.Revision = "forged"
	if err = WriteJSON(filepath.Join(store.Directory, entry.Submission.SHA256, name), changed); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(index); err == nil {
		t.Fatal("altered host identity accepted at original path")
	}
}
func TestUnknownABIIsUntestedWithoutReports(t *testing.T) {
	entry := acceptedFixture(t)
	index := EmptyIndex()
	if err := index.Add(entry); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err := Export(index, output); err != nil {
		t.Fatal(err)
	}
	var manifest PluginManifest
	if err := readRecord(filepath.Join(output, "plugins", entry.Receipt.PluginKey(), "index.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Versions[0].Compatibility == nil || len(manifest.Versions[0].Compatibility) != 0 {
		t.Fatal("unknown contract was presented as tested")
	}
}
func TestLegacyRecordsRetainTheirOriginalBytes(t *testing.T) {
	entry := acceptedFixture(t)
	declared := entry.Artifact
	entry.RecordVersion = 0
	entry.Artifact = nil
	entry.Validation = &LegacyValidation{Kind: declared.Kind, PluginID: declared.PluginID, PluginVersion: declared.PluginVersion, Repository: declared.Repository, Metadata: declared.Metadata, Contract: "wasm-core-json/1", Rules: []string{"example"}, Checks: []string{"host-abi"}, HostCommit: strings.Repeat("a", 40), PolicyVersion: "1"}
	index := EmptyIndex()
	if err := index.Add(entry); err != nil {
		t.Fatal(err)
	}
	store := Store{Directory: filepath.Join(t.TempDir(), "plugins")}
	if err := store.Merge(index); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory, entry.Receipt.PluginKey(), "versions", key(entry.Submission.Tag)+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Read and export a historical record without adding modern fields.
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err = Export(loaded, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err = store.Merge(loaded); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || bytes.Contains(after, []byte(`"record_version"`)) || bytes.Contains(after, []byte(`"artifact"`)) {
		t.Fatal("historical record changed")
	}
}
func TestRegistryHasNoHostSDKDependency(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "./cmd/registry")
	cmd.Dir = "../.."
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "actionlint.kjanat.dev/") || strings.Contains(line, "/adapters/") {
			t.Fatalf("registry imports host SDK: %s", line)
		}
	}
}
func TestCompatibilityReportStrictProtocol(t *testing.T) {
	e := observation(acceptedFixture(t), "passed")
	for _, mutate := range []func(*compatibility.Report){
		func(r *compatibility.Report) { r.SchemaVersion = 99 },
		func(r *compatibility.Report) { r.Outcome = "maybe" },
		func(r *compatibility.Report) { r.Host.Revision = "" },
		func(r *compatibility.Report) { r.Policy = "" },
		func(r *compatibility.Report) { r.Checks = nil },
	} {
		changed := e.Report
		mutate(&changed)
		if err := changed.Validate(); err == nil {
			data, _ := json.Marshal(changed)
			t.Fatalf("invalid report accepted: %s", data)
		}
	}
}
