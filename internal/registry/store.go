package registry

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Plugin struct {
	SchemaVersion int    `json:"schema_version"`
	Issuer        string `json:"issuer"`
	RepositoryID  string `json:"repository_id"`
	OwnerID       string `json:"owner_id"`
	Provider      string `json:"provider"`
	Repository    string `json:"repository"`
	PluginID      string `json:"plugin_id"`
}

func pluginFor(e Entry) Plugin {
	return Plugin{
		SchemaVersion: 1,
		Issuer:        e.Receipt.Identity.Issuer, RepositoryID: e.Receipt.Identity.RepositoryID,
		OwnerID: e.Receipt.Identity.OwnerID, Provider: e.Submission.Provider,
		Repository: e.Submission.Repository, PluginID: e.PluginID(),
	}
}

type Store struct{ Directory string }

const maxRecordBytes = 2 << 20

// Refuse symlinks in every existing path component, including store ancestors.
func safePath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink refused: %s", current)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}
func readRecord(path string, target any) (err error) {
	if err = safePath(path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("registry record is not regular")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxRecordBytes {
		return errors.New("registry record exceeds 2 MiB")
	}
	return jsonv2.Unmarshal(data, target, jsonv2.RejectUnknownMembers(true))
}
func (s Store) Load() (Index, error) {
	index := EmptyIndex()
	if err := safePath(s.Directory); err != nil {
		return index, err
	}
	dirs, err := os.ReadDir(s.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return index, nil
	}
	if err != nil {
		return index, err
	}
	for _, dir := range dirs {
		if dir.Name() == ".gitkeep" && dir.Type().IsRegular() {
			continue
		}
		if !dir.IsDir() || !digestPattern.MatchString(dir.Name()) {
			return index, errors.New("unexpected registry store entry")
		}
		base := filepath.Join(s.Directory, dir.Name())
		var plugin Plugin
		if err = readRecord(filepath.Join(base, "plugin.json"), &plugin); err != nil {
			return index, err
		}
		if plugin.SchemaVersion != 1 || dir.Name() != key(plugin.Issuer+"\n"+plugin.RepositoryID) {
			return index, errors.New("plugin path disagrees with identity")
		}
		children, err := os.ReadDir(base)
		if err != nil {
			return index, err
		}
		if len(children) != 2 {
			return index, errors.New("unexpected plugin files")
		}
		versionsPath := filepath.Join(base, "versions")
		if err = safePath(versionsPath); err != nil {
			return index, err
		}
		versions, err := os.ReadDir(versionsPath)
		if err != nil {
			return index, err
		}
		if len(versions) == 0 {
			return index, errors.New("plugin has no accepted versions")
		}
		for _, version := range versions {
			if version.IsDir() || !strings.HasSuffix(version.Name(), ".json") {
				return index, errors.New("unexpected version file")
			}
			var e Entry
			if err = readRecord(filepath.Join(versionsPath, version.Name()), &e); err != nil {
				return index, err
			}
			if version.Name() != key(e.Submission.Tag)+".json" || pluginFor(e) != plugin {
				return index, errors.New("version path or plugin identity mismatch")
			}
			before := len(index.Entries)
			if err = index.Add(e); err != nil {
				return index, err
			}
			if len(index.Entries) != before+1 {
				return index, errors.New("duplicate accepted version")
			}
		}
	}
	return index, nil
}

// Merge validates every candidate record before writing any of them. The publication
// workflow permits only one writer at a time, and existing immutable files are never replaced.
func (s Store) Merge(candidate Index) error {
	if candidate.SchemaVersion != 1 {
		return errors.New("unsupported candidate schema_version")
	}
	existing, err := s.Load()
	if err != nil {
		return err
	}
	original := len(existing.Entries)
	for _, entry := range candidate.Entries {
		if err = existing.Add(entry); err != nil {
			return err
		}
		data, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return err
		}
		if len(data)+1 > maxRecordBytes {
			return errors.New("serialized version record exceeds 2 MiB")
		}
	}
	if len(existing.Entries) == original {
		return nil
	}
	for _, entry := range candidate.Entries {
		base := filepath.Join(s.Directory, entry.Receipt.PluginKey())
		if err = safePath(base); err != nil {
			return err
		}
		if _, err = os.Stat(base); errors.Is(err, os.ErrNotExist) {
			if err = os.MkdirAll(s.Directory, 0o755); err != nil {
				return err
			}
			if err = s.createPlugin(base, entry); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			path := filepath.Join(base, "versions", key(entry.Submission.Tag)+".json")
			if _, err = os.Stat(path); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err = writeImmutableJSON(path, entry); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s Store) createPlugin(path string, entry Entry) (err error) {
	temp, err := os.MkdirTemp(s.Directory, ".plugin-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temp)) }()
	if err = WriteJSON(filepath.Join(temp, "plugin.json"), pluginFor(entry)); err != nil {
		return err
	}
	if err = WriteJSON(filepath.Join(temp, "versions", key(entry.Submission.Tag)+".json"), entry); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
func writeImmutableJSON(path string, value any) (err error) {
	if err = safePath(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".version-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(f.Name())) }()
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	if err = errors.Join(writeErr, syncErr, f.Close()); err != nil {
		return err
	}
	// Hard-link creation is atomic and refuses an existing destination.
	return os.Link(f.Name(), path)
}
