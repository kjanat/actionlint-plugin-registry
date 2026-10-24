package validation

import (
	"errors"
	"strings"
	"testing"
)

const componentContractDigest = "7ce5cd5b7c62542d8b3b9860cdc5b1fa050ee50a87d068765b5050261861dfa7"
const scriptContractDigest = "cb0d4c86f1c4e9e8029368b8686f5da542d8e6e542483aa6dbce2510a35eba86"
const runnerContractDigest = "4d8a53c83af4cc69656c9cbd1fc09b7e9bc79aaeaba6db5a0e5b9998290cb0bf"

// ShellCheck vendors the same analyzer types with older documentation comments.
// Both source digests must reach type validation, including incompatible exports.
func TestAnalyzerCompatibilityUsesExportedTypes(t *testing.T) {
	for _, digest := range []string{componentContractDigest, "a0096f562ab3da46b30d0f025ad1c6ed0d46e5b7ea1a53660b3214e3270fcbee"} {
		metadata := strings.Replace(componentMetadata, componentContractDigest, digest, 1)
		result, err := Validate(t.Context(), packagedFixture(t, "component-no-sudo", metadata))
		if err != nil {
			t.Fatal(err)
		}
		if result.Contract != componentInterface+"#sha256="+digest {
			t.Fatalf("changed declared source identity: %s", result.Contract)
		}
		_, err = Validate(t.Context(), packagedFixture(t, "component-wrong-record", metadata))
		if err == nil || errors.Is(err, ErrUnsupportedContract) {
			t.Fatalf("wrong types must fail runtime validation: %v", err)
		}
	}
	for _, invalid := range []string{"", "invalid", strings.Repeat("A", 64), strings.Repeat("0", 63)} {
		metadata := strings.Replace(componentMetadata, componentContractDigest, invalid, 1)
		if _, err := Validate(t.Context(), packagedFixture(t, "component-no-sudo", metadata)); err == nil {
			t.Fatal("accepted malformed digest")
		}
	}
}
