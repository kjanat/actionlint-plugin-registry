package registry

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"registry.actionlint.kjanat.dev/internal/artifact"
)

func TestReportContainsLargeAcceptedRecord(t *testing.T) {
	entry := acceptedFixture(t)
	metadata := bytes.TrimSuffix(entry.Artifact.Metadata, []byte("}"))
	metadata = append(metadata, []byte(`,"options_schema":{"description":"`)...)
	metadata = append(metadata, bytes.Repeat([]byte("x"), artifact.MaxMetadata-512-len(metadata))...)
	entry.Artifact.Metadata = append(metadata, []byte(`"}}`)...)
	index := EmptyIndex()
	if err := index.Add(entry); err != nil {
		t.Fatal(err)
	}
	store := Store{Directory: filepath.Join(t.TempDir(), "plugins")}
	if err := store.Merge(index); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded.Entries) != 1 {
		t.Fatalf("load accepted record: %v, %d entries", err, len(loaded.Entries))
	}
	for _, mode := range []string{"admit", "merge"} {
		t.Run(mode, func(t *testing.T) {
			report := NewReport(mode)
			report.SubmissionID = entry.Receipt.ID
			report.Accepted = loaded.Entries
			path := filepath.Join(t.TempDir(), "report.json")
			if err := WriteReport(path, report); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) <= 1<<20 || len(data) > maxRecordBytes+32<<10 {
				t.Fatalf("report size outside recovery bounds: %d", len(data))
			}
			compact, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, append(compact, '\n')) {
				t.Fatal("report nesting expanded accepted record")
			}
			var decoded Report
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.Accepted) != 1 || decoded.SubmissionID != entry.Receipt.ID {
				t.Fatal("report lost admission evidence")
			}
		})
	}
}
