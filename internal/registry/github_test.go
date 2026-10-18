package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"
	"registry.actionlint.kjanat.dev/internal/validation"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func sampleSubmission() Submission {
	digest := sha256.Sum256([]byte("wasm sample"))
	return Submission{1, "github", "publisher/plugin", "v1.0.0", "plugin.wasm", hex.EncodeToString(digest[:]), "core"}
}

func fixtureGitHub(t *testing.T, mutate func(*github.RepositoryRelease)) *GitHub {
	t.Helper()
	s := sampleSubmission()
	client, err := newGitHub("test-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "token test-token" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Errorf("missing auth or API version: %v", r.Header)
		}
		switch r.URL.Path {
		case "/repos/publisher/plugin":
			return response(r, `{"id":10,"full_name":"Publisher/Plugin","private":false,"owner":{"id":5}}`), nil
		case "/repos/publisher/plugin/releases/tags/v1.0.0", "/repos/publisher/plugin/releases":
			release := github.RepositoryRelease{
				ID: 20, TagName: s.Tag, Immutable: new(true), PublishedAt: &github.Timestamp{Time: time.Now().UTC()},
				Assets: []*github.ReleaseAsset{{ID: new(int64(30)), Name: new(s.Asset), State: new("uploaded"), Size: new(len("wasm sample")), Digest: new("sha256:" + s.SHA256), BrowserDownloadURL: new(assetURL(s))}},
			}
			mutate(&release)
			var data []byte
			var err error
			if r.URL.Path == "/repos/publisher/plugin/releases" {
				if r.URL.Query().Get("per_page") != "20" {
					t.Error("release discovery is not bounded")
				}
				data, err = json.Marshal([]github.RepositoryRelease{release})
			} else {
				data, err = json.Marshal(release)
			}
			return response(r, string(data)), err
		case "/repos/publisher/plugin/commits/v1.0.0":
			return response(r, `{"sha":"`+strings.Repeat("a", 40)+`"}`), nil
		default:
			t.Errorf("unexpected request %s", r.URL)
			return nil, errors.New("unexpected request")
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	client.download.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.URL.String() != assetURL(s) {
			t.Errorf("unexpected download request %s, headers %v", r.URL, r.Header)
		}
		return response(r, "wasm sample"), nil
	})
	return client
}

func TestAdmissionAndImmutableIdentity(t *testing.T) {
	client := fixtureGitHub(t, func(*github.RepositoryRelease) {})
	index := EmptyIndex()
	calls := 0
	service := Service{Providers: map[string]ReleaseProvider{"github": client}, Validate: func(_ context.Context, path string) (validation.Result, error) {
		calls++
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "wasm sample" {
			t.Fatalf("validator received wrong snapshot: %q, %v", data, err)
		}
		return validation.Result{Kind: "core", PluginID: "example", Rules: []string{"example"}, Checks: []string{"host-abi"}, HostCommit: validation.HostCommit, PluginVersion: "1.0.0", Repository: "https://github.com/publisher/plugin", Metadata: json.RawMessage(`{"id":"example"}`), Contract: "wasm-core-json/1", PolicyVersion: "1"}, nil
	}}
	s := sampleSubmission()
	entry, err := service.Admit(t.Context(), &index, sampleReceipt())
	if err != nil {
		t.Fatal(err)
	}
	if entry.RepositoryID != 10 || entry.ReleaseID != 20 || entry.AssetID != 30 || entry.Validation.HostCommit != validation.HostCommit {
		t.Fatalf("missing authoritative identity: %+v", entry)
	}
	if _, err = service.Admit(t.Context(), &index, sampleReceipt()); err != nil || calls != 1 || len(index.Entries) != 1 {
		t.Fatalf("repeat admission must be idempotent: %v, calls %d", err, calls)
	}
	store := Store{Directory: filepath.Join(t.TempDir(), "plugins")}
	if err = store.Merge(index); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded.Entries) != 1 {
		t.Fatalf("persisted index unreadable: %v", err)
	}
	pin, err := Resolve(loaded, "github.com/Publisher/Plugin", s.Tag)
	if err != nil || pin != assetURL(s)+"#sha256="+s.SHA256 {
		t.Fatalf("wrong pin %q, %v", pin, err)
	}
	for name, mutate := range map[string]func(*Entry){
		"repository": func(e *Entry) { e.RepositoryID++ },
		"release":    func(e *Entry) { e.ReleaseID++ },
		"asset":      func(e *Entry) { e.AssetID++ },
		"commit":     func(e *Entry) { e.Commit = strings.Repeat("b", 40) },
		"digest":     func(e *Entry) { e.Submission.SHA256 = strings.Repeat("b", 64) },
		"plugin":     func(e *Entry) { e.Validation.PluginID = "replacement" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := entry
			mutate(&changed)
			if err := index.Add(changed); err == nil {
				t.Fatal("replaced immutable accepted identity")
			}
		})
	}
}

