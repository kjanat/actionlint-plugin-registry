package registry

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func statusFixture(entry Entry, state ReleaseState) ReleaseStatus {
	return ReleaseStatus{
		Name:    entry.Submission.Provider + ".com/" + entry.Submission.Repository,
		Version: entry.Submission.Tag, SHA256: entry.Submission.SHA256, Status: state,
		Reason:    "Administrator reviewed publisher request",
		ChangedAt: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		Actor: StatusActor{Kind: "registry-admin", ID: "123", Name: "maintainer",
			Workflow: "owner/registry/.github/workflows/status.yml@refs/heads/master", RunID: "100:1"},
	}
}

func TestReleaseStatusAuditAndExport(t *testing.T) {
	entry := acceptedFixture(t)
	index := Index{SchemaVersion: 1, Entries: []Entry{entry}}
	store := StatusStore{Directory: filepath.Join(t.TempDir(), "status")}
	before := t.TempDir()
	if err := Export(index, before, nil, nil); err != nil {
		t.Fatal(err)
	}
	var empty StatusSnapshot
	if err := readRecord(filepath.Join(before, "statuses.json"), &empty); err != nil || empty.SchemaVersion != 1 || empty.Releases == nil || len(empty.Releases) != 0 {
		t.Fatalf("empty status export: %+v, %v", empty, err)
	}
	states := []ReleaseState{StatusDeprecated, StatusWithdrawn, StatusActive, StatusRevoked}
	for i, state := range states {
		status := statusFixture(entry, state)
		status.ChangedAt = status.ChangedAt.Add(time.Duration(i) * time.Second)
		status.Replacement = "github.com/publisher/plugin@v2.0.0"
		if err := store.Add(index, status); err != nil {
			t.Fatal(err)
		}
		current, err := store.Load(index)
		if err != nil || len(current) != 1 || current[0] != status {
			t.Fatalf("current status: %+v, %v", current, err)
		}
		output := t.TempDir()
		if err := Export(index, output, current, nil); err != nil {
			t.Fatal(err)
		}
		version := filepath.Join("plugins", entry.Receipt.PluginKey(), "versions", key(entry.Submission.Tag)+".json")
		original, err := os.ReadFile(filepath.Join(before, version))
		if err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(filepath.Join(output, version))
		if err != nil || !bytes.Equal(original, after) {
			t.Fatal("status change modified immutable publication bytes")
		}
		var snapshot StatusSnapshot
		if err := readRecord(filepath.Join(output, "statuses.json"), &snapshot); err != nil || len(snapshot.Releases) != 1 || snapshot.Releases[0] != status {
			t.Fatalf("exported status: %+v, %v", snapshot, err)
		}
	}
	for _, state := range states {
		if err := store.Add(index, statusFixture(entry, state)); err == nil {
			t.Fatalf("revoked digest changed to %s", state)
		}
	}
	files, err := os.ReadDir(filepath.Join(store.Directory, statusKey(entry)))
	if err != nil || len(files) != len(states) {
		t.Fatalf("audit history: %v, %v", files, err)
	}
}

func TestStatusRejectsInvalidIdentityAndAudit(t *testing.T) {
	entry := acceptedFixture(t)
	index := Index{SchemaVersion: 1, Entries: []Entry{entry}}
	for name, mutate := range map[string]func(*ReleaseStatus){
		"digest":      func(s *ReleaseStatus) { s.SHA256 = strings.Repeat("f", 64) },
		"version":     func(s *ReleaseStatus) { s.Version = "v2.0.0" },
		"name":        func(s *ReleaseStatus) { s.Name = "github.com/other/plugin" },
		"state":       func(s *ReleaseStatus) { s.Status = "deleted" },
		"reason":      func(s *ReleaseStatus) { s.Reason = " \t" },
		"time":        func(s *ReleaseStatus) { s.ChangedAt = time.Time{} },
		"actor":       func(s *ReleaseStatus) { s.Actor.Kind = "publisher" },
		"workflow":    func(s *ReleaseStatus) { s.Actor.Workflow = "untrusted" },
		"replacement": func(s *ReleaseStatus) { s.Replacement = "a\nb" },
	} {
		t.Run(name, func(t *testing.T) {
			store := StatusStore{Directory: filepath.Join(t.TempDir(), "status")}
			status := statusFixture(entry, StatusDeprecated)
			mutate(&status)
			if err := store.Add(index, status); err == nil {
				t.Fatal("invalid status accepted")
			}
			if _, err := os.Stat(store.Directory); !os.IsNotExist(err) {
				t.Fatal("invalid status changed the store")
			}
		})
	}
}

func TestStatusRevocationBindsDigestAcrossVersions(t *testing.T) {
	entry := acceptedFixture(t)
	other := entry
	other.Submission.Tag = "v2.0.0"
	index := Index{SchemaVersion: 1, Entries: []Entry{entry, other}}
	store := StatusStore{Directory: filepath.Join(t.TempDir(), "status")}
	if err := store.Add(index, statusFixture(entry, StatusRevoked)); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(index, statusFixture(other, StatusActive)); err == nil {
		t.Fatal("reused revoked bytes were restored under another tag")
	}
	statuses, err := store.Load(index)
	if err != nil {
		t.Fatal(err)
	}
	if selected := StatusFor(other, statuses); selected == nil || selected.Status != StatusRevoked {
		t.Fatal("revocation did not apply to another tag using the same digest")
	}
}

func TestStatusAuditRejectsTampering(t *testing.T) {
	entry := acceptedFixture(t)
	index := Index{SchemaVersion: 1, Entries: []Entry{entry}}
	for _, mode := range []string{"sequence", "digest", "backwards-time", "after-revocation"} {
		t.Run(mode, func(t *testing.T) {
			store := StatusStore{Directory: filepath.Join(t.TempDir(), "status")}
			initial := statusFixture(entry, StatusDeprecated)
			if mode == "after-revocation" {
				initial.Status = StatusRevoked
			}
			if err := store.Add(index, initial); err != nil {
				t.Fatal(err)
			}
			next := StatusEvent{Sequence: 2, ReleaseStatus: statusFixture(entry, StatusWithdrawn)}
			next.ChangedAt = initial.ChangedAt.Add(time.Second)
			switch mode {
			case "sequence":
				next.Sequence = 3
			case "digest":
				next.SHA256 = strings.Repeat("f", 64)
			case "backwards-time":
				next.ChangedAt = initial.ChangedAt.Add(-time.Second)
			}
			body, err := json.Marshal(next)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.Directory, statusKey(entry), "2.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(index); err == nil {
				t.Fatal("tampered history accepted")
			}
		})
	}
}
