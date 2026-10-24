import { createHash } from 'node:crypto';
import { openAsBlob } from 'node:fs';
import { appendFile, lstat, readdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const registryOrigin = 'https://registry.actionlint.kjanat.dev';
const filePattern = /^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/;
const digestPattern = /^[0-9a-f]{64}$/;
const identifierPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

import { HTTPClient, HTTPError, oidcToken, output, requestJSON, TransportError } from '@/scripts/http.ts';

export interface PackageFile {
	name: string;
	path: string;
	sha256: string;
	size: number;
}

export interface Package {
	asset: string;
	sha256: string;
	files: PackageFile[];
}

function object(value: unknown): Record<string, unknown> {
	if (typeof value !== 'object' || value === null || Array.isArray(value)) {
		throw new Error('Expected a JSON object');
	}
	return Object.fromEntries(Object.entries(value));
}

function text(value: unknown, label: string): string {
	if (typeof value !== 'string' || !value || /[\r\n]/.test(value)) {
		throw new Error(`Invalid ${label}`);
	}
	return value;
}

function integer(value: unknown, label: string): number {
	if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 1) {
		throw new Error(`Invalid ${label}`);
	}
	return value;
}

export async function verifyPackage(
	directory: string,
	asset: string,
	expected: string,
): Promise<Package> {
	if (
		!filePattern.test(asset)
		|| !/\.wasm(?:\.gz)?$/.test(asset)
		|| !digestPattern.test(expected)
	) {
		throw new Error('Supply a Wasm asset filename and its tested SHA-256');
	}
	const manifest = join(directory, 'SHA256SUMS');
	if (!(await lstat(manifest)).isFile()) {
		throw new Error('SHA256SUMS must be a regular file');
	}
	const checksums = new Map<string, string>();
	for (
		const line of (await readFile(manifest, 'utf8'))
			.trimEnd()
			.split(/\r?\n/)
	) {
		const match = /^([0-9a-f]{64}) {2}([A-Za-z0-9][A-Za-z0-9._+-]{0,127})$/.exec(line);
		const checksum = match?.[1];
		const name = match?.[2];
		if (!checksum || !name || name === 'SHA256SUMS' || checksums.has(name)) {
			throw new Error('Malformed or duplicate package checksums');
		}
		checksums.set(name, checksum);
	}
	const names = await readdir(directory);
	if (
		names.length !== checksums.size + 1
		|| names.some((name) => name !== 'SHA256SUMS' && !checksums.has(name))
	) {
		throw new Error(
			'Every package file must appear exactly once in SHA256SUMS',
		);
	}
	if (checksums.get(asset) !== expected) {
		throw new Error('Package differs from the tested Wasm artifact');
	}
	const files: PackageFile[] = [];
	for (const name of names.sort()) {
		const path = join(directory, name);
		const stat = await lstat(path);
		if (!stat.isFile()) {
			throw new Error(`Package member must be a regular file: ${name}`);
		}
		const hash = createHash('sha256');
		const blob = await openAsBlob(path);
		for await (const chunk of blob.stream()) hash.update(chunk);
		const sha256 = hash.digest('hex');
		if (name !== 'SHA256SUMS' && sha256 !== checksums.get(name)) {
			throw new Error(`Package checksum mismatch: ${name}`);
		}
		files.push({ name, path, sha256, size: stat.size });
	}
	return { asset, sha256: expected, files };
}

interface Release {
	id: number;
	draft: boolean;
	immutable: boolean;
	assets: { id: number; name: string }[];
}

function release(value: Record<string, unknown>): Release {
	if (typeof value.draft !== 'boolean' || !Array.isArray(value.assets)) {
		throw new Error('Invalid release response');
	}
	return {
		id: integer(value.id, 'release ID'),
		draft: value.draft,
		immutable: value.immutable === true,
		assets: value.assets.map((value) => {
			const asset = object(value);
			return {
				id: integer(asset.id, 'asset ID'),
				name: text(asset.name, 'asset filename'),
			};
		}),
	};
}

export class GitHubPublisher {
	readonly http: HTTPClient;
	readonly api: string;
	readonly uploads: string;
	readonly token: string;
	constructor(
		token: string,
		http = new HTTPClient(),
		api = 'https://api.github.com',
		uploads = 'https://uploads.github.com',
	) {
		this.token = text(token, 'GitHub token');
		this.http = http;
		this.api = api;
		this.uploads = uploads;
	}

