package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"registry.actionlint.kjanat.dev/adapters/actionlint/internal/validation"
)

type host struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
}
type report struct {
	SchemaVersion  int      `json:"schema_version"`
	ArtifactSHA256 string   `json:"artifact_sha256"`
	Host           host     `json:"host"`
	Policy         string   `json:"policy"`
	Outcome        string   `json:"outcome"`
	Checks         []string `json:"checks"`
	Detail         string   `json:"detail,omitempty"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: actionlint-checker <local-artifact>")
	}
	file, err := os.Open(args[0])
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, hashErr := io.Copy(hash, io.LimitReader(file, (64<<20)+1))
	if err = errors.Join(hashErr, file.Close()); err != nil {
		return err
	}
	if size > 64<<20 {
		return errors.New("artifact exceeds 64 MiB")
	}
	reply := report{SchemaVersion: 1, ArtifactSHA256: hex.EncodeToString(hash.Sum(nil)), Host: host{Name: "actionlint", Revision: validation.HostCommit}, Policy: "actionlint-smoke/1", Outcome: "passed", Checks: []string{}}
	result, err := validation.Validate(ctx, args[0])
	if err != nil {
		reply.Outcome = "failed"
		if errors.Is(err, validation.ErrUnsupportedContract) {
			reply.Outcome = "unsupported"
		}
		reply.Detail = err.Error()
	} else {
		reply.Checks = result.Checks
	}
	return json.NewEncoder(os.Stdout).Encode(reply)
}
