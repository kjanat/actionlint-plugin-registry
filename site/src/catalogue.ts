import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { catalogueSchema, downloadsSchema, entrySchema, manifestSchema, statusSchema } from '@/src/schema.ts';
import type { DownloadSnapshot, Entry, Metric, Plugin, Release, ReleaseStatus, SiteData } from '@/src/schema.ts';

async function readJSON(path: string): Promise<unknown> {
	return JSON.parse(await readFile(path, 'utf8'));
}

function releaseMetric(entry: Entry, downloads: DownloadSnapshot | null): Metric {
	if (downloads === null) return { state: 'not collected' };
	const found = downloads.releases.find(item =>
		item.provider === entry.submission.provider && item.repository_id === entry.repository_id
		&& item.version === entry.submission.tag && item.asset_id === entry.asset_id
		&& item.sha256 === entry.submission.sha256
	);
	if (!found) return { state: 'unavailable' };
	if (found.status === 'available' && found.count !== undefined) return { state: 'available', count: found.count };
	return { state: found.status === 'unsupported' ? 'unsupported' : 'unavailable' };
}

function totalMetric(releases: Release[]): Metric {
	let total = 0;
	for (const release of releases) {
		if (release.downloads.state !== 'available') return release.downloads;
		total += release.downloads.count;
		if (!Number.isSafeInteger(total)) return { state: 'unavailable' };
	}
	return { state: 'available', count: total };
}

function statusFor(entry: Entry, statuses: ReleaseStatus[]): ReleaseStatus | undefined {
	return statuses.find(item => item.status === 'revoked' && item.sha256 === entry.submission.sha256)
		?? statuses.findLast(item =>
			item.name === `${entry.submission.provider}.com/${entry.submission.repository}`
			&& item.version === entry.submission.tag
		);
}

export async function loadCatalogue(directory: string): Promise<SiteData> {
	const catalogue = catalogueSchema.parse(await readJSON(join(directory, 'index.json')));
	const statuses = statusSchema.parse(await readJSON(join(directory, catalogue.statuses ?? 'statuses.json')));
	let downloads: DownloadSnapshot | null = null;
	try {
		downloads = downloadsSchema.parse(await readJSON(join(directory, 'downloads.json')));
	} catch (error) {
		if (!(error instanceof Error && 'code' in error && error.code === 'ENOENT')) throw error;
	}
	const plugins = await Promise.all(catalogue.plugins.map(async item => {
		const manifest = manifestSchema.parse(await readJSON(join(directory, item.manifest)));
		if (
			`${manifest.plugin.provider}.com/${manifest.plugin.repository}` !== item.name
			|| manifest.plugin.plugin_id !== item.plugin_id
		) {
			throw new Error(`Manifest identity disagrees with catalogue: ${item.name}`);
		}
		const reference = item.aliases.toSorted()[0] ?? item.name;
		const entries = await Promise.all(manifest.versions.map(async version => {
			const record = join(dirname(item.manifest), version.record).replaceAll('\\', '/');
			const entry = entrySchema.parse(await readJSON(join(directory, record)));
			if (
				entry.submission.tag !== version.version
				|| `${entry.submission.provider}.com/${entry.submission.repository}` !== item.name
				|| String(entry.repository_id) !== manifest.plugin.repository_id
			) throw new Error(`Release identity disagrees with manifest: ${record}`);
			const status = statusFor(entry, statuses.releases);
			const release: Release = {
				version: entry.submission.tag,
				kind: entry.submission.kind,
				asset: entry.submission.asset,
				sha256: entry.submission.sha256,
				pinnedURL: `${entry.url}#sha256=${entry.submission.sha256}`,
				record,
				publishedAt: entry.published_at,
				reference: `${reference}@${entry.submission.tag}`,
				state: status?.status ?? 'active',
				reason: status?.reason ?? '',
				replacement: status?.replacement ?? '',
				downloads: releaseMetric(entry, downloads),
			};
			return { entry, release };
		}));
		entries.sort((a, b) =>
			Date.parse(b.release.publishedAt) - Date.parse(a.release.publishedAt)
			|| b.release.version.localeCompare(a.release.version)
		);
		const newest = entries[0];
		if (!newest) throw new Error(`No releases for ${item.name}`);
		const declared = newest.entry.artifact?.metadata ?? newest.entry.validation?.metadata;
		const releases = entries.map(entry => entry.release);
		const plugin: Plugin = {
			name: declared?.name || reference,
			description: declared?.description ?? '',
			license: declared?.license ?? '',
			id: item.plugin_id,
			repository: item.name,
			repositoryURL: `https://${item.name}`,
			path: item.manifest.replace(/index\.json$/, 'index.html'),
			version: newest.release.version,
			updatedAt: newest.release.publishedAt,
			downloads: totalMetric(releases),
			releases,
		};
		return plugin;
	}));
	return { plugins, observedAt: downloads?.observed_at ?? null };
}
