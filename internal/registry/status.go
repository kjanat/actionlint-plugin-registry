package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type ReleaseState string

const (
	StatusActive     ReleaseState = "active"
	StatusDeprecated ReleaseState = "deprecated"
	StatusWithdrawn  ReleaseState = "withdrawn"
	StatusRevoked    ReleaseState = "revoked"
)

// StatusActor is supplied by the trusted registry administrator workflow, never
// by publisher input. Local commands cannot authenticate a claimed actor.
type StatusActor struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Workflow string `json:"workflow"`
	RunID    string `json:"run_id"`
}

var (
	actorPattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,38}$`)
	workflowPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+/\.github/workflows/status\.yml@refs/heads/master$`)
)

func (a StatusActor) validate() error {
	if a.Kind != "registry-admin" || !numericIDPattern.MatchString(a.ID) ||
		!actorPattern.MatchString(a.Name) || !workflowPattern.MatchString(a.Workflow) ||
		!runIDPattern.MatchString(a.RunID) {
		return errors.New("invalid registry administrator audit identity")
	}
	return nil
}

func ReadStatusActor(path string) (StatusActor, error) {
	var actor StatusActor
	if err := readRecord(path, &actor); err != nil {
		return actor, err
	}
	return actor, actor.validate()
}

type ReleaseStatus struct {
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	SHA256      string       `json:"sha256"`
	Status      ReleaseState `json:"status"`
	Reason      string       `json:"reason"`
	Replacement string       `json:"replacement,omitempty"`
	ChangedAt   time.Time    `json:"changed_at"`
	Actor       StatusActor  `json:"actor"`
}

type StatusSnapshot struct {
	SchemaVersion int             `json:"schema_version"`
	Releases      []ReleaseStatus `json:"releases"`
}

// StatusFor gives terminal digest revocation precedence over a tag's status.
func StatusFor(entry Entry, statuses []ReleaseStatus) *ReleaseStatus {
	var selected *ReleaseStatus
	for i := range statuses {
		status := &statuses[i]
		if status.SHA256 == entry.Submission.SHA256 && status.Status == StatusRevoked {
			return status
		}
		if status.Name == entry.Submission.Provider+".com/"+entry.Submission.Repository && status.Version == entry.Submission.Tag {
			selected = status
		}
	}
	return selected
}

// StatusEvent records a transition without modifying accepted publication data.
type StatusEvent struct {
	Sequence int `json:"sequence"`
	ReleaseStatus
}

func (s ReleaseStatus) validate(index Index) (Entry, error) {
	entry, err := Find(index, s.Name, s.Version)
	if err != nil {
		return Entry{}, err
	}
	name := entry.Submission.Provider + ".com/" + entry.Submission.Repository
	if s.Name != name || s.SHA256 != entry.Submission.SHA256 {
		return Entry{}, errors.New("release status disagrees with accepted repository, version or digest")
	}
	switch s.Status {
	case StatusActive, StatusDeprecated, StatusWithdrawn, StatusRevoked:
	default:
		return Entry{}, errors.New("invalid release status")
	}
	if strings.TrimSpace(s.Reason) == "" || len(s.Reason) > 2048 || strings.ContainsAny(s.Reason, "\x00\r") {
		return Entry{}, errors.New("status reason must contain 1 to 2048 bytes without NUL or CR")
	}
	if len(s.Replacement) > 512 || strings.ContainsAny(s.Replacement, "\x00\r\n") {
		return Entry{}, errors.New("invalid replacement reference")
	}
	if s.ChangedAt.IsZero() || s.ChangedAt.Location() != time.UTC {
		return Entry{}, errors.New("status audit timestamp must be UTC")
	}
	if err := s.Actor.validate(); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

type StatusStore struct{ Directory string }

func statusKey(entry Entry) string {
	return filepath.Join(entry.Receipt.PluginKey(), key(entry.Submission.Tag))
}

// Load verifies the entire audit history. Revocation is terminal for the bytes,
// including when another accepted version references the same digest.
func (store StatusStore) Load(index Index) ([]ReleaseStatus, error) {
	if err := safePath(store.Directory); err != nil {
		return nil, err
	}
	histories := map[string][]StatusEvent{}
	directories := map[string]bool{filepath.Clean(store.Directory): true}
	for _, entry := range index.Entries {
		directory := filepath.Join(store.Directory, statusKey(entry))
		directories[directory] = true
		directories[filepath.Dir(directory)] = true
	}
	err := filepath.WalkDir(store.Directory, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := safePath(path); err != nil {
			return err
		}
		if item.IsDir() {
			if !directories[filepath.Clean(path)] {
				return errors.New("unexpected status audit directory")
			}
			return nil
		}
		var event StatusEvent
		if err := readRecord(path, &event); err != nil {
			return err
		}
		entry, err := event.validate(index)
		if err != nil {
			return err
		}
		expected := filepath.Join(store.Directory, statusKey(entry), strconv.Itoa(event.Sequence)+".json")
		if event.Sequence < 1 || filepath.Clean(path) != filepath.Clean(expected) {
			return errors.New("status event path disagrees with release or sequence")
		}
		histories[statusKey(entry)] = append(histories[statusKey(entry)], event)
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Lstat(store.Directory); errors.Is(statErr, os.ErrNotExist) {
			return []ReleaseStatus{}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	current := []ReleaseStatus{}
	revocations := map[string]time.Time{}
	for _, history := range histories {
		slices.SortFunc(history, func(a, b StatusEvent) int { return a.Sequence - b.Sequence })
		for i, event := range history {
			if event.Sequence != i+1 {
				return nil, errors.New("status audit sequence contains a gap")
			}
			if i > 0 {
				previous := history[i-1]
				if previous.Status == StatusRevoked || event.ChangedAt.Before(previous.ChangedAt) {
					return nil, errors.New("invalid transition after revocation or backwards audit time")
				}
			}
		}
		current = append(current, history[len(history)-1].ReleaseStatus)
		last := history[len(history)-1]
		if last.Status == StatusRevoked {
			revocations[last.SHA256] = last.ChangedAt
		}
	}
	for _, history := range histories {
		for _, event := range history {
			if revokedAt, ok := revocations[event.SHA256]; ok && event.Status != StatusRevoked && !event.ChangedAt.Before(revokedAt) {
				return nil, errors.New("status change follows permanent digest revocation")
			}
		}
	}
	slices.SortFunc(current, func(a, b ReleaseStatus) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(a.Version, b.Version)
	})
	return current, nil
}

// Add is for trusted control-plane callers. The publication workflow serializes
// writes; an existing sequence file can never be replaced.
func (store StatusStore) Add(index Index, status ReleaseStatus) error {
	entry, err := status.validate(index)
	if err != nil {
		return err
	}
	current, err := store.Load(index)
	if err != nil {
		return err
	}
	for _, previous := range current {
		if previous.SHA256 == status.SHA256 && previous.Status == StatusRevoked {
			return errors.New("artifact digest is revoked permanently")
		}
		if previous.Name == status.Name && previous.Version == status.Version && status.ChangedAt.Before(previous.ChangedAt) {
			return errors.New("status audit time moved backwards")
		}
	}
	directory := filepath.Join(store.Directory, statusKey(entry))
	if err := safePath(directory); err != nil {
		return err
	}
	files, err := os.ReadDir(directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	event := StatusEvent{Sequence: len(files) + 1, ReleaseStatus: status}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	return writeImmutableJSON(filepath.Join(directory, fmt.Sprint(event.Sequence)+".json"), event)
}
