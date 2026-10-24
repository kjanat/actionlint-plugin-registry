package registry

import (
	_ "embed"
	"errors"
	"html/template"
	"os"
	"path/filepath"
)

//go:embed page.html
var catalogueHTML string

var catalogueTemplate = template.Must(template.New("catalogue").Parse(catalogueHTML))

type catalogueRelease struct {
	Entry
	Name          string
	RepositoryURL string
	Reference     string
	Record        string
	State         ReleaseState
	Reason        string
	Replacement   string
	Downloads     string
}

func writeCataloguePage(index Index, directory string, statuses []ReleaseStatus, downloads *DownloadSnapshot, aliases *Aliases) error {
	releases := make([]catalogueRelease, 0, len(index.Entries))
	for _, entry := range index.Entries {
		repository := entry.Submission.Provider + ".com/" + entry.Submission.Repository
		release := catalogueRelease{
			Entry: entry, Name: entry.PluginID(), RepositoryURL: "https://" + repository,
			Reference: repository, State: StatusActive, Downloads: "not collected",
			Record: "plugins/" + entry.Receipt.PluginKey() + "/versions/" + key(entry.Submission.Tag) + ".json",
		}
		if aliases != nil {
			if names := aliases.For(repository); len(names) > 0 {
				release.Name, release.Reference = names[0], names[0]
			}
		}
		if release.Name == "" {
			release.Name = entry.Submission.Repository
		}
		release.Reference += "@" + entry.Submission.Tag
		if status := StatusFor(entry, statuses); status != nil {
			release.State, release.Reason, release.Replacement = status.Status, status.Reason, status.Replacement
		}
		if downloads != nil {
			release.Downloads = downloads.display(entry)
		}
		releases = append(releases, release)
	}
	f, err := os.Create(filepath.Join(directory, "index.html"))
	if err != nil {
		return err
	}
	if err = catalogueTemplate.Execute(f, struct {
		Releases  []catalogueRelease
		Downloads *DownloadSnapshot
	}{releases, downloads}); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}
