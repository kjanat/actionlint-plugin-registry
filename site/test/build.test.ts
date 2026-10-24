import { expect, test } from 'bun:test';
import { readFile, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { buildSite } from '@/src/build.tsx';
import { createExport, fixture } from '@/test/fixtures.ts';

test('100-plugin build emits 20-row static pages for every sort and preserves registry JSON', async () => {
	const directory = await createExport(
		Array.from(
			{ length: 100 },
			(_, index) => fixture(`Plugin ${String(index + 1).padStart(3, '0')}`, index === 99 ? null : index),
		),
	);
	const before = await readFile(join(directory, 'index.json'), 'utf8');
	expect(await buildSite(directory, directory)).toBe(100);
	const home = await readFile(join(directory, 'index.html'), 'utf8');
	expect(home.match(/class="plugin-card"/g)).toHaveLength(20);
	expect(home).toContain('Plugin 099');
	expect(home).not.toContain('Plugin 001');
	expect(home).not.toContain('Plugin 100');
	for (const path of ['page/5/index.html', 'sort/name/page/5/index.html', 'sort/updated/page/5/index.html']) {
		const page = await readFile(join(directory, path), 'utf8');
		expect(page.match(/class="plugin-card"/g)).toHaveLength(20);
		expect(page).toContain('rel="prev"');
	}
	expect(await readFile(join(directory, 'index.json'), 'utf8')).toBe(before);
	expect((await stat(join(directory, 'assets/client.js'))).size).toBeGreaterThan(0);
	expect((await stat(join(directory, 'assets/search-index.json'))).size).toBeGreaterThan(0);
	const named = await readFile(join(directory, 'sort/name/index.html'), 'utf8');
	expect(named).toContain('href="../../assets/site.css"');
	expect(named).toContain('href="../../plugins/');
});

test('empty export builds useful static fallback pages', async () => {
	const directory = await createExport([]);
	expect(await buildSite(directory, directory)).toBe(0);
	const home = await readFile(join(directory, 'index.html'), 'utf8');
	expect(home).toContain('No plugins published yet.');
	expect(home).toContain('Publish your first plugin');
	expect(home).not.toContain('class="plugin-card"');
});

test('separate output keeps publication records reachable and byte-for-byte intact', async () => {
	const directory = await createExport([fixture('Plugin')]);
	const output = join(directory, 'website');
	await buildSite(directory, output);
	const record = `plugins/${'1'.padStart(64, '0')}/versions/${'1'.padStart(64, '0')}.json`;
	expect(await readFile(join(output, 'index.json'), 'utf8')).toBe(
		await readFile(join(directory, 'index.json'), 'utf8'),
	);
	expect(await readFile(join(output, record), 'utf8')).toBe(await readFile(join(directory, record), 'utf8'));
});
