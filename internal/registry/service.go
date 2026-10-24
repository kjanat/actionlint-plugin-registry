package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"registry.actionlint.kjanat.dev/internal/artifact"
)

type Inspector func(context.Context, string) (artifact.Declaration, error)

type Failure struct {
	Repository  string `json:"repository"`
	Tag         string `json:"tag,omitempty"`
	Error       string `json:"error"`
	Disposition string `json:"disposition"`
}

type Report struct {
	SchemaVersion int       `json:"schema_version"`
	Mode          string    `json:"mode"`
	SubmissionID  string    `json:"submission_id,omitempty"`
	Accepted      []Entry   `json:"accepted"`
	Rejected      []Failure `json:"rejected"`
	NoCandidates  []string  `json:"no_candidates,omitempty"`
}

type rejection struct{ message string }

func (e *rejection) Error() string { return e.message }

func reject(message string) error { return &rejection{message: message} }

// FailureDisposition defaults unknown, upstream, storage and transport failures
// to retryable. Only evidence that establishes invalid content or identity can
// permanently reject a submission.
func FailureDisposition(err error) string {
	var denied *rejection
	var invalid *artifact.InvalidError
	if errors.As(err, &denied) || errors.As(err, &invalid) {
		return "rejected"
	}
	return "retryable"
}

func NewReport(mode string) Report {
	return Report{SchemaVersion: 1, Mode: mode, Accepted: []Entry{}, Rejected: []Failure{}}
}

// WriteReport keeps accepted records within their storage bound when nested.
// Recovery reads one 2 MiB record plus a 32 KiB report envelope.
func WriteReport(path string, report Report) error {
	return writeJSON(path, report, false)
}

type Service struct {
	Providers map[string]ReleaseProvider
	Inspect   Inspector
}

func (service Service) Admit(ctx context.Context, index *Index, receipt Receipt) (Entry, error) {
	if err := receipt.Validate(); err != nil {
		return Entry{}, err
	}
	s := receipt.Submission
	provider, ok := service.Providers[s.Provider]
	if !ok {
		return Entry{}, errors.New("unsupported release provider")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	release, err := provider.ResolveRelease(ctx, s)
	if err != nil {
		return Entry{}, err
	}
	entry := release.Entry
	entry.Receipt = receipt
	if strconv.FormatInt(entry.RepositoryID, 10) != receipt.Identity.RepositoryID || strconv.FormatInt(entry.OwnerID, 10) != receipt.Identity.OwnerID || entry.Commit != receipt.Identity.Commit {
		return Entry{}, reject("release identity or commit disagrees with OIDC publisher")
	}
	for _, old := range index.Entries {
		if old.Receipt.PluginKey() != receipt.PluginKey() && (old.Submission.Provider != s.Provider || !strings.EqualFold(old.Submission.Repository, s.Repository)) {
			continue
		}
		if old.Receipt.PluginKey() != receipt.PluginKey() || old.OwnerID != entry.OwnerID ||
			old.RepositoryID != entry.RepositoryID || old.Submission.Provider != s.Provider ||
			old.Submission.Repository != s.Repository {
			return Entry{}, reject("registered publisher identity changed")
		}
		if old.Submission.Tag == s.Tag {
			if old.Submission != s || old.ReleaseID != entry.ReleaseID || old.AssetID != entry.AssetID || old.Commit != entry.Commit || old.URL != entry.URL {
				return Entry{}, reject("accepted version is immutable")
			}
			return old, nil
		}
	}
	declaration, err := func() (result artifact.Declaration, err error) {
		dir, err := os.MkdirTemp("", "actionlint-registry-")
		if err != nil {
			return result, err
		}
		defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
		path := filepath.Join(dir, s.Asset)
		if err = provider.DownloadRelease(ctx, s, release, path); err != nil {
			return result, err
		}
		inspect := service.Inspect
		if inspect == nil {
			inspect = artifact.Inspect
		}
		return inspect(ctx, path)
	}()
	if err != nil {
		return Entry{}, fmt.Errorf("plugin inspection: %w", err)
	}
	if declaration.Kind != s.Kind {
		return Entry{}, reject("binary kind disagrees with submission")
	}
	expectedRepository := "https://" + s.Provider + ".com/" + s.Repository
	if !strings.EqualFold(declaration.Repository, expectedRepository) || strings.TrimPrefix(declaration.PluginVersion, "v") != strings.TrimPrefix(s.Tag, "v") {
		return Entry{}, reject("embedded repository or version disagrees with authenticated release")
	}
	for _, old := range index.Entries {
		if old.Receipt.PluginKey() == receipt.PluginKey() && old.PluginID() != declaration.PluginID {
			return Entry{}, reject("registered plugin identity changed")
		}
	}
	entry.RecordVersion = 2
	entry.Artifact = &declaration
	entry.AcceptedAt = time.Now().UTC()
	if err = index.Add(entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}