	private headers() {
		return {
			Authorization: `Bearer ${this.token}`,
			Accept: 'application/vnd.github+json',
			'Content-Type': 'application/json',
		};
	}

	private async compare(
		repository: string,
		existing: Release,
		pkg: Package,
	): Promise<PackageFile[]> {
		const seen = new Set<string>();
		for (const asset of existing.assets) {
			const file = pkg.files.find((file) => file.name === asset.name);
			if (!file || seen.has(asset.name)) {
				throw new Error('Release has unexpected or duplicate assets');
			}
			seen.add(asset.name);
			const url = `${this.api}/repos/${repository}/releases/assets/${asset.id}`;
			// GitHub serves draft assets through its authenticated API, possibly via a signed redirect.
			const first = await fetch(url, {
				headers: { ...this.headers(), Accept: 'application/octet-stream' },
				redirect: 'manual',
				signal: AbortSignal.timeout(30_000),
			});
			let response = first;
			if (first.status === 302) {
				const location = new URL(
					text(first.headers.get('location'), 'asset download URL'),
				);
				if (
					location.protocol !== 'https:'
					|| !location.hostname.endsWith('.githubusercontent.com')
					|| location.username
					|| location.password
				) {
					throw new Error('Unexpected asset download destination');
				}
				await first.body?.cancel();
				response = await this.http.response(location.href);
			}
			if (!response.ok || !response.body) {
				throw new Error('Release asset download failed');
			}
			let size = 0;
			const hash = createHash('sha256');
			for await (const chunk of response.body) {
				size += chunk.length;
				if (size > file.size) {
					throw new Error(`Release asset differs: ${file.name}`);
				}
				hash.update(chunk);
			}
			if (size !== file.size || hash.digest('hex') !== file.sha256) {
				throw new Error(`Release asset differs: ${file.name}`);
			}
		}
		const missing = pkg.files.filter((file) => !seen.has(file.name));
		if (missing.length && !existing.draft) {
			throw new Error('Published release is incomplete; refusing to change it');
		}
		return missing;
	}

	async publish(
		repository: string,
		tag: string,
		pkg: Package,
		notes: string,
	): Promise<void> {
		if (
			!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)
			|| !filePattern.test(tag)
		) {
			throw new Error('Invalid repository or release tag');
		}
		const endpoint = `${this.api}/repos/${repository}/releases`;
		const reference = await this.http.json(
			`${this.api}/repos/${repository}/git/ref/tags/${tag}`,
			{
				headers: this.headers(),
			},
		);
		if (reference.ref !== `refs/tags/${tag}`) {
			throw new Error('Release tag must already exist');
		}
		let existing: Release;
		try {
			existing = release(
				await this.http.json(`${endpoint}/tags/${tag}`, {
					headers: this.headers(),
				}),
			);
		} catch (error) {
			if (!(error instanceof HTTPError) || error.status !== 404) throw error;
			existing = release(
				await this.http.json(endpoint, {
					method: 'POST',
					headers: this.headers(),
					body: JSON.stringify({
						tag_name: tag,
						name: tag,
						draft: true,
						prerelease: tag.includes('-'),
						body: notes,
					}),
				}),
			);
		}
		for (const file of await this.compare(repository, existing, pkg)) {
			await this.http.json(
				`${this.uploads}/repos/${repository}/releases/${existing.id}/assets?name=${encodeURIComponent(file.name)}`,
				{
					method: 'POST',
					headers: {
						...this.headers(),
						'Content-Type': 'application/octet-stream',
					},
					body: await openAsBlob(file.path),
				},
			);
		}
		existing = release(
			await this.http.json(`${endpoint}/${existing.id}`, {
				headers: this.headers(),
			}),
		);
		if ((await this.compare(repository, existing, pkg)).length) {
			throw new Error('Release upload is incomplete');
		}
		if (existing.draft) {
			existing = release(
				await this.http.json(`${endpoint}/${existing.id}`, {
					method: 'PATCH',
					headers: this.headers(),
					body: JSON.stringify({ draft: false }),
				}),
			);
		}
		if (!existing.immutable) {
			throw new Error(
				'Release is not immutable; enable repository release immutability before publication',
			);
		}
	}
}

export interface Submission {
	repository: string;
	tag: string;
	asset: string;
	sha256: string;
	kind: 'core' | 'component';
}

