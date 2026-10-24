// Package registry accepts authenticated plugin releases after verifying publisher identity, checksums and discovery metadata.
package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"registry.actionlint.kjanat.dev/internal/artifact"
)

const MaxArtifactBytes int64 = 64 << 20

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	namePattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
	digestPattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	commitPattern     = regexp.MustCompile(`^[a-f0-9]{40}$`)
)

type Source struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Asset      string `json:"asset"`
	Kind       string `json:"kind"`
}

type Submission struct {
	SchemaVersion int    `json:"schema_version"`
	Provider      string `json:"provider"`
	Repository    string `json:"repository"`
	Tag           string `json:"tag"`
	Asset         string `json:"asset"`
	SHA256        string `json:"sha256"`
	Kind          string `json:"kind"`
}

func (s Source) Validate() error {
	if s.Provider != "github" && s.Provider != "gitlab" {
		return errors.New("provider must be github or gitlab")
	}
	if len(s.Repository) > 512 {
		return errors.New("repository exceeds 512 characters")
	}
	if s.Provider == "github" && !repositoryPattern.MatchString(s.Repository) || s.Provider == "gitlab" && !gitlabRepositoryPattern.MatchString(s.Repository) {
		return errors.New("repository must be owner/repository")
	}
	if !namePattern.MatchString(s.Asset) || !strings.HasSuffix(s.Asset, ".wasm") && !strings.HasSuffix(s.Asset, ".wasm.gz") {
		return errors.New("asset must be a .wasm or .wasm.gz filename")
	}
	if s.Kind != "core" && s.Kind != "component" {
		return errors.New("kind must be core or component")
	}
	return nil
}

func (s Submission) Source() Source { return Source{s.Provider, s.Repository, s.Asset, s.Kind} }

