// Package compatibility defines host-specific observations independently of plugin ABIs.
package compatibility

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const ProtocolVersion = 1

var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Host struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
}

// Report is the versioned wire response from an explicitly selected checker.
// Unsupported means the checker cannot assess this contract; it is not a failure.
type Report struct {
	SchemaVersion  int      `json:"schema_version"`
	ArtifactSHA256 string   `json:"artifact_sha256"`
	Host           Host     `json:"host"`
	Policy         string   `json:"policy"`
	Outcome        string   `json:"outcome"`
	Checks         []string `json:"checks"`
	Detail         string   `json:"detail,omitempty"`
}

func (r Report) Validate() error {
	if r.SchemaVersion != ProtocolVersion || !digest.MatchString(r.ArtifactSHA256) || !bounded(r.Host.Name, 128) || !bounded(r.Host.Revision, 256) || !bounded(r.Policy, 256) || len(r.Detail) > 16384 || len(r.Checks) > 4096 {
		return errors.New("invalid compatibility report identity or bounds")
	}
	switch r.Outcome {
	case "passed":
		if len(r.Checks) == 0 || r.Detail != "" {
			return errors.New("passed report requires checks and no error")
		}
	case "failed", "unsupported":
		if !bounded(r.Detail, 16384) {
			return errors.New("unsuccessful report requires a reason")
		}
	default:
		return errors.New("unknown compatibility outcome")
	}
	for _, check := range r.Checks {
		if !bounded(check, 256) {
			return errors.New("invalid compatibility check name")
		}
	}
	return nil
}
func bounded(value string, limit int) bool {
	return len(value) > 0 && len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n")
}

// Evidence binds an observation to the exact checker executable used by the operator.
// Host identities are checker claims, not publisher-controlled release metadata.
type Evidence struct {
	Report        Report    `json:"report"`
	CheckerSHA256 string    `json:"checker_sha256"`
	ObservedAt    time.Time `json:"observed_at"`
}

func (e Evidence) Validate() error {
	if err := e.Report.Validate(); err != nil {
		return err
	}
	if !digest.MatchString(e.CheckerSHA256) || e.ObservedAt.IsZero() || e.ObservedAt.After(time.Now().Add(time.Minute)) {
		return errors.New("invalid compatibility observation")
	}
	return nil
}