export async function verifyCandidateReceipt(
	path: string,
	entry: Submission,
	actionlint: string,
	fixturePath: string,
): Promise<void> {
	const value: unknown = JSON.parse(await readFile(path, 'utf8'));
	const receipt = object(value);
	const artifact = object(receipt.artifact);
	const installation = object(receipt.installation);
	const host = object(receipt.host);
	const fixture = object(receipt.fixture);
	if (
		receipt.schema_version !== 1
		|| receipt.offline !== true
		|| receipt.missing_cache_rejected !== true
		|| artifact.sha256 !== entry.sha256
		|| artifact.kind !== entry.kind
		|| artifact.repository !== `https://github.com/${entry.repository}`
		|| artifact.plugin_version !== entry.tag.replace(/^v/, '')
		|| installation.sha256 !== entry.sha256
		|| installation.version !== entry.tag
		|| !digestPattern.test(text(host.sha256, 'candidate host digest'))
		|| !digestPattern.test(text(fixture.sha256, 'candidate fixture digest'))
		|| !Array.isArray(fixture.checks)
		|| fixture.checks.length === 0
		|| fixture.checks.some((check) => typeof check !== 'string' || !check.trim())
	) {
		throw new Error(
			'Candidate receipt does not match this release or lacks successful installation and offline checks',
		);
	}
	for (
		const [file, expected, label] of [
			[actionlint, host.sha256, 'host'],
			[fixturePath, fixture.sha256, 'fixture'],
		]
	) {
		if (typeof file !== 'string') throw new Error('Missing verification file');
		const hash = createHash('sha256');
		for await (const chunk of (await openAsBlob(file)).stream()) hash.update(chunk);
		if (hash.digest('hex') !== expected) {
			throw new Error(`Candidate ${label} changed since verification`);
		}
	}
}

export async function submit(
	entry: Submission,
	environ: NodeJS.ProcessEnv,
	http = new HTTPClient(),
	provider: 'github' | 'gitlab' = 'github',
): Promise<string> {
	const repository = entry.repository.toLowerCase();
	const segments = repository.split('/');
	if (
		repository.length > 255
		|| segments.length < 2
		|| (provider === 'github' && segments.length !== 2)
		|| segments.some((segment) => !/^[a-z0-9][a-z0-9._-]{0,99}$/.test(segment))
	) {
		throw new Error('Invalid repository path');
	}
	if (!filePattern.test(entry.tag)) throw new Error('Invalid release tag');
	if (!filePattern.test(entry.asset) || !/\.wasm(?:\.gz)?$/.test(entry.asset)) {
		throw new Error('Asset must be a Wasm release filename');
	}
	if (!digestPattern.test(entry.sha256)) {
		throw new Error('Invalid artifact SHA-256');
	}
	const token = await oidcToken(registryOrigin, environ, provider, http);
	const response = await requestJSON(
		`${registryOrigin}/v1/submissions`,
		token,
		{
			schema_version: 1,
			provider,
			repository,
			tag: entry.tag,
			asset: entry.asset,
			sha256: entry.sha256,
			kind: entry.kind,
		},
		'Registry submission',
		http,
	);
	const identifier = text(response.id, 'submission ID');
	if (!identifierPattern.test(identifier)) {
		throw new Error('Invalid submission ID');
	}
	const status = text(response.status, 'submission status');
	if (
		!['pending', 'validating', 'published', 'rejected', 'failed'].includes(
			status,
		)
	) {
		throw new Error('Unexpected submission status');
	}
	await output('submission-id', identifier);
	await output('submission-status', status);
	console.log(
		`Registry submission ${identifier}: ${status}; ${registryOrigin}/v1/submissions/${identifier}`,
	);
	if (status === 'rejected' || status === 'failed') {
		throw new Error(`Registry submission ${identifier} is ${status}`);
	}
	return identifier;
}

interface WaitOptions {
	timeoutMs?: number;
	intervalMs?: number;
	origin?: string;
	http?: HTTPClient;
	sleep?: (milliseconds: number) => Promise<void>;
	now?: () => number;
}

