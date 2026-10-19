package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-github/v93/github"
	"registry.actionlint.kjanat.dev/internal/artifact"
)

func TestAdmissionFailureDisposition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.wasm")
	if err := os.WriteFile(path, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, malformed := artifact.Inspect(t.Context(), path)
	_, missing := artifact.Inspect(t.Context(), filepath.Join(t.TempDir(), "missing.wasm"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, canceled := artifact.Inspect(ctx, path)
	for _, item := range []struct {
		name string
		err  error
		want string
	}{
		{"malformed-content", malformed, "rejected"},
		{"wrapped-content", fmt.Errorf("inspection: %w", malformed), "rejected"},
		{"missing-file", missing, "retryable"},
		{"canceled", canceled, "retryable"},
		{"deadline", context.DeadlineExceeded, "retryable"},
		{"unclassified", errors.New("HTTP 404"), "retryable"},
		{"storage", os.ErrPermission, "retryable"},
	} {
		t.Run(item.name, func(t *testing.T) {
			if item.err == nil || FailureDisposition(item.err) != item.want {
				t.Fatalf("wrong disposition for %v: got %s, want %s", item.err, FailureDisposition(item.err), item.want)
			}
		})
	}
}

func TestAdmissionIdentityFailureIsDeterministic(t *testing.T) {
	service := Service{Providers: map[string]ReleaseProvider{"github": fixtureGitHub(t, func(*github.RepositoryRelease) {})}}
	receipt := sampleReceipt()
	receipt.Identity.RepositoryID = "11"
	index := EmptyIndex()
	_, err := service.Admit(t.Context(), &index, receipt)
	if err == nil || FailureDisposition(err) != "rejected" || len(index.Entries) != 0 {
		t.Fatalf("identity mismatch was not rejected: %v", err)
	}
}

func TestAdmissionReleaseAvailabilityRemainsRetryable(t *testing.T) {
	service := Service{Providers: map[string]ReleaseProvider{"github": fixtureGitHub(t, func(release *github.RepositoryRelease) { release.Draft = true })}}
	index := EmptyIndex()
	_, err := service.Admit(t.Context(), &index, sampleReceipt())
	if err == nil || FailureDisposition(err) != "retryable" || len(index.Entries) != 0 {
		t.Fatalf("unpublished release was permanently rejected: %v", err)
	}
}
