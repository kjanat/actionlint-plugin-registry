package registry

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// AliasTarget is assigned by registry maintainers, never by plugin metadata.
// Reserved permits assignment before the repository has a published release.
type AliasTarget struct {
	Repository string `json:"repository"`
	Reserved   bool   `json:"reserved,omitempty"`
}

type Aliases struct {
	SchemaVersion int                    `json:"schema_version"`
	Aliases       map[string]AliasTarget `json:"aliases"`
}

func ReadAliases(path string, index Index) (Aliases, error) {
	var aliases Aliases
	if err := readRecord(path, &aliases); err != nil {
		return aliases, err
	}
	return aliases, aliases.Validate(index)
}

func (a Aliases) Validate(index Index) error {
	if a.SchemaVersion != 1 || a.Aliases == nil {
		return errors.New("alias assignments require schema_version 1 and an aliases object")
	}
	published := map[string]bool{}
	for _, entry := range index.Entries {
		published[strings.ToLower(entry.Submission.Provider+".com/"+entry.Submission.Repository)] = true
	}
	for alias, target := range a.Aliases {
		if !aliasPattern.MatchString(alias) {
			return fmt.Errorf("invalid registry alias %q: use 1–64 lowercase letters, digits or hyphens, starting with a letter", alias)
		}
		host, repository, ok := strings.Cut(target.Repository, "/")
		provider := strings.TrimSuffix(host, ".com")
		if !ok || (host != "github.com" && host != "gitlab.com") ||
			target.Repository != strings.ToLower(target.Repository) || len(repository) > 512 ||
			(provider == "github" && !repositoryPattern.MatchString(repository)) ||
			(provider == "gitlab" && !gitlabRepositoryPattern.MatchString(repository)) {
			return fmt.Errorf("alias %q has invalid canonical repository %q", alias, target.Repository)
		}
		if !published[target.Repository] && !target.Reserved {
			return fmt.Errorf("alias %q targets unpublished repository %q; set reserved explicitly for a future publication", alias, target.Repository)
		}
	}
	return nil
}

func (a Aliases) For(repository string) []string {
	var names []string
	for name, target := range a.Aliases {
		if strings.EqualFold(target.Repository, repository) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
