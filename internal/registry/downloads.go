package registry

import (
	"context"
	"errors"
	"strings"
	"time"

	"registry.actionlint.kjanat.dev/internal/compatibility"
)

// DownloadObservation is mutable upstream usage data, never admission evidence.
// A nil count means unknown, not zero. Only the admitted artifact is counted.
type DownloadObservation struct {
	Provider     string `json:"provider"`
	Repository   string `json:"repository"`
	RepositoryID int64  `json:"repository_id"`
	Version      string `json:"version"`
	Asset        string `json:"asset"`
	AssetID      int64  `json:"asset_id"`
	SHA256       string `json:"sha256"`
	Status       string `json:"status"`
	Count        *int64 `json:"count,omitempty"`
}

type DownloadSnapshot struct {
	SchemaVersion int                   `json:"schema_version"`
	ObservedAt    time.Time             `json:"observed_at"`
	Releases      []DownloadObservation `json:"releases"`
}

// DownloadCount reads asset metadata without downloading the artifact. Matching
// the immutable asset ID and digest prevents counting a replacement release.
func (g *GitHub) DownloadCount(ctx context.Context, entry Entry) (int64, error) {
	if entry.Submission.Provider != "github" || entry.AssetID <= 0 {
		return 0, errors.New("download counts require a GitHub asset identity")
	}
	if err := entry.Submission.Validate(); err != nil {
		return 0, err
	}
	owner, name, _ := strings.Cut(entry.Submission.Repository, "/")
	asset, _, err := g.api.Repositories.GetReleaseAsset(ctx, owner, name, entry.AssetID)
	if err != nil {
		return 0, err
	}
	if asset.GetID() != entry.AssetID || asset.GetName() != entry.Submission.Asset ||
		asset.GetDigest() != "sha256:"+entry.Submission.SHA256 || asset.GetState() != "uploaded" ||
		asset.DownloadCount == nil || asset.GetDownloadCount() < 0 {
		return 0, errors.New("download count missing or asset identity changed")
	}
	return int64(asset.GetDownloadCount()), nil
}

type downloadCounter interface {
	DownloadCount(context.Context, Entry) (int64, error)
}

func collectDownloads(ctx context.Context, index Index, counter downloadCounter) DownloadSnapshot {
	snapshot := DownloadSnapshot{
		SchemaVersion: 1,
		ObservedAt:    time.Now().UTC(),
		Releases:      make([]DownloadObservation, 0, len(index.Entries)),
	}
	for _, entry := range index.Entries {
		observation := DownloadObservation{
			Provider: entry.Submission.Provider, Repository: entry.Submission.Repository,
			RepositoryID: entry.RepositoryID, Version: entry.Submission.Tag,
			Asset: entry.Submission.Asset, AssetID: entry.AssetID, SHA256: entry.Submission.SHA256,
			Status: "unsupported",
		}
		if observation.Provider == "github" {
			observation.Status = "unavailable"
			// A failed or rate-limited metrics request must not block publication.
			if ctx.Err() == nil {
				if count, err := counter.DownloadCount(ctx, entry); err == nil && count >= 0 {
					observation.Status = "available"
					observation.Count = &count
				}
			}
		}
		snapshot.Releases = append(snapshot.Releases, observation)
	}
	return snapshot
}

// ExportWithDownloadCounts keeps network-derived statistics separate from the
// immutable catalogue records. A bounded refresh cannot delay admission forever.
func ExportWithDownloadCounts(ctx context.Context, index Index, directory string, client *GitHub, statuses []ReleaseStatus, aliases *Aliases, evidence ...compatibility.Evidence) error {
	refresh, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	snapshot := collectDownloads(refresh, index, client)
	if err := ctx.Err(); err != nil {
		return err
	}
	return export(index, directory, statuses, &snapshot, aliases, evidence...)
}
