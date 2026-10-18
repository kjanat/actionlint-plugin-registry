package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"registry.actionlint.kjanat.dev/internal/compatibility"
	"strings"
	"testing"
)

func TestCapturedOutputCannotBypassLimit(t *testing.T) {
	buffer := &boundedBuffer{limit: 8}
	reader := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 32))}
	if _, err := io.Copy(buffer, reader); err == nil {
		t.Fatal("io.Copy bypassed output budget")
	}
	if len(buffer.Bytes()) > 8 {
		t.Fatal("buffer retained data beyond budget")
	}
}

// The test executable acts as a checker when launched on the parent's snapshot.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && filepath.Base(os.Args[1]) == "plugin.wasm" {
		data, err := os.ReadFile(os.Args[1])
		if err != nil || os.Getenv("GH_TOKEN") != "" || bytes.Equal(data, []byte("crash")) {
			os.Exit(7)
		}
		sum := sha256.Sum256(data)
		reply := compatibility.Report{SchemaVersion: 1, ArtifactSHA256: hex.EncodeToString(sum[:]), Host: compatibility.Host{Name: "fixture", Revision: "host-a"}, Policy: "test/1", Outcome: string(data), Checks: []string{"sample"}}
		if reply.Outcome != "passed" {
			reply.Detail = "unsupported or failed fixture"
		}
		if bytes.Equal(data, []byte("wrong-digest")) {
			reply.Outcome = "failed"
			reply.ArtifactSHA256 = strings.Repeat("0", 64)
		}
		if err = json.NewEncoder(os.Stdout).Encode(reply); err != nil {
			os.Exit(8)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestCheckerFailuresNeverMutateAdmission(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "must-not-reach-checker")
	for _, outcome := range []string{"passed", "failed", "unsupported", "crash", "wrong-digest"} {
		t.Run(outcome, func(t *testing.T) {
			entry := acceptedFixture(t)
			raw := []byte(outcome)
			sum := sha256.Sum256(raw)
			entry.Submission.SHA256 = hex.EncodeToString(sum[:])
			before, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "source.wasm")
			if err = os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := CheckCompatibility(t.Context(), executable, path, entry)
			if outcome == "crash" || outcome == "wrong-digest" {
				if err == nil {
					t.Fatal("invalid checker response became evidence")
				}
			} else if err != nil || result.Report.Outcome != outcome || result.CheckerSHA256 == "" {
				t.Fatalf("report: %+v, %v", result, err)
			}
			after, err := json.Marshal(entry)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("checker mutated admission record")
			}
		})
	}
	entry := acceptedFixture(t)
	path := filepath.Join(t.TempDir(), "tampered.wasm")
	if err = os.WriteFile(path, []byte("passed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = CheckCompatibility(t.Context(), executable, path, entry); err == nil {
		t.Fatal("tampered artifact reached checker")
	}
	if _, err = CheckCompatibility(t.Context(), filepath.Join(t.TempDir(), "missing"), path, entry); err == nil {
		t.Fatal("missing checker became evidence")
	}
}
