import { describe, expect, test } from 'bun:test';
import { readFile, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { loadCatalogue } from '#catalogue.ts';
import { downloadsSchema } from '#schema.ts';
import { createExport, fixture } from '#test/fixtures.ts';

describe('public registry JSON projection', () => {
	test('groups versions and follows newest publication, even when its version sorts earlier', async () => {
		const directory = await createExport([{
			name: 'Plugin',
			versions: [
				{ version: 'v10.0.0', publishedAt: '2026-01-01T00:00:00Z', count: 4 },
				{
					version: 'v2.0.0',
					publishedAt: '2026-02-01T00:00:00Z',
					count: 7,
					metadata: { name: 'Updated', description: 'Current description', license: 'Apache-2.0' },
				},
			],
		}]);
		const data = await loadCatalogue(directory);
		expect(data.plugins).toHaveLength(1);
		expect(data.plugins[0]).toMatchObject({
			name: 'Updated',
			description: 'Current description',
			license: 'Apache-2.0',
			version: 'v2.0.0',
			downloads: { state: 'available', count: 11 },
		});
		expect(data.plugins[0]?.releases.map(release => release.version)).toEqual(['v2.0.0', 'v10.0.0']);
	});

	test('keeps zero, partial, absent and overflowing counts distinct', async () => {
		const directory = await createExport([
			fixture('Zero', 0),
			{
				name: 'Partial',
				versions: [{ version: 'v1', publishedAt: '2026-01-01T00:00:00Z', count: 30 }, {
					version: 'v2',
					publishedAt: '2026-01-02T00:00:00Z',
					count: null,
				}],
			},
			{
				name: 'Overflow',
				versions: [{ version: 'v1', publishedAt: '2026-01-01T00:00:00Z', count: Number.MAX_SAFE_INTEGER }, {
					version: 'v2',
					publishedAt: '2026-01-02T00:00:00Z',
					count: 1,
				}],
			},
		]);
		expect((await loadCatalogue(directory)).plugins.map(plugin => plugin.downloads)).toEqual([
			{ state: 'available', count: 0 },
			{ state: 'unavailable' },
			{ state: 'unavailable' },
		]);
		await rm(join(directory, 'downloads.json'));
		expect((await loadCatalogue(directory)).plugins.every(plugin => plugin.downloads.state === 'not collected')).toBe(
			true,
		);
	});

	test.each(['provider', 'repository_id', 'version', 'asset_id', 'sha256'])(
		'rejects a count from a different %s',
		async field => {
			const directory = await createExport([fixture('Plugin')]);
			const snapshot = downloadsSchema.parse(JSON.parse(await readFile(join(directory, 'downloads.json'), 'utf8')));
			const observation = snapshot.releases[0];
			if (!observation) throw new Error('Missing fixture observation');
			if (field === 'provider') observation.provider = 'gitlab';
			if (field === 'repository_id') observation.repository_id++;
			if (field === 'version') observation.version = 'other';
			if (field === 'asset_id') observation.asset_id++;
			if (field === 'sha256') observation.sha256 = 'f'.repeat(64);
			await writeFile(join(directory, 'downloads.json'), JSON.stringify(snapshot));
			expect((await loadCatalogue(directory)).plugins[0]?.downloads).toEqual({ state: 'unavailable' });
		},
	);

	test('case variants remain unknown extension fields', async () => {
		const directory = await createExport([{
			name: 'Plugin',
			versions: [{
				version: 'v1',
				publishedAt: '2026-01-01T00:00:00Z',
				count: 1,
				metadata: {
					name: 'Correct',
					description: 'Correct description',
					license: 'MIT',
					Name: { future: true },
					Description: ['future'],
					License: 'Wrong',
				},
			}],
		}]);
		expect((await loadCatalogue(directory)).plugins[0]).toMatchObject({
			name: 'Correct',
			description: 'Correct description',
			license: 'MIT',
		});
	});

	test('only curated aliases supply shorthand; digest revocation covers every tag', async () => {
		const sha256 = 'f'.repeat(64);
		const directory = await createExport([{
			name: 'Metadata does not own an alias',
			aliases: ['curated'],
			versions: [
				{ version: 'v1', publishedAt: '2026-01-01T00:00:00Z', count: 1, sha256, state: 'revoked' },
				{ version: 'v2', publishedAt: '2026-01-02T00:00:00Z', count: 2, sha256 },
			],
		}, fixture('No alias')]);
		const data = await loadCatalogue(directory);
		expect(data.plugins[0]?.releases.map(release => [release.reference, release.state])).toEqual([[
			'curated@v2',
			'revoked',
		], ['curated@v1', 'revoked']]);
		expect(data.plugins[1]?.releases[0]?.reference).toBe('github.com/publisher/plugin-2@v1.0.0');
	});
});
