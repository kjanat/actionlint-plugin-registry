package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	ghapi "github.com/cli/go-gh/v2/pkg/api"
	"github.com/google/go-github/v83/github"
)

const GitHubAPIVersion = "2026-03-10"

type GitHub struct {
	api      *github.Client
	download *http.Client
}

func NewGitHub(token string) (*GitHub, error) {
	return newGitHub(token, http.DefaultTransport)
}

func newGitHub(token string, transport http.RoundTripper) (*GitHub, error) {
	client, err := ghapi.NewHTTPClient(ghapi.ClientOptions{
		Host: "github.com", APIHost: "api.github.com", AuthToken: token,
		Timeout: 30 * time.Second, LogIgnoreEnv: true,
		Transport: apiTransport{transport},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("GitHub API redirect refused; submit canonical repository identity")
		},
	})
	if err != nil {
		return nil, fmt.Errorf("GitHub client: %w", err)
	}
	return &GitHub{api: github.NewClient(client), download: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many asset redirects")
		}
		if req.URL.Scheme != "https" || req.URL.User != nil || (req.URL.Host != "github.com" && req.URL.Host != "release-assets.githubusercontent.com" && req.URL.Host != "objects.githubusercontent.com") {
			return errors.New("asset redirect outside GitHub release storage")
		}
		return nil
	}}}, nil
}

// Set the API version in the shared transport so neither SDK can override it
// with an older default. Limit response sizes before the typed SDK decodes them.
type apiTransport struct{ base http.RoundTripper }

func (t apiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "api.github.com" || req.URL.User != nil {
		return nil, errors.New("API request outside api.github.com")
	}
	req = req.Clone(req.Context())
	req.Header.Set("X-GitHub-Api-Version", GitHubAPIVersion)
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		resp.Body = http.MaxBytesReader(nil, resp.Body, 4<<20)
	}
	return resp, err
}

func assetURL(s Submission) string {
	return "https://github.com/" + strings.ToLower(s.Repository) + "/releases/download/" + url.PathEscape(s.Tag) + "/" + url.PathEscape(s.Asset)
}

// Resolve establishes identity from GitHub, not from plugin-authored metadata.
func (g *GitHub) Resolve(ctx context.Context, s Submission) (Entry, *github.ReleaseAsset, error) {
	if s.Provider != "github" {
		return Entry{}, nil, errors.New("GitHub provider mismatch")
	}
	if err := s.Validate(); err != nil {
		return Entry{}, nil, err
	}
	owner, name, _ := strings.Cut(s.Repository, "/")
	repo, _, err := g.api.Repositories.Get(ctx, owner, name)
	if err != nil {
		return Entry{}, nil, err
	}
	if repo.GetID() <= 0 || repo.GetPrivate() || !strings.EqualFold(repo.GetFullName(), s.Repository) {
		return Entry{}, nil, errors.New("repository must be public with matching canonical identity")
	}
	release, _, err := g.api.Repositories.GetReleaseByTag(ctx, owner, name, s.Tag)
	if err != nil {
		return Entry{}, nil, err
	}
	if release.GetID() <= 0 || release.GetDraft() || !release.GetImmutable() || release.GetTagName() != s.Tag || release.GetPublishedAt().IsZero() {
		return Entry{}, nil, errors.New("release must be published and immutable, with matching tag")
	}
	var chosen *github.ReleaseAsset
	for _, asset := range release.Assets {
		if asset.GetName() == s.Asset {
			if chosen != nil {
				return Entry{}, nil, errors.New("duplicate release asset")
			}
			chosen = asset
		}
	}
	if chosen.GetID() <= 0 || chosen.GetState() != "uploaded" || chosen.GetSize() <= 0 || int64(chosen.GetSize()) > MaxArtifactBytes || chosen.GetDigest() != "sha256:"+s.SHA256 {
		return Entry{}, nil, errors.New("asset missing, oversized, incomplete, or SHA256 disagrees with GitHub")
	}
	canonicalURL := strings.Replace(assetURL(s), strings.ToLower(s.Repository), repo.GetFullName(), 1)
	if chosen.GetBrowserDownloadURL() != assetURL(s) && chosen.GetBrowserDownloadURL() != canonicalURL {
		return Entry{}, nil, errors.New("asset URL does not match submitted release")
	}
	commit, _, err := g.api.Repositories.GetCommit(ctx, owner, name, s.Tag, nil)
	if err != nil {
		return Entry{}, nil, err
	}
	if !commitPattern.MatchString(commit.GetSHA()) {
		return Entry{}, nil, errors.New("invalid release commit")
	}
	return Entry{Submission: s, RepositoryID: repo.GetID(), OwnerID: repo.GetOwner().GetID(), UpstreamImmutable: true, ReleaseID: release.GetID(), AssetID: chosen.GetID(), Commit: commit.GetSHA(), URL: assetURL(s), PublishedAt: release.GetPublishedAt().Time}, chosen, nil
}

func (g *GitHub) Download(ctx context.Context, s Submission, asset *github.ReleaseAsset, path string) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.GetBrowserDownloadURL(), nil)
	if err != nil {
		return err
	}
	// No API credential is sent to release downloads or their CDN redirects.
	resp, err := g.download.Do(req)
	if err != nil {
		return errors.New("release asset download failed")
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("release asset download: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, MaxArtifactBytes+1))
	closeErr := f.Close()
	if err = errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if n != int64(asset.GetSize()) || n > MaxArtifactBytes || hex.EncodeToString(h.Sum(nil)) != s.SHA256 {
		return errors.New("download size or SHA256 does not match release asset")
	}
	return nil
}

// Candidates lists a limited number of recent releases. Publishers can submit older versions directly.
func (g *GitHub) Candidates(ctx context.Context, s Source) ([]Submission, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	owner, name, _ := strings.Cut(s.Repository, "/")
	releases, _, err := g.api.Repositories.ListReleases(ctx, owner, name, &github.ListOptions{PerPage: 20})
	if err != nil {
		return nil, err
	}
	var result []Submission
	for _, release := range releases {
		if release.GetDraft() || release.GetPrerelease() || !release.GetImmutable() {
			continue
		}
		for _, asset := range release.Assets {
			if asset.GetName() == s.Asset {
				candidate := Submission{1, "github", strings.ToLower(s.Repository), release.GetTagName(), s.Asset, strings.TrimPrefix(asset.GetDigest(), "sha256:"), s.Kind}
				if err := candidate.Validate(); err != nil {
					return nil, err
				}
				result = append(result, candidate)
			}
		}
	}
	return result, nil
}

// ResolveRelease resolves a GitHub release using its repository and tag, rather than a publisher-supplied URL.
func (g *GitHub) ResolveRelease(ctx context.Context, s Submission) (Release, error) {
	entry, asset, err := g.Resolve(ctx, s)
	if err != nil {
		return Release{}, err
	}
	return Release{Entry: entry, Size: int64(asset.GetSize()), DownloadURL: asset.GetBrowserDownloadURL()}, nil
}
func (g *GitHub) DownloadRelease(ctx context.Context, s Submission, r Release, path string) error {
	if r.Entry.URL != assetURL(s) || (r.DownloadURL != assetURL(s) && !strings.EqualFold(r.DownloadURL, assetURL(s))) {
		return errors.New("invalid GitHub artifact URL")
	}
	size, err := strconv.Atoi(strconv.FormatInt(r.Size, 10))
	if err != nil {
		return err
	}
	return g.Download(ctx, s, &github.ReleaseAsset{Size: github.Ptr(size), BrowserDownloadURL: github.Ptr(r.DownloadURL)}, path)
}
