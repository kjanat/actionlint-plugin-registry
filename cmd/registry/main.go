package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"registry.actionlint.kjanat.dev/internal/artifact"
	"registry.actionlint.kjanat.dev/internal/registry"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: registry <candidate|admit|inspect|check|resolve|merge|export|status> [flags]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	storePath := flags.String("store", "registry/plugins", "Read or write accepted plugin records in this directory.")
	receiptPath := flags.String("receipt", "", "Read the JSON receipt obtained from an authenticated Worker claim.")
	reportPath := flags.String("report", "", "Write a JSON report to this file.")
	artifactPath := flags.String("artifact", "", "Inspect this local WASM file or check it with an adapter.")
	candidatePath := flags.String("candidate", "", "Write an admission delta here, or merge validated records from this directory.")
	outputPath := flags.String("output", "dist", "Write the generated catalogue to this directory.")
	aliasesPath := flags.String("aliases", "registry/aliases.json", "Read registry-maintained alias assignments for catalogue export.")
	downloadCounts := flags.Bool("download-counts", false, "Refresh GitHub asset download counts when exporting the catalogue.")
	pluginID := flags.String("plugin", "", "Identify the plugin by provider/repository or host/repository.")
	checker := flags.String("checker", "", "Run this trusted checker executable for an optional compatibility observation.")
	evidencePath := flags.String("compatibility", "registry/compatibility", "Read or append host compatibility observations in this directory.")
	version := flags.String("version", "", "Resolve this exact accepted release tag.")
	statusPath := flags.String("statuses", "registry/status", "Read or append audited release status events.")
	state := flags.String("status", "", "Set active, deprecated, withdrawn or revoked.")
	sha256 := flags.String("sha256", "", "Require this exact accepted artifact digest.")
	reason := flags.String("reason", "", "Record the reason for the status change.")
	replacement := flags.String("replacement", "", "Suggest a replacement plugin or version.")
	actorPath := flags.String("actor-file", "", "Read the administrator audit identity from the trusted workflow.")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("this command does not accept positional arguments")
	}
	if args[0] == "inspect" {
		if *artifactPath == "" {
			return errors.New("inspect requires --artifact")
		}
		declaration, err := artifact.Inspect(ctx, *artifactPath)
		if err != nil {
			return err
		}
		if *reportPath != "" {
			return registry.WriteJSON(*reportPath, declaration)
		}
		return json.NewEncoder(os.Stdout).Encode(declaration)
	}
	store := registry.Store{Directory: *storePath}
	index, err := store.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "candidate":
		if *artifactPath == "" {
			return errors.New("candidate requires --artifact; --output must name a new directory")
		}
		candidate, err := registry.ExportCandidate(ctx, *artifactPath, *outputPath, index)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(candidate)
	case "status":
		if *pluginID == "" || *version == "" || *sha256 == "" || *state == "" || *reason == "" || *actorPath == "" {
			return errors.New("status requires --plugin, --version, --sha256, --status, --reason and --actor-file")
		}
		actor, err := registry.ReadStatusActor(*actorPath)
		if err != nil {
			return err
		}
		entry, err := registry.Find(index, *pluginID, *version)
		if err != nil {
			return err
		}
		status := registry.ReleaseStatus{
			Name:    entry.Submission.Provider + ".com/" + entry.Submission.Repository,
			Version: *version, SHA256: *sha256, Status: registry.ReleaseState(*state),
			Reason: *reason, Replacement: *replacement, ChangedAt: time.Now().UTC(), Actor: actor,
		}
		return (registry.StatusStore{Directory: *statusPath}).Add(index, status)
	case "check":
		if *checker == "" || *artifactPath == "" || (*pluginID == "") != (*version == "") {
			return errors.New("check requires --checker and --artifact; add both --plugin and --version to record evidence for an accepted release")
		}
		if *pluginID == "" {
			declaration, err := artifact.Inspect(ctx, *artifactPath)
			if err != nil {
				return err
			}
			evidence, err := registry.CheckArtifact(ctx, *checker, *artifactPath, declaration.SHA256)
			if err != nil {
				return err
			}
			if *reportPath != "" {
				return registry.WriteJSON(*reportPath, evidence)
			}
			return json.NewEncoder(os.Stdout).Encode(evidence)
		}
		entry, err := registry.Find(index, *pluginID, *version)
		if err != nil {
			return err
		}
		evidence, err := registry.CheckCompatibility(ctx, *checker, *artifactPath, entry)
		if err != nil {
			return err
		}
		if err = (registry.EvidenceStore{Directory: *evidencePath}).Add(index, evidence); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(evidence)
	case "resolve":
		statuses, err := (registry.StatusStore{Directory: *statusPath}).Load(index)
		if err != nil {
			return err
		}
		entry, err := registry.Find(index, *pluginID, *version)
		if err != nil {
			return err
		}
		if status := registry.StatusFor(entry, statuses); status != nil {
			switch status.Status {
			case registry.StatusRevoked:
				return fmt.Errorf("artifact is revoked: %s", status.Reason)
			case registry.StatusWithdrawn:
				return fmt.Errorf("release is withdrawn: %s", status.Reason)
			case registry.StatusDeprecated:
				fmt.Fprintf(os.Stderr, "warning: release is deprecated: %s\n", status.Reason)
			}
		}
		value, err := registry.Resolve(index, *pluginID, *version)
		if err != nil {
			return err
		}
		fmt.Println(value)
		return nil
	case "export":
		aliases, err := registry.ReadAliases(*aliasesPath, index)
		if err != nil {
			return err
		}
		statuses, err := (registry.StatusStore{Directory: *statusPath}).Load(index)
		if err != nil {
			return err
		}
		evidence, err := (registry.EvidenceStore{Directory: *evidencePath}).Load(index)
		if err != nil {
			return err
		}
		if *downloadCounts {
			client, err := registry.NewGitHub(os.Getenv("GH_TOKEN"))
			if err != nil {
				return err
			}
			snapshot, err := registry.ExportWithDownloadCounts(ctx, index, *outputPath, client, statuses, &aliases, evidence...)
			if err != nil {
				return err
			}
			return registry.WriteCatalogue(index, *outputPath, statuses, &snapshot, &aliases)
		}
		if err := registry.Export(index, *outputPath, statuses, &aliases, evidence...); err != nil {
			return err
		}
		return registry.WriteCatalogue(index, *outputPath, statuses, nil, &aliases)
	case "merge":
		if *candidatePath == "" {
			return errors.New("merge requires --candidate")
		}
		candidate, err := (registry.Store{Directory: *candidatePath}).Load()
		if err != nil {
			return err
		}
		mergeErr := store.Merge(candidate)
		if *reportPath != "" {
			report := registry.NewReport("merge")
			for _, entry := range candidate.Entries {
				if len(candidate.Entries) == 1 {
					report.SubmissionID = entry.Receipt.ID
				}
				if mergeErr == nil {
					report.Accepted = append(report.Accepted, entry)
				} else {
					report.Rejected = append(report.Rejected, registry.Failure{Repository: entry.Submission.Repository, Tag: entry.Submission.Tag, Error: mergeErr.Error(), Disposition: registry.FailureDisposition(mergeErr)})
				}
			}
			if err = registry.WriteReport(*reportPath, report); err != nil {
				return errors.Join(mergeErr, err)
			}
		}
		return mergeErr
	case "admit":
		if *receiptPath == "" {
			return errors.New("admit requires --receipt to reference a receipt obtained from an authenticated Worker claim")
		}
		data, err := readBounded(*receiptPath, 32<<10)
		if err != nil {
			return err
		}
		receipt, err := registry.ParseReceipt(data)
		if err != nil {
			return err
		}
		providers := map[string]registry.ReleaseProvider{}
		if receipt.Submission.Provider == "github" {
			client, err := registry.NewGitHub(os.Getenv("GH_TOKEN"))
			if err != nil {
				return err
			}
			providers["github"] = client
		} else {
			providers["gitlab"] = registry.NewGitLab()
		}
		service := registry.Service{Providers: providers}
		report := registry.NewReport("admit")
		report.SubmissionID = receipt.ID
		entry, admissionErr := service.Admit(ctx, &index, receipt)
		if admissionErr == nil {
			delta := registry.EmptyIndex()
			if err = delta.Add(entry); err != nil {
				return err
			}
			destination := store
			if *candidatePath != "" {
				destination.Directory = *candidatePath
			}
			admissionErr = destination.Merge(delta)
		}
		if admissionErr == nil {
			report.Accepted = append(report.Accepted, entry)
		} else {
			report.Rejected = append(report.Rejected, registry.Failure{Repository: receipt.Submission.Repository, Tag: receipt.Submission.Tag, Error: admissionErr.Error(), Disposition: registry.FailureDisposition(admissionErr)})
		}
		if *reportPath != "" {
			if err = registry.WriteReport(*reportPath, report); err != nil {
				return errors.Join(admissionErr, err)
			}
		}
		return admissionErr
	default:
		return errors.New("unknown registry command")
	}
}

func readBounded(path string, limit int64) (data []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	data, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("input exceeds size limit")
	}
	return data, nil
}
