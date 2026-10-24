/** @jsxImportSource preact */
import { copyFile, mkdir, readdir, writeFile } from 'node:fs/promises';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { render } from 'preact-render-to-string';
import { loadCatalogue } from '#catalogue.ts';
import { Home, PluginPage } from '#components.tsx';
import { pageSize, staticPath } from '#directory.ts';
import type { Sort } from '#directory.ts';

export async function buildSite(input: string, output: string): Promise<number> {
	const data = await loadCatalogue(resolve(input));
	const destination = resolve(output);
	if (resolve(input) !== destination) {
		async function copyJSON(source: string, target: string): Promise<void> {
			for (const entry of await readdir(source, { withFileTypes: true })) {
				const from = join(source, entry.name);
				const to = join(target, entry.name);
				if (from === destination) continue;
				if (entry.isDirectory()) await copyJSON(from, to);
				else if (entry.isFile() && entry.name.endsWith('.json')) {
					await mkdir(dirname(to), { recursive: true });
					await copyFile(from, to);
				}
			}
		}
		await copyJSON(resolve(input), destination);
	}
	const source = fileURLToPath(new URL('.', import.meta.url));
	const assets = join(resolve(output), 'assets');
	await mkdir(assets, { recursive: true });
	const bundled = await Bun.build({
		entrypoints: [join(source, 'client.tsx')],
		outdir: assets,
		naming: 'client.js',
		target: 'browser',
		minify: true,
		jsx: { runtime: 'automatic', importSource: 'preact' },
	});
	if (!bundled.success) throw new AggregateError(bundled.logs, 'Client bundle failed');
	await writeFile(join(assets, 'site.css'), await Bun.file(join(source, 'site.css')).text());
	const summaries = data.plugins.map(({ name, description, repository, path, version, updatedAt, downloads }) => ({
		name,
		description,
		repository,
		path,
		version,
		updatedAt,
		downloads,
	}));
	await writeFile(join(assets, 'search-index.json'), JSON.stringify(summaries));
	async function page(path: string, html: string) {
		const target = join(resolve(output), path);
		await mkdir(dirname(target), { recursive: true });
		await writeFile(target, `<!doctype html>\n${html}\n`);
	}
	const pages = Math.max(1, Math.ceil(data.plugins.length / pageSize));
	const sorts: Sort[] = ['downloads', 'name', 'updated'];
	for (const sort of sorts) {
		for (let number = 1; number <= pages; number++) {
			const path = staticPath(sort, number);
			const root = relative(dirname(path), '.').replaceAll('\\', '/');
			await page(
				path,
				render(<Home root={root ? `${root}/` : ''} data={data} state={{ query: '', sort, page: number }} />),
			);
		}
	}
	for (const plugin of data.plugins) {
		await page(plugin.path, render(<PluginPage plugin={plugin} observedAt={data.observedAt} />));
	}
	return data.plugins.length;
}
