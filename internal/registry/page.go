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

//go:embed site.css
var catalogueCSS []byte

// WriteCatalogue renders the website after the registry JSON has been exported.
func WriteCatalogue(index Index, directory string, statuses []ReleaseStatus, downloads *DownloadSnapshot, aliases *Aliases) error {
	plugins, err := cataloguePlugins(index, statuses, downloads, aliases)
	if err != nil {
		return err
	}
	page, err := template.New("catalogue").Parse(catalogueHTML)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(directory, "assets"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "assets", "site.css"), catalogueCSS, 0o644); err != nil {
		return err
	}
	view := catalogueHomepage(plugins, downloads)
	if err := writeCatalogueTemplate(page, filepath.Join(directory, "index.html"), "home", view); err != nil {
		return err
	}
	for _, plugin := range plugins {
		view := catalogueView{Root: "../../", Plugin: plugin, Downloads: downloads}
		if err := writeCatalogueTemplate(page, filepath.Join(directory, filepath.FromSlash(plugin.Path)), "plugin", view); err != nil {
			return err
		}
	}
	return nil
}

func writeCatalogueTemplate(page *template.Template, path, name string, view catalogueView) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err = page.ExecuteTemplate(f, name, view); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}
