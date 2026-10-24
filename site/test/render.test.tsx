/** @jsxImportSource preact */
import { expect, test } from 'bun:test';
import { render } from 'preact-render-to-string';
import { loadCatalogue } from '#catalogue.ts';
import { Home, PluginPage } from '#components.tsx';
import type { ReleaseState } from '#schema.ts';
import { createExport, fixture } from '#test/fixtures.ts';

test('home lists plugins; details retain pinned artifacts and exact commands', async () => {
	const directory = await createExport([{ ...fixture('Example'), aliases: ['curated'] }]);
	const data = await loadCatalogue(directory);
	const home = render(<Home root='' data={data} state={{ query: '', sort: 'downloads', page: 1 }} />);
	expect(home).toContain('class="plugin-card"');
	expect(home).toContain('<code>v1.0.0</code>');
	expect(home).not.toContain('actionlint plugin install');
	expect(home).not.toContain('Most downloaded');
	const plugin = data.plugins[0];
	if (!plugin) throw new Error('Missing fixture plugin');
	const page = render(<PluginPage plugin={plugin} observedAt={data.observedAt} />);
	expect(page).toContain('actionlint plugin install curated@v1.0.0');
	expect(page).toContain('SHA-256');
	expect(page).toContain(plugin.releases[0]?.pinnedURL ?? 'missing');
	expect(page).toContain(`href="../../${plugin.releases[0]?.record}"`);
	expect(page).toContain('datetime="2026-03-29T00:30:00Z"');
	expect(page).toContain('UTC');
});

const states: ReleaseState[] = ['active', 'deprecated', 'withdrawn', 'revoked'];
test.each(states)('%s status controls exact install commands without hiding evidence', async state => {
	const directory = await createExport([{
		name: 'Plugin',
		versions: [{
			version: 'v1',
			publishedAt: '2026-01-01T00:00:00Z',
			count: 1,
			state,
			reason: 'Maintainer explanation',
		}],
	}]);
	const data = await loadCatalogue(directory);
	const plugin = data.plugins[0];
	if (!plugin) throw new Error('Missing fixture plugin');
	const page = render(<PluginPage plugin={plugin} observedAt={data.observedAt} />);
	expect(page.includes('actionlint plugin install ')).toBe(state === 'active' || state === 'deprecated');
	expect(page).toContain('Maintainer explanation');
	expect(page).toContain('Publication record');
	expect(page).toContain(`class="state ${state}"`);
});

test('publisher metadata and status replacements remain text on both pages', async () => {
	const directory = await createExport([{
		name: 'Plugin',
		versions: [{
			version: 'v1',
			publishedAt: '2026-01-01T00:00:00Z',
			count: 1,
			metadata: {
				name: '<script>alert(1)</script>',
				description: '<img src=x onerror=alert(1)>',
				license: '<b>MIT</b>',
			},
			state: 'deprecated',
			reason: '<script>alert(2)</script>',
			replacement: 'javascript:alert(3)',
		}],
	}]);
	const data = await loadCatalogue(directory);
	const plugin = data.plugins[0];
	if (!plugin) throw new Error('Missing fixture plugin');
	for (
		const html of [
			render(<Home root='' data={data} state={{ query: '', sort: 'downloads', page: 1 }} />),
			render(<PluginPage plugin={plugin} observedAt={data.observedAt} />),
		]
	) {
		expect(html).not.toContain('<script>alert');
		expect(html).not.toContain('<img src=x');
		expect(html).toContain('&lt;script>');
		expect(html).not.toContain('href="javascript:');
	}
});
