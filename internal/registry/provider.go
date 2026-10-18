package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Release struct {
	Entry       Entry
	Size        int64
	DownloadURL string
}
type ReleaseProvider interface {
	ResolveRelease(context.Context, Submission) (Release, error)
	DownloadRelease(context.Context, Submission, Release, string) error
}

// GitLab supports public gitlab.com projects and project-owned generic packages.
// Publishers can modify these packages, but accepted registry records and checksums cannot change.
type GitLab struct{ client *http.Client }

func NewGitLab() *GitLab {
	return &GitLab{client: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("GitLab redirect refused") }}}
}
func releaseURL(s Submission, id int64) string {
	if s.Provider == "github" {
		return assetURL(s)
	}
	return "https://gitlab.com/api/v4/projects/" + strconv.FormatInt(id, 10) + "/packages/generic/actionlint-plugin/" + url.PathEscape(s.Tag) + "/" + url.PathEscape(s.Asset)
}
func (g *GitLab) get(ctx context.Context, path string, value any) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://gitlab.com/api/v4/"+path, nil)
	if err != nil {
		return err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitLab API HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return errors.New("GitLab API response too large")
	}
	return json.Unmarshal(data, value)
}
func (g *GitLab) ResolveRelease(ctx context.Context, s Submission) (Release, error) {
	if err := s.Validate(); err != nil {
		return Release{}, err
	}
	if s.Provider != "gitlab" {
		return Release{}, errors.New("GitLab provider mismatch")
	}
	var project struct {
		ID         int64  `json:"id"`
		Path       string `json:"path_with_namespace"`
		Visibility string `json:"visibility"`
		Namespace  struct {
			ID int64 `json:"id"`
		} `json:"namespace"`
	}
	if err := g.get(ctx, "projects/"+url.PathEscape(s.Repository), &project); err != nil {
		return Release{}, err
	}
	if project.ID <= 0 || project.Namespace.ID <= 0 || project.Visibility != "public" || !strings.EqualFold(project.Path, s.Repository) {
		return Release{}, errors.New("GitLab project must be public with canonical identity")
	}
	var release struct {
		Tag        string    `json:"tag_name"`
		ReleasedAt time.Time `json:"released_at"`
		Upcoming   bool      `json:"upcoming_release"`
		Commit     struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := g.get(ctx, "projects/"+strconv.FormatInt(project.ID, 10)+"/releases/"+url.PathEscape(s.Tag), &release); err != nil {
		return Release{}, err
	}
	if release.Tag != s.Tag || release.Upcoming || release.ReleasedAt.IsZero() || release.ReleasedAt.After(time.Now()) || !commitPattern.MatchString(release.Commit.ID) {
		return Release{}, errors.New("GitLab release must be published with matching tag and commit")
	}
	var tag struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := g.get(ctx, "projects/"+strconv.FormatInt(project.ID, 10)+"/repository/tags/"+url.PathEscape(s.Tag), &tag); err != nil {
		return Release{}, err
	}
	if tag.Name != s.Tag || tag.Commit.ID != release.Commit.ID {
		return Release{}, errors.New("GitLab release commit disagrees with current tag")
	}
	entry := Entry{Submission: s, RepositoryID: project.ID, OwnerID: project.Namespace.ID, Commit: release.Commit.ID, PublishedAt: release.ReleasedAt, URL: releaseURL(s, project.ID)}
	return Release{Entry: entry, DownloadURL: entry.URL}, nil
}
func (g *GitLab) DownloadRelease(ctx context.Context, s Submission, r Release, path string) (err error) {
	if r.DownloadURL != releaseURL(s, r.Entry.RepositoryID) {
		return errors.New("invalid GitLab package URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.DownloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitLab package HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, MaxArtifactBytes+1))
	if err = errors.Join(copyErr, f.Close()); err != nil {
		return err
	}
	if n == 0 || n > MaxArtifactBytes || hex.EncodeToString(hash.Sum(nil)) != s.SHA256 {
		return errors.New("GitLab package size or SHA256 mismatch")
	}
	return nil
}
