package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"
	"registry.actionlint.kjanat.dev/internal/artifact"
)

func acceptedFixture(t *testing.T) Entry {
	t.Helper()
	service := Service{Providers: map[string]ReleaseProvider{"github": fixtureGitHub(t, func(*github.RepositoryRelease) {})}, Inspect: func(context.Context, string) (artifact.Declaration, error) {
		return sampleDeclaration(), nil
	}}
	index := EmptyIndex()
	entry, err := service.Admit(t.Context(), &index, sampleReceipt())
	if err != nil {
		t.Fatal(err)
	}
	return entry
}
func TestReceiptBindingBeforeValidation(t *testing.T) {
	for name, mutate := range map[string]func(*Receipt){
		"missing":    func(r *Receipt) { *r = Receipt{} },
		"issuer":     func(r *Receipt) { r.Identity.Issuer = "https://attacker.example" },
		"branch":     func(r *Receipt) { r.Identity.Ref = "refs/heads/master" },
		"project":    func(r *Receipt) { r.Identity.RepositoryID = "11" },
		"owner":      func(r *Receipt) { r.Identity.OwnerID = "6" },
		"commit":     func(r *Receipt) { r.Identity.Commit = strings.Repeat("b", 40) },
		"repository": func(r *Receipt) { r.Identity.Repository = "other/plugin" },
	} {
		t.Run(name, func(t *testing.T) {
			service := Service{Providers: map[string]ReleaseProvider{"github": fixtureGitHub(t, func(*github.RepositoryRelease) {})}, Inspect: func(context.Context, string) (artifact.Declaration, error) {
				t.Fatal("unauthorized publication reached inspection")
				return artifact.Declaration{}, nil
			}}
			receipt := sampleReceipt()
			mutate(&receipt)
			index := EmptyIndex()
			if _, err := service.Admit(t.Context(), &index, receipt); err == nil || len(index.Entries) != 0 {
				t.Fatal("unauthorized publication accepted")
			}
		})
	}
}
func TestStoreAndCatalogIsolation(t *testing.T) {
	entry := acceptedFixture(t)
	index := EmptyIndex()
	if err := index.Add(entry); err != nil {
		t.Fatal(err)
	}
	store := Store{Directory: filepath.Join(t.TempDir(), "plugins")}
	if err := store.Merge(index); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory, entry.Receipt.PluginKey(), "versions", key(entry.Submission.Tag)+".json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	repeat := entry
	repeat.Receipt.ID = "00000000-0000-4000-8000-000000000002"
	repeat.AcceptedAt = time.Now()
	delta := EmptyIndex()
	if err = delta.Add(repeat); err != nil {
		t.Fatal(err)
	}
	if err = store.Merge(delta); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatal("idempotent receipt rewrote accepted evidence")
	}
	output := t.TempDir()
	if err = Export(index, output, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "validation") || strings.Contains(string(data), entry.Submission.SHA256) || strings.Contains(string(data), "receipt") {
		t.Fatal("root catalog embeds version evidence")
	}
	if err = os.Rename(path, filepath.Join(filepath.Dir(path), strings.Repeat("0", 64)+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(); err == nil {
		t.Fatal("accepted mismatched version path")
	}
}
func TestMergeValidatesWholeDeltaBeforeWriting(t *testing.T) {
	entry := acceptedFixture(t)
	invalid := entry
	invalid.Submission.Tag = "v2" // receipt disagrees
	store := Store{Directory: filepath.Join(t.TempDir(), "plugins")}
	if err := store.Merge(Index{SchemaVersion: 1, Entries: []Entry{entry, invalid}}); err == nil {
		t.Fatal("invalid candidate accepted")
	}
	if _, err := os.Stat(store.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial merge wrote records")
	}
}

func TestMergeRejectsUnreadableRecordBeforeWriting(t *testing.T) {
	entry := acceptedFixture(t)
	entry.Artifact.Metadata = json.RawMessage(`{"padding":"` + strings.Repeat("x", maxRecordBytes) + `"}`)
	store := Store{Directory: filepath.Join(t.TempDir(), "plugins")}
	if err := store.Merge(Index{SchemaVersion: 1, Entries: []Entry{entry}}); err == nil {
		t.Fatal("oversized serialized record accepted")
	}
	if _, err := os.Stat(store.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversized record mutated store")
	}
}
func TestStoreRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := (Store{Directory: link}).Load(); err == nil {
		t.Fatal("symlink store accepted")
	}
}
func TestGitLabPublication(t *testing.T) {
	submission := sampleSubmission()
	submission.Provider = "gitlab"
	submission.Repository = "publisher/group/plugin"
	gitlab := NewGitLab()
	gitlab.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("credential attached to public GitLab request")
		}
		switch r.URL.Path {
		case "/api/v4/projects/publisher/group/plugin":
			return response(r, `{"id":10,"namespace":{"id":5},"path_with_namespace":"publisher/group/plugin","visibility":"public"}`), nil
		case "/api/v4/projects/10/releases/v1.0.0":
			return response(r, `{"tag_name":"v1.0.0","released_at":"2026-01-01T00:00:00Z","commit":{"id":"`+strings.Repeat("a", 40)+`"}}`), nil
		case "/api/v4/projects/10/repository/tags/v1.0.0":
			return response(r, `{"name":"v1.0.0","commit":{"id":"`+strings.Repeat("a", 40)+`"}}`), nil
		case "/api/v4/projects/10/packages/generic/actionlint-plugin/v1.0.0/plugin.wasm":
			return response(r, sampleBody()), nil
		default:
			return nil, errors.New("unexpected GitLab request")
		}
	})
	release, err := gitlab.ResolveRelease(t.Context(), submission)
	if err != nil {
		t.Fatal(err)
	}
	if release.Entry.UpstreamImmutable || release.Entry.OwnerID != 5 {
		t.Fatal("incorrect GitLab identity or immutability guarantee")
	}
	if err = gitlab.DownloadRelease(t.Context(), submission, release, filepath.Join(t.TempDir(), submission.Asset)); err != nil {
		t.Fatal(err)
	}
	release.DownloadURL = "https://attacker.example/plugin.wasm"
	if err = gitlab.DownloadRelease(t.Context(), submission, release, filepath.Join(t.TempDir(), submission.Asset)); err == nil {
		t.Fatal("arbitrary download URL accepted")
	}
	req, err := http.NewRequest(http.MethodGet, "https://attacker.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = gitlab.client.CheckRedirect(req, nil); err == nil {
		t.Fatal("GitLab redirect accepted")
	}
}
