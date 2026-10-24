package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"registry.actionlint.kjanat.dev/internal/artifact"
)

// Candidate records local package contents without claiming a publisher identity
// or a release. Only explicit filesystem catalogues may contain these records.
type Candidate struct {
	Source   string               `json:"source"`
	Artifact artifact.Declaration `json:"artifact"`
	URL      string               `json:"url"`
}

type CandidateManifest struct {
	Source   string        `json:"source"`
	Name     string        `json:"name"`
	PluginID string        `json:"plugin_id"`
	Versions []VersionLink `json:"versions"`
}

// ExportCandidate snapshots one local package, inspects those exact bytes and
// exports an installable catalogue. It never writes accepted publication records.
func ExportCandidate(ctx context.Context, input, output string, existing Index) (candidate Candidate, err error) {
	if err = safePath(output); err != nil {
		return candidate, err
	}
	parent := filepath.Dir(output)
	if err = os.MkdirAll(parent, 0o700); err != nil {
		return candidate, err
	}
	staging, err := os.MkdirTemp(parent, ".candidate-*")
	if err != nil {
		return candidate, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(staging)) }()
	extension := ".wasm"
	if strings.HasSuffix(input, ".wasm.gz") {
		extension += ".gz"
	}
	snapshot := filepath.Join(staging, "package"+extension)
	checksum, err := snapshotFile(input, snapshot, MaxArtifactBytes, 0o600)
	if err != nil {
		return candidate, err
	}
	declaration, err := artifact.Inspect(ctx, snapshot)
	if err != nil {
		return candidate, err
	}
	if declaration.SHA256 != checksum {
		return candidate, errors.New("inspected candidate checksum changed")
	}
	name := strings.TrimPrefix(declaration.Repository, "https://")
	provider, repository, ok := strings.Cut(name, ".com/")
	version := "v" + strings.TrimPrefix(declaration.PluginVersion, "v")
	submission := Submission{SchemaVersion: 1, Provider: provider, Repository: repository,
		Tag: version, Asset: "plugin" + extension, SHA256: checksum, Kind: declaration.Kind}
	if !ok {
		return candidate, errors.New("candidate repository must use GitHub or GitLab")
	}
	if err = submission.Validate(); err != nil {
		return candidate, err
	}
	for _, old := range existing.Entries {
		if old.Submission.Provider != provider || !strings.EqualFold(old.Submission.Repository, repository) {
			continue
		}
		if old.PluginID() != declaration.PluginID {
			return candidate, reject("registered plugin identity changed")
		}
		if old.Submission.Tag == version && old.Submission.SHA256 != checksum {
			return candidate, reject("accepted version is immutable; use a new package version")
		}
	}
	candidate = Candidate{Source: "candidate", Artifact: declaration, URL: "artifacts/" + checksum + extension}
	if err = os.Mkdir(filepath.Join(staging, "artifacts"), 0o700); err != nil {
		return candidate, err
	}
	if err = os.Rename(snapshot, filepath.Join(staging, filepath.FromSlash(candidate.URL))); err != nil {
		return candidate, err
	}
	manifest := "plugins/" + key(name) + "/index.json"
	record := "versions/" + key(version) + ".json"
	if err = WriteJSON(filepath.Join(staging, filepath.FromSlash(strings.TrimSuffix(manifest, "index.json")+record)), candidate); err != nil {
		return candidate, err
	}
	if err = WriteJSON(filepath.Join(staging, filepath.FromSlash(manifest)), CandidateManifest{
		Source: "candidate", Name: name, PluginID: declaration.PluginID,
		Versions: []VersionLink{{Version: version, Record: record, Compatibility: []CompatibilityLink{}}},
	}); err != nil {
		return candidate, err
	}
	if err = writeCatalog(staging, Catalog{SchemaVersion: 1, Source: "candidate", Plugins: []CatalogPlugin{{Name: name, PluginID: declaration.PluginID, Manifest: manifest}}}); err != nil {
		return candidate, err
	}
	if _, err = os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return candidate, fmt.Errorf("candidate output must be a new directory: %s", output)
	}
	err = os.Rename(staging, output)
	return candidate, err
}
