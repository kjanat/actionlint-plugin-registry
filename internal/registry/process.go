package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"registry.actionlint.kjanat.dev/internal/validation"
)

type WorkerReply struct {
	Result validation.Result `json:"result"`
	Error  string            `json:"error,omitempty"`
}

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

// ProcessValidator excludes API/publication credentials from the runtime process.
// The configured actionlint host enforces guest memory and call deadlines.
func ProcessValidator(executable string) Validator {
	return func(ctx context.Context, path string) (result validation.Result, err error) {
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		absolute, err := filepath.Abs(path)
		if err != nil {
			return validation.Result{}, err
		}
		dir, err := os.MkdirTemp("", "registry-worker-")
		if err != nil {
			return validation.Result{}, err
		}
		defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
		cmd := exec.CommandContext(ctx, executable, "_validate", absolute)
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
		err = cmd.Run()
		if ctx.Err() != nil {
			return validation.Result{}, errors.New("validator exceeded 45 second process deadline")
		}
		if err != nil {
			return validation.Result{}, fmt.Errorf("validator process failed: %w", err)
		}
		var reply WorkerReply
		if err = json.Unmarshal(stdout.Bytes(), &reply); err != nil {
			return validation.Result{}, errors.New("invalid validator response")
		}
		if reply.Error != "" {
			return validation.Result{}, errors.New(reply.Error)
		}
		return reply.Result, nil
	}
}