export async function waitForPublication(
	identifier: string,
	sha256: string,
	receiptPath: string,
	options: WaitOptions = {},
): Promise<void> {
	if (!identifierPattern.test(identifier) || !digestPattern.test(sha256)) {
		throw new Error('Invalid submission ID or artifact digest');
	}
	const http = options.http ?? new HTTPClient();
	const now = options.now ?? Date.now;
	const sleep = options.sleep
		?? ((milliseconds) => new Promise((done) => setTimeout(done, milliseconds)));
	const deadline = now() + (options.timeoutMs ?? 1_800_000);
	const url = `${options.origin ?? registryOrigin}/v1/submissions/${identifier}`;
	let status = 'pending';
	let validator = '';
	let failures = 0;
	await writeFile(
		receiptPath,
		`${JSON.stringify({ id: identifier, sha256, status_url: url, status }, null, 2)}\n`,
	);
	for (;;) {
		let delay = options.intervalMs ?? 15_000;
		try {
			const response = await http.json(url);
			if (response.id !== identifier) {
				throw new Error('Registry returned a different submission');
			}
			status = text(response.status, 'submission status');
			validator = typeof response.validator_run === 'string'
					&& /^https:\/\/github\.com\/[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+\/actions\/runs\/\d+(?:\/attempts\/\d+)?$/.test(
						response.validator_run,
					)
				? response.validator_run
				: '';
			await writeFile(
				receiptPath,
				`${JSON.stringify({ id: identifier, sha256, status_url: url, status, response }, null, 2)}\n`,
			);
			console.log(
				`Registry submission ${identifier}: ${status}${validator ? `; ${validator}` : ''}`,
			);
			failures = 0;
			if (status === 'published') return;
			if (status !== 'pending' && status !== 'validating') {
				throw new Error(`Registry publication ${status}; ${validator || url}`);
			}
		} catch (error) {
			if (
				!(error instanceof TransportError)
				&& !(
					error instanceof HTTPError
					&& (error.status === 408 || error.status === 429 || error.status >= 500)
				)
			) {
				throw error;
			}
			failures += 1;
			delay = Math.max(
				Math.min(60_000, 1000 * 2 ** Math.min(failures, 6)),
				error instanceof HTTPError ? error.retryAfter : 0,
			);
			console.log(
				`Registry status temporarily unavailable; retrying in ${Math.ceil(delay / 1000)}s; ${validator || url}`,
			);
		}
		const remaining = deadline - now();
		if (remaining <= 0) {
			throw new Error(
				`Registry publication still ${status}; ${validator || url}. Rerun publication with identical bytes to resume.`,
			);
		}
		await sleep(Math.min(delay, remaining));
	}
}

export interface PublishOptions {
	actionlint: string;
	fixture: string;
	directory: string;
	asset: string;
	sha256: string;
	repository: string;
	tag: string;
	kind: 'component' | 'core';
	notesFile: string;
	candidateReceipt: string;
	receipt: string;
	timeoutSeconds: number;
}

export async function publishPackage(options: PublishOptions): Promise<void> {
	const pkg = await verifyPackage(
		options.directory,
		options.asset,
		options.sha256,
	);
	if (
		!Number.isInteger(options.timeoutSeconds)
		|| options.timeoutSeconds < 1
		|| options.timeoutSeconds > 7200
	) {
		throw new Error('Timeout must be between 1 and 7200 seconds');
	}
	const notes = options.notesFile
		? await readFile(options.notesFile, 'utf8')
		: '';
	const entry: Submission = {
		repository: options.repository,
		tag: options.tag,
		asset: pkg.asset,
		sha256: pkg.sha256,
		kind: options.kind,
	};
	await verifyCandidateReceipt(options.candidateReceipt, entry, options.actionlint, options.fixture);
	if (
		process.env.GITHUB_ACTIONS === 'true'
		&& (process.env.GITHUB_REF !== `refs/tags/${options.tag}`
			|| process.env.GITHUB_REPOSITORY?.toLowerCase()
				!== options.repository.toLowerCase())
	) {
		throw new Error('Publication must run for this repository and release tag');
	}
	// Check OIDC permissions before making the release public; obtain a fresh token for submission.
	await oidcToken(registryOrigin, process.env);
	await new GitHubPublisher(text(process.env.GH_TOKEN, 'GH_TOKEN')).publish(
		options.repository,
		options.tag,
		pkg,
		notes,
	);
	const identifier = await submit(entry, process.env);
	if (process.env.GITHUB_OUTPUT) {
		await appendFile(
			process.env.GITHUB_OUTPUT,
			`submission-id=${identifier}\nsha256=${pkg.sha256}\n`,
		);
	}
	await waitForPublication(identifier, pkg.sha256, options.receipt, {
		timeoutMs: options.timeoutSeconds * 1000,
	});
	if (process.env.GITHUB_OUTPUT) {
		await appendFile(
			process.env.GITHUB_OUTPUT,
			'submission-status=published\n',
		);
	}
}
