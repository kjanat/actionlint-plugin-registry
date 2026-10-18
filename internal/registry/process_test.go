package registry

import (
	"io"
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
