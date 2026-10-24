package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"registry.actionlint.kjanat.dev/internal/compatibility"
)

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, errors.New("validator output exceeds limit")
	}
	return b.buffer.Write(p)
}

// CheckCompatibility runs a trusted, explicitly selected checker on a checksum-verified
// snapshot. It excludes registry credentials but does not provide an OS sandbox.
// Admission never calls this function.
func CheckCompatibility(ctx context.Context, executable, path string, entry Entry) (evidence compatibility.Evidence, err error) {
	if err = entry.Submission.Validate(); err != nil {
		return evidence, err
	}
	return CheckArtifact(ctx, executable, path, entry.Submission.SHA256)
}

// CheckArtifact observes a local candidate without requiring publication. The
// caller supplies the inspected digest; the checker receives a verified snapshot.
func CheckArtifact(ctx context.Context, executable, path, expectedDigest string) (evidence compatibility.Evidence, err error) {
	executable, err = filepath.Abs(executable)
	if err != nil {
		return evidence, err
	}
	if !digestPattern.MatchString(expectedDigest) {
		return evidence, errors.New("invalid checker input digest")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "registry-checker-")
	if err != nil {
		return evidence, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	checkerPath := filepath.Join(dir, "checker")
	if runtime.GOOS == "windows" {
		checkerPath += ".exe"
	}
	evidence.CheckerSHA256, err = snapshotFile(executable, checkerPath, 256<<20, 0o700)
	if err != nil {
		return evidence, err
	}
	snapshot := filepath.Join(dir, "plugin.wasm")
	digest, err := snapshotFile(path, snapshot, MaxArtifactBytes, 0o600)
	if err != nil {
		return evidence, err
	}
	if digest != expectedDigest {
		return evidence, errors.New("checker input checksum differs from inspected artifact")
	}
	cmd := exec.CommandContext(ctx, checkerPath, snapshot)
	cmd.Dir = dir
	cmd.Env = []string{"GOMEMLIMIT=512MiB", "TMPDIR=" + dir, "TMP=" + dir, "TEMP=" + dir}
	if root, ok := os.LookupEnv("SystemRoot"); ok {
		cmd.Env = append(cmd.Env, "SystemRoot="+root)
	}
	cmd.WaitDelay = time.Second
	stdout := &boundedBuffer{limit: 1 << 20}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err = cmd.Run(); err != nil {
		return evidence, fmt.Errorf("checker process: %w", err)
	}
	if err = jsonv2.Unmarshal(stdout.Bytes(), &evidence.Report, jsonv2.RejectUnknownMembers(true)); err != nil {
		return evidence, fmt.Errorf("checker response: %w", err)
	}
	if evidence.Report.ArtifactSHA256 != expectedDigest {
		return evidence, errors.New("checker report refers to a different artifact")
	}
	evidence.ObservedAt = time.Now().UTC()
	return evidence, evidence.Validate()
}

// snapshotFile hashes the bytes it copies, so execution cannot race a replacement
// of the operator's checker executable or local artifact.
func snapshotFile(sourcePath, targetPath string, limit int64, mode os.FileMode) (digest string, err error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	stat, err := source.Stat()
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() || stat.Size() > limit {
		return "", errors.New("snapshot requires a bounded regular file")
	}
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(target, hash), io.LimitReader(source, limit+1))
	if err != nil {
		return "", err
	}
	if size > limit {
		return "", errors.New("snapshot exceeds size limit")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
