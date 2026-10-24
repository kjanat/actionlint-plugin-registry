package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
)

func TestDownloadCountMatchesAdmittedAsset(t *testing.T) {
	entry := acceptedFixture(t)
	for _, name := range []string{"valid", "zero", "missing", "negative", "id", "digest", "name", "state", "rate-limit"} {
		t.Run(name, func(t *testing.T) {
			client, err := newGitHub("test-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Path != "/repos/publisher/plugin/releases/assets/30" ||
					r.Header.Get("Accept") == "application/octet-stream" {
					t.Fatalf("expected metadata-only request for admitted asset, got %s %s", r.Method, r.URL)
				}
				asset := github.ReleaseAsset{
					ID: new(entry.AssetID), Name: new(entry.Submission.Asset),
					Digest: new("sha256:" + entry.Submission.SHA256), State: new("uploaded"), DownloadCount: new(42),
				}
				switch name {
				case "zero":
					asset.DownloadCount = new(0)
				case "missing":
					asset.DownloadCount = nil
				case "negative":
					asset.DownloadCount = new(-1)
				case "id":
					asset.ID = new(int64(31))
				case "digest":
					asset.Digest = new("sha256:" + strings.Repeat("0", 64))
				case "name":
					asset.Name = new("schema.json")
				case "state":
					asset.State = new("new")
				case "rate-limit":
					result := response(r, `{"message":"rate limited"}`)
					result.StatusCode = http.StatusForbidden
					return result, nil
				}
				data, err := json.Marshal(asset)
				return response(r, string(data)), err
			}))
			if err != nil {
				t.Fatal(err)
			}
			count, err := client.DownloadCount(t.Context(), entry)
			switch name {
			case "valid":
				if err != nil || count != 42 {
					t.Fatalf("got %d, %v", count, err)
				}
			case "zero":
				if err != nil || count != 0 {
					t.Fatalf("zero downloads not preserved: %d, %v", count, err)
				}
			default:
				if err == nil {
					t.Fatalf("accepted invalid count/asset: %d", count)
				}
			}
		})
	}
}

type counterFunc func(context.Context, Entry) (int64, error)

func (f counterFunc) DownloadCount(ctx context.Context, entry Entry) (int64, error) {
	return f(ctx, entry)
}

func TestDownloadSnapshotsPreserveUnknownsAndRecords(t *testing.T) {
	entry := acceptedFixture(t)
	unavailable := entry
	unavailable.Submission.Tag = "v2"
	unsupported := entry
	unsupported.Submission.Provider = "gitlab"
	unsupported.Receipt.Identity.Issuer = "https://gitlab.com"
	index := Index{SchemaVersion: 1, Entries: []Entry{entry, unavailable, unsupported}}
	calls := 0
	snapshot := collectDownloads(t.Context(), index, counterFunc(func(_ context.Context, e Entry) (int64, error) {
		calls++
		if e.Submission.Tag == "v2" {
			return 0, errors.New("upstream unavailable")
		}
		return 0, nil
	}))
	if calls != 2 || snapshot.SchemaVersion != 1 || snapshot.ObservedAt.IsZero() {
		t.Fatalf("invalid snapshot: %+v, calls %d", snapshot, calls)
	}
	if snapshot.Releases[0].Count == nil || *snapshot.Releases[0].Count != 0 ||
		snapshot.Releases[1].Count != nil || snapshot.Releases[1].Status != "unavailable" ||
		snapshot.Releases[2].Count != nil || snapshot.Releases[2].Status != "unsupported" {
		t.Fatalf("unknown download counts became zero: %+v", snapshot.Releases)
	}
	before := t.TempDir()
	after := t.TempDir()
	if err := Export(index, before, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := export(index, after, nil, &snapshot, nil); err != nil {
		t.Fatal(err)
	}
	// Statistics never alter installer indexes or immutable publication evidence.
	for _, path := range []string{
		"index.json",
		filepath.Join("plugins", entry.Receipt.PluginKey(), "index.json"),
		filepath.Join("plugins", entry.Receipt.PluginKey(), "versions", key(entry.Submission.Tag)+".json"),
	} {
		original, err := os.ReadFile(filepath.Join(before, path))
		if err != nil {
			t.Fatal(err)
		}
		current, err := os.ReadFile(filepath.Join(after, path))
		if err != nil || string(original) != string(current) {
			t.Fatalf("statistics changed %s: %v", path, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(after, "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded DownloadSnapshot
	if err = json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(decoded, snapshot) {
		t.Fatalf("snapshot round trip failed: %v", err)
	}
	html := readCataloguePage(t, after) + readPluginPage(t, after, entry) + readPluginPage(t, after, unsupported)
	for _, text := range []string{"<dd>0</dd>", "<dd>unavailable</dd>", "<dd>unsupported</dd>", "downloads.json", "unique users", "Observed "} {
		if !strings.Contains(html, text) {
			t.Errorf("missing rendered %q", text)
		}
	}
}

func TestDownloadRefreshBudgetStopsFurtherRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	snapshot := collectDownloads(ctx, Index{Entries: []Entry{acceptedFixture(t)}}, counterFunc(func(context.Context, Entry) (int64, error) {
		t.Fatal("expired refresh attempted another API request")
		return 0, nil
	}))
	if snapshot.Releases[0].Count != nil || snapshot.Releases[0].Status != "unavailable" {
		t.Fatal("expired refresh misreported downloads")
	}
}
