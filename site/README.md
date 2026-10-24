# Catalogue website

Static Preact pages built from the registry's public JSON export. The site owns
its browser search index, rendering and client behavior.

From the repository root:

```sh
bun install --frozen-lockfile
bun site/cli.ts --input dist --output dist
bun run --cwd site typecheck
bun test --cwd site
bun run --cwd site browser:install
bun run --cwd site test:browser
```

The root Bun workspace owns the shared dependency catalogs and the single lockfile.

Input contains `index.json`, plugin manifests and version records, `statuses.json`,
and optional `downloads.json`. A separate output directory receives unchanged
copies of public JSON alongside the website.

Every sort has static pages of 20 plugins. Search enhances those pages after its
data loads; sorting and pagination remain usable without JavaScript. URL state
preserves queries, sort and page across sharing and browser history.

HTML dates fall back to UTC. The browser formats dates, times and numeric counts
for its locale and time zone. Sorting always compares UTC instants.

The browser check uses intercepted static file responses, never a dev server.
`SITE_CHROMIUM` can select an existing Chromium executable for local verification.
