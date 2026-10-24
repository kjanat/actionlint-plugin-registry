import { expect, test } from 'bun:test';
import { readState, selectPlugins, stateLink } from '#directory.ts';
import type { DirectoryState } from '#directory.ts';
import type { PluginSummary } from '#schema.ts';
import { localTime } from '#time.tsx';

export const hundred: PluginSummary[] = Array.from({ length: 100 }, (_, index) => ({
	name: `Plugin ${String(index + 1).padStart(3, '0')}`,
	description: `Description ${index + 1}`,
	repository: `github.com/publisher/plugin-${index + 1}`,
	path: `plugins/${String(index + 1).padStart(64, '0')}/index.html`,
	version: `v${index + 1}.0.0`,
	updatedAt: new Date(Date.UTC(2026, 0, index + 1)).toISOString(),
	downloads: index === 99 ? { state: 'unavailable' } : { state: 'available', count: index },
}));

test('100 plugins produce five bounded pages; downloads default puts unknown after zero', () => {
	const state = readState(new URL('https://example.test/registry/'));
	const first = selectPlugins(hundred, state);
	expect(first.items).toHaveLength(20);
	expect(first.pages).toBe(5);
	expect(first.items[0]?.name).toBe('Plugin 099');
	expect(selectPlugins(hundred, { ...state, page: 5 }).items.at(-1)?.name).toBe('Plugin 100');
	expect(
		new Set(
			Array.from({ length: 5 }, (_, index) => selectPlugins(hundred, { ...state, page: index + 1 }).items).flat().map(
				plugin => plugin.path,
			),
		).size,
	).toBe(100);
});

test('name ties are stable; recent updates compare UTC instants', () => {
	const parsed = hundred.slice(0, 2).map((plugin, index): PluginSummary => ({
		...plugin,
		downloads: { state: 'available', count: 4 },
		updatedAt: index === 0 ? '2026-03-29T03:00:00+02:00' : '2026-03-29T00:30:00Z',
	}));
	expect(selectPlugins(parsed.toReversed(), { query: '', sort: 'downloads', page: 1 }).items.map(plugin => plugin.name))
		.toEqual(['Plugin 001', 'Plugin 002']);
	expect(selectPlugins(parsed, { query: '', sort: 'updated', page: 1 }).items[0]?.name).toBe('Plugin 001');
});

test('search covers name, description and repository; empty results and excessive pages normalize', () => {
	expect(selectPlugins(hundred, { query: 'PLUGIN 042', sort: 'name', page: 90 })).toMatchObject({ page: 1, count: 1 });
	expect(selectPlugins(hundred, { query: 'publisher/plugin-77', sort: 'name', page: 1 }).items[0]?.name).toBe(
		'Plugin 077',
	);
	expect(selectPlugins(hundred, { query: 'no such plugin', sort: 'name', page: 1 })).toMatchObject({
		items: [],
		pages: 1,
		count: 0,
	});
	expect(selectPlugins([], { query: '', sort: 'downloads', page: 1 }).items).toEqual([]);
});

test('static sort/page paths and query state round trip under a project base path', () => {
	const state: DirectoryState = { query: 'shell & check', sort: 'updated', page: 3 };
	const parsed = readState(new URL(stateLink('/registry/', { ...state, sort: 'updated' }), 'https://example.test'));
	expect(parsed).toEqual(state);
	expect(readState(new URL('https://example.test/registry/sort/name/page/2/index.html'))).toEqual({
		query: '',
		sort: 'name',
		page: 2,
	});
});

test('local dates respect locale, day boundaries and DST transitions', () => {
	expect(localTime('2026-03-29T00:30:00Z', 'nl-NL', 'Europe/Amsterdam')).toContain('01:30');
	expect(localTime('2026-03-29T01:30:00Z', 'nl-NL', 'Europe/Amsterdam')).toContain('03:30');
	expect(localTime('2026-03-29T00:30:00Z', 'en-US', 'America/Los_Angeles')).toContain('Mar 28');
	expect(localTime('2026-11-01T08:30:00Z', 'en-US', 'America/Los_Angeles')).toContain('PDT');
	expect(localTime('2026-11-01T09:30:00Z', 'en-US', 'America/Los_Angeles')).toContain('PST');
});
