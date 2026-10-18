package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictSubmission(t *testing.T) {
	valid, err := json.Marshal(sampleSubmission())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"unknown":   strings.TrimSuffix(string(valid), "}") + `,"unexpected":true}`,
		"duplicate": strings.TrimSuffix(string(valid), "}") + `,"kind":"component"}`,
		"traversal": strings.Replace(string(valid), "plugin.wasm", "../plugin.wasm", 1),
		"url":       strings.Replace(string(valid), "publisher/plugin", "https://github.com/publisher/plugin", 1),
		"multiple":  string(valid) + string(valid),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSubmission([]byte(data)); err == nil {
				t.Fatal("accepted invalid submission")
			}
		})
	}

}

func TestSharedReceiptFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/github-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ParseReceipt(data)
	if err != nil {
		t.Fatal(err)
	}
	expected := sampleSubmission()
	expected.SHA256 = "fe0a035dd5a29c90e6f16cf9d7a63ce80ca832c07cd0113c278da046ce7f08a0"
	if receipt.Identity.RunID != "1:1" || receipt.Submission != expected {
		t.Fatal("shared receipt protocol changed")
	}
	for _, payload := range []string{
		strings.TrimSuffix(strings.TrimSpace(string(data)), "}") + `,"unexpected":true}`,
		strings.TrimSuffix(strings.TrimSpace(string(data)), "}") + `,"id":"00000000-0000-4000-8000-000000000002"}`,
		strings.Replace(string(data), `"run_id": "1:1"`, `"run_id": "1"`, 1),
	} {
		if _, err = ParseReceipt([]byte(payload)); err == nil {
			t.Fatal("invalid receipt accepted")
		}
	}
}

func TestWriteJSONReplacesAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path, EmptyIndex()); err != nil {
		t.Fatal(err)
	}
	var loaded Index
	if err := readRecord(path, &loaded); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary files left behind: %v, %v", files, err)
	}
}