func TestReleaseRejections(t *testing.T) {
	for name, mutate := range map[string]func(*github.RepositoryRelease){
		"mutable": func(r *github.RepositoryRelease) { r.Immutable = new(false) },
		"draft":   func(r *github.RepositoryRelease) { r.Draft = true },
		"tag":     func(r *github.RepositoryRelease) { r.TagName = "other" },
		"digest": func(r *github.RepositoryRelease) {
			r.Assets[0].Digest = new("sha256:" + strings.Repeat("0", 64))
		},
		"url": func(r *github.RepositoryRelease) {
			r.Assets[0].BrowserDownloadURL = new("https://example.com/plugin.wasm")
		},
		"size":      func(r *github.RepositoryRelease) { r.Assets[0].Size = new(int(MaxArtifactBytes + 1)) },
		"duplicate": func(r *github.RepositoryRelease) { r.Assets = append(r.Assets, r.Assets[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			client := fixtureGitHub(t, mutate)
			if _, _, err := client.Resolve(t.Context(), sampleSubmission()); err == nil {
				t.Fatal("accepted invalid release")
			}
		})
	}
}

func TestValidationFailureDoesNotAdmit(t *testing.T) {
	client := fixtureGitHub(t, func(*github.RepositoryRelease) {})
	index := EmptyIndex()
	var snapshot string
	service := Service{Providers: map[string]ReleaseProvider{"github": client}, Validate: func(_ context.Context, path string) (validation.Result, error) {
		snapshot = path
		return validation.Result{}, errors.New("incompatible WIT")
	}}
	if _, err := service.Admit(t.Context(), &index, sampleReceipt()); err == nil || len(index.Entries) != 0 {
		t.Fatal("failed plugin admitted")
	}
	if snapshot == "" {
		t.Fatal("validation was not invoked")
	}
	if _, err := os.Stat(filepath.Dir(snapshot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot directory remains after rejection: %v", err)
	}
}

type failingCloseBody struct {
	io.Reader
	err error
}

func (b failingCloseBody) Close() error { return b.err }

func TestDownloadCloseFailurePreventsAdmission(t *testing.T) {
	client := fixtureGitHub(t, func(*github.RepositoryRelease) {})
	closeErr := errors.New("response close failed")
	client.download.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := response(r, "wasm sample")
		resp.Body = failingCloseBody{Reader: strings.NewReader("wasm sample"), err: closeErr}
		return resp, nil
	})
	index := EmptyIndex()
	service := Service{Providers: map[string]ReleaseProvider{"github": client}, Validate: func(context.Context, string) (validation.Result, error) {
		t.Fatal("invoked validator after failed download cleanup")
		return validation.Result{}, nil
	}}
	if _, err := service.Admit(t.Context(), &index, sampleReceipt()); !errors.Is(err, closeErr) || len(index.Entries) != 0 {
		t.Fatalf("download cleanup failure lost: %v, entries %d", err, len(index.Entries))
	}
}

func TestDownloadDigestAndRedirectPolicy(t *testing.T) {
	client := fixtureGitHub(t, func(*github.RepositoryRelease) {})
	s := sampleSubmission()
	_, asset, err := client.Resolve(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	client.download.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return response(r, "tampered!!!"), nil })
	if err = client.Download(t.Context(), s, asset, filepath.Join(t.TempDir(), s.Asset)); err == nil {
		t.Fatal("tampered download accepted")
	}
	for _, target := range []string{"http://github.com/asset", "https://attacker.example/asset", "https://github.com.evil/asset", "https://user@github.com/asset"} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = client.download.CheckRedirect(req, nil); err == nil {
			t.Errorf("allowed %s", target)
		}
	}
}

func TestDiscoverySkipsMutableAndPrerelease(t *testing.T) {
	for _, skipped := range []string{"none", "mutable", "prerelease"} {
		t.Run(skipped, func(t *testing.T) {
			client := fixtureGitHub(t, func(r *github.RepositoryRelease) {
				if skipped == "mutable" {
					r.Immutable = new(false)
				}
				if skipped == "prerelease" {
					r.Prerelease = true
				}
			})
			candidates, err := client.Candidates(t.Context(), sampleSubmission().Source())
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if skipped == "none" {
				want = 1
			}
			if len(candidates) != want {
				t.Fatalf("got %d candidates, want %d", len(candidates), want)
			}
		})
	}
}

func TestAPIBounds(t *testing.T) {
	client, err := newGitHub("test-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(r, `{"full_name":"`+strings.Repeat("x", 4<<20)+`"}`), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = client.Resolve(t.Context(), sampleSubmission()); err == nil {
		t.Fatal("oversized API body accepted")
	}
}

func sampleReceipt() Receipt {
	return Receipt{SchemaVersion: 1, ID: "00000000-0000-4000-8000-000000000001", Submission: sampleSubmission(), AuthorizedAt: time.Now().UTC(), Identity: PublisherIdentity{Issuer: issuer("github"), Subject: "repo:publisher/plugin:ref:refs/tags/v1.0.0", RepositoryID: "10", OwnerID: "5", Repository: "publisher/plugin", Ref: "refs/tags/v1.0.0", Commit: strings.Repeat("a", 40), Workflow: "publisher/plugin/.github/workflows/release.yml@refs/tags/v1.0.0", RunID: "1:1"}}
}
