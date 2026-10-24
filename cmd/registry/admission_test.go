package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"registry.actionlint.kjanat.dev/internal/registry"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the admission command with release HTTP fixtures. Admission must read
// existing registrations even when it writes elsewhere, and publication must
// report a conflict introduced after validation without changing the real store.
func TestAdmissionDeltaAndPublicationConflict(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	root := t.TempDir()
	storePath := filepath.Join(root, "registry/plugins")
	candidatePath := filepath.Join(root, "candidate/plugins")
	receiptPath := filepath.Join(root, "handoff/receipt.json")
	workflow, err := os.ReadFile("../../.github/workflows/admission.yml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Jobs map[string]struct {
			Steps []struct{ ID, Run string }
		}
	}
	if err = yaml.Unmarshal(workflow, &document); err != nil {
		t.Fatal(err)
	}
	workflowArguments := func(job, step, prefix string) []string {
		t.Helper()
		for _, value := range document.Jobs[job].Steps {
			if value.ID != step {
				continue
			}
			command, ok := strings.CutPrefix(value.Run, prefix)
			if !ok {
				t.Fatalf("unexpected %s command: %s", step, value.Run)
			}
			args := strings.Fields(command)
			for i := 1; i < len(args); i += 2 {
				if i+1 >= len(args) {
					t.Fatal("workflow flag has no value")
				}
				args[i+1] = filepath.Join(root, args[i+1])
			}
			return args
		}
		t.Fatalf("workflow step missing: %s/%s", job, step)
		return nil
	}
	admissionArgs := workflowArguments("validate", "admission", "bash scripts/validate.sh ./registry-cli ")
	mergeArgs := workflowArguments("publish", "merge", "go run ./cmd/registry ")
	data, err := os.ReadFile("../../internal/registry/testdata/github-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := registry.ParseReceipt(data)
	if err != nil {
		t.Fatal(err)
	}
	var wasm []byte
	previous := http.DefaultTransport
	http.DefaultTransport = releaseTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Host + r.URL.Path {
		case "api.github.com/repos/publisher/plugin":
			body = `{"id":10,"full_name":"publisher/plugin","private":false,"owner":{"id":5}}`
		case "api.github.com/repos/publisher/plugin/releases/tags/" + receipt.Submission.Tag:
			body = fmt.Sprintf(`{"id":20,"tag_name":%q,"immutable":true,"published_at":"2026-01-01T00:00:00Z","assets":[{"id":30,"name":"plugin.wasm","state":"uploaded","size":%d,"digest":%q,"browser_download_url":%q}]}`,
				receipt.Submission.Tag, len(wasm), "sha256:"+receipt.Submission.SHA256,
				"https://github.com/publisher/plugin/releases/download/"+receipt.Submission.Tag+"/plugin.wasm")
		case "api.github.com/repos/publisher/plugin/commits/" + receipt.Submission.Tag:
			body = `{"sha":"` + receipt.Identity.Commit + `"}`
		case "github.com/publisher/plugin/releases/download/" + receipt.Submission.Tag + "/plugin.wasm":
			body = string(wasm)
		default:
			return nil, fmt.Errorf("unexpected release request: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	prepare := func(version, pluginID string) {
		t.Helper()
		metadata := fmt.Sprintf(`{"metadata_version":1,"abi_version":1,"transport":"wasm-core-json","id":%q,"name":"Example","description":"Example plugin","license":"MIT","repository":"https://github.com/publisher/plugin","plugin_version":%q}`, pluginID, version)
		section := append([]byte{17}, []byte("actionlint.plugin")...)
		section = append(section, metadata...)
		wasm = []byte{0, 97, 115, 109, 1, 0, 0, 0, 0}
		for size := len(section); ; size >>= 7 {
			value := byte(size & 127)
			if size >= 128 {
				value |= 128
			}
			wasm = append(wasm, value)
			if size < 128 {
				break
			}
		}
		wasm = append(wasm, section...)
		digest := sha256.Sum256(wasm)
		receipt.Submission.SHA256 = hex.EncodeToString(digest[:])
		receipt.Submission.Tag = "v" + version
		receipt.Identity.Ref = "refs/tags/v" + version
		receipt.AuthorizedAt = time.Now().UTC()
		if err := registry.WriteJSON(receiptPath, receipt); err != nil {
			t.Fatal(err)
		}
	}
	prepare("1.0.0", "example")
	if err = run(t.Context(), []string{"admit", "--receipt", receiptPath, "--store", storePath}); err != nil {
		t.Fatal(err)
	}
	original, err := (registry.Store{Directory: storePath}).Load()
	if err != nil {
		t.Fatal(err)
	}
	prepare("1.1.0", "different")
	if err = run(t.Context(), admissionArgs); err == nil || registry.FailureDisposition(err) != "rejected" {
		t.Fatalf("changed identity escaped existing registrations: %v", err)
	}
	if _, err = os.Stat(candidatePath); !os.IsNotExist(err) {
		t.Fatal("rejected candidate wrote an output store")
	}
	prepare("1.1.0", "example")
	if err = run(t.Context(), admissionArgs); err != nil {
		t.Fatal(err)
	}
	unchanged, err := (registry.Store{Directory: storePath}).Load()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(original)
	after, _ := json.Marshal(unchanged)
	if !bytes.Equal(before, after) {
		t.Fatal("admission mutated existing registrations")
	}
	candidate, err := (registry.Store{Directory: candidatePath}).Load()
	if err != nil || len(candidate.Entries) != 1 || candidate.Entries[0].Submission.Tag != "v1.1.0" {
		t.Fatalf("wrong admission delta: %+v, %v", candidate, err)
	}
	if err = (registry.Store{Directory: filepath.Join(root, "accepted/candidate/plugins")}).Merge(candidate); err != nil {
		t.Fatal(err)
	}
	conflict := candidate.Entries[0]
	conflict.AssetID++
	if err = (registry.Store{Directory: storePath}).Merge(registry.Index{SchemaVersion: 1, Entries: []registry.Entry{conflict}}); err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), mergeArgs)
	if err == nil || registry.FailureDisposition(err) != "rejected" {
		t.Fatalf("publication conflict was not rejected: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(root, "reports/publication.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report registry.Report
	if err = json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Mode != "merge" || report.SubmissionID != receipt.ID || len(report.Accepted) != 0 || len(report.Rejected) != 1 || report.Rejected[0].Disposition != "rejected" {
		t.Fatalf("publication failure has no actionable report: %+v", report)
	}
}