func (s Submission) Validate() error {
	if s.SchemaVersion != 1 {
		return errors.New("unsupported submission schema_version")
	}
	if err := s.Source().Validate(); err != nil {
		return err
	}
	if !namePattern.MatchString(s.Tag) {
		return errors.New("tag must contain only letters, digits, dot, underscore, plus or hyphen")
	}
	if !digestPattern.MatchString(s.SHA256) {
		return errors.New("sha256 must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func ParseSubmission(data []byte) (Submission, error) {
	var s Submission
	if len(data) > 16<<10 {
		return s, errors.New("submission exceeds 16 KiB")
	}
	if err := jsonv2.Unmarshal(data, &s, jsonv2.RejectUnknownMembers(true)); err != nil {
		return s, fmt.Errorf("submission JSON: %w", err)
	}
	s.Repository = strings.ToLower(s.Repository)
	return s, s.Validate()
}

var (
	gitlabRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)+$`)
	uuidPattern             = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	runIDPattern            = regexp.MustCompile(`^[1-9][0-9]*:[1-9][0-9]*$`)
	numericIDPattern        = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// Receipt records a submission claimed by an authenticated registry workflow.
// Parsing it does not verify OIDC. Callers must obtain it from the authenticated Worker response.
type Receipt struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	Submission    Submission        `json:"submission"`
	Identity      PublisherIdentity `json:"identity"`
	AuthorizedAt  time.Time         `json:"authorized_at"`
}
type PublisherIdentity struct {
	Issuer       string `json:"issuer"`
	Subject      string `json:"subject"`
	RepositoryID string `json:"repository_id"`
	OwnerID      string `json:"owner_id"`
	Repository   string `json:"repository"`
	Ref          string `json:"ref"`
	Commit       string `json:"commit"`
	Workflow     string `json:"workflow"`
	RunID        string `json:"run_id"`
}

func issuer(provider string) string {
	if provider == "github" {
		return "https://token.actions.githubusercontent.com"
	}
	if provider == "gitlab" {
		return "https://gitlab.com"
	}
	return ""
}
func (r Receipt) Validate() error {
	if err := r.Submission.Validate(); err != nil {
		return err
	}
	i := r.Identity
	if r.SchemaVersion != 1 || !uuidPattern.MatchString(r.ID) || r.AuthorizedAt.IsZero() || r.AuthorizedAt.After(time.Now().Add(time.Minute)) {
		return errors.New("invalid publication receipt")
	}
	if i.Issuer != issuer(r.Submission.Provider) || i.Subject == "" || len(i.Subject) > 2048 || !numericIDPattern.MatchString(i.RepositoryID) || !numericIDPattern.MatchString(i.OwnerID) || !strings.EqualFold(i.Repository, r.Submission.Repository) || i.Ref != "refs/tags/"+r.Submission.Tag || !commitPattern.MatchString(i.Commit) || i.Workflow == "" || len(i.Workflow) > 2048 || !runIDPattern.MatchString(i.RunID) {
		return errors.New("receipt identity does not authorize submitted tag")
	}
	if _, err := strconv.ParseInt(i.RepositoryID, 10, 64); err != nil {
		return errors.New("repository ID exceeds supported range")
	}
	if _, err := strconv.ParseInt(i.OwnerID, 10, 64); err != nil {
		return errors.New("owner ID exceeds supported range")
	}
	return nil
}
func ParseReceipt(data []byte) (Receipt, error) {
	var r Receipt
	if len(data) > 32<<10 {
		return r, errors.New("receipt exceeds 32 KiB")
	}
	if err := jsonv2.Unmarshal(data, &r, jsonv2.RejectUnknownMembers(true)); err != nil {
		return r, err
	}
	return r, r.Validate()
}
func key(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
func (r Receipt) PluginKey() string { return key(r.Identity.Issuer + "\n" + r.Identity.RepositoryID) }

// LegacyValidation preserves historical records without importing their host SDK.
type LegacyValidation struct {
	Kind          string          `json:"kind"`
	PluginID      string          `json:"plugin_id"`
	PluginVersion string          `json:"plugin_version,omitempty"`
	Rules         []string        `json:"rules"`
	Checks        []string        `json:"checks"`
	HostCommit    string          `json:"host_commit"`
	Metadata      json.RawMessage `json:"metadata"`
	Repository    string          `json:"repository"`
	Contract      string          `json:"contract"`
	PolicyVersion string          `json:"policy_version"`
}

type Entry struct {
	RecordVersion     int                   `json:"record_version,omitempty"`
	Artifact          *artifact.Declaration `json:"artifact,omitempty"`
	Submission        Submission            `json:"submission"`
	Receipt           Receipt               `json:"receipt"`
	OwnerID           int64                 `json:"owner_id"`
	UpstreamImmutable bool                  `json:"upstream_immutable"`
	RepositoryID      int64                 `json:"repository_id"`
	ReleaseID         int64                 `json:"release_id"`
	AssetID           int64                 `json:"asset_id"`
	Commit            string                `json:"commit"`
	URL               string                `json:"url"`
	PublishedAt       time.Time             `json:"published_at"`
	AcceptedAt        time.Time             `json:"accepted_at"`
	Validation        *LegacyValidation     `json:"validation,omitempty"`
}

func (e Entry) PluginID() string {
	if e.Artifact != nil {
		return e.Artifact.PluginID
	}
	if e.Validation != nil {
		return e.Validation.PluginID
	}
	return ""
}

func (e Entry) declaration() (repository, version string, err error) {
	switch {
	case e.RecordVersion == 2 && e.Artifact != nil && e.Validation == nil:
		if err = e.Artifact.Validate(); err != nil {
			return "", "", err
		}
		if e.Artifact.Kind != e.Submission.Kind || e.Artifact.SHA256 != e.Submission.SHA256 {
			return "", "", errors.New("artifact disagrees with submission")
		}
		return e.Artifact.Repository, e.Artifact.PluginVersion, nil
	case e.RecordVersion == 0 && e.Artifact == nil && e.Validation != nil:
		v := e.Validation
		if !commitPattern.MatchString(v.HostCommit) || v.Kind != e.Submission.Kind || v.PluginID == "" || len(v.Rules) == 0 || len(v.Checks) == 0 || len(v.Metadata) == 0 || !json.Valid(v.Metadata) || v.Repository == "" || v.PluginVersion == "" || v.Contract == "" || v.PolicyVersion != "1" {
			return "", "", errors.New("invalid historical validation evidence")
		}
		return v.Repository, v.PluginVersion, nil
	default:
		return "", "", errors.New("invalid release record version or declaration")
	}
}

func (e Entry) PinnedURL() string { return e.URL + "#sha256=" + e.Submission.SHA256 }

type Index struct {
	SchemaVersion int     `json:"schema_version"`
	Entries       []Entry `json:"entries"`
}

func EmptyIndex() Index { return Index{SchemaVersion: 1, Entries: []Entry{}} }

func (i *Index) Add(e Entry) error {
	if i.SchemaVersion != 1 {
		return errors.New("unsupported index schema_version")
	}
	if err := e.Submission.Validate(); err != nil {
		return err
	}
	if err := e.Receipt.Validate(); err != nil {
		return err
	}
	if e.Receipt.Submission != e.Submission || strconv.FormatInt(e.RepositoryID, 10) != e.Receipt.Identity.RepositoryID || strconv.FormatInt(e.OwnerID, 10) != e.Receipt.Identity.OwnerID || e.Commit != e.Receipt.Identity.Commit {
		return errors.New("entry disagrees with publication identity")
	}
	if e.RepositoryID <= 0 || !commitPattern.MatchString(e.Commit) || e.URL != releaseURL(e.Submission, e.RepositoryID) || e.PublishedAt.IsZero() || e.AcceptedAt.IsZero() {
		return errors.New("entry lacks valid release identity")
	}
	repository, version, err := e.declaration()
	if err != nil {
		return err
	}
	expectedRepository := "https://" + e.Submission.Provider + ".com/" + e.Submission.Repository
	if !strings.EqualFold(repository, expectedRepository) || strings.TrimPrefix(version, "v") != strings.TrimPrefix(e.Submission.Tag, "v") {
		return errors.New("embedded metadata disagrees with release identity")
	}
	if e.Submission.Provider == "github" && (e.ReleaseID <= 0 || e.AssetID <= 0 || !e.UpstreamImmutable) {
		return errors.New("GitHub entry lacks immutable release identity")
	}
	if e.Submission.Provider == "gitlab" && e.UpstreamImmutable {
		return errors.New("GitLab generic packages do not guarantee upstream immutability")
	}
	for _, old := range i.Entries {
		if old.Receipt.PluginKey() != e.Receipt.PluginKey() && (old.Submission.Provider != e.Submission.Provider || !strings.EqualFold(old.Submission.Repository, e.Submission.Repository)) {
			continue
		}
		if pluginFor(old) != pluginFor(e) {
			return reject("registered publisher or plugin identity changed")
		}
		if old.Submission.Tag == e.Submission.Tag {
			if old.Submission != e.Submission || old.ReleaseID != e.ReleaseID || old.AssetID != e.AssetID || old.Commit != e.Commit || old.URL != e.URL || old.UpstreamImmutable != e.UpstreamImmutable {
				return reject("accepted version is immutable; refusing replacement")
			}
			return nil
		}
	}
	i.Entries = append(i.Entries, e)
	slices.SortFunc(i.Entries, func(a, b Entry) int {
		if n := strings.Compare(a.Submission.Repository, b.Submission.Repository); n != 0 {
			return n
		}
		return strings.Compare(a.Submission.Tag, b.Submission.Tag)
	})
	return nil
}

func WriteJSON(path string, value any) (err error) {
	if err = safePath(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".registry-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() {
		if removeErr := os.Remove(name); !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return errors.Join(err, f.Close())
	}
	if err = f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
