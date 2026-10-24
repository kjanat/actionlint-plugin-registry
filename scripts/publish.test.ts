import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { HTTPClient, HTTPError } from '@/scripts/http.ts';
import { GitHubPublisher, verifyCandidateReceipt, verifyPackage, waitForPublication } from '@/scripts/publish.ts';

const identifier = '12345678-1234-4234-8234-123456789abc';
const checksum = (value: string | Buffer) => createHash('sha256').update(value).digest('hex');

async function fixture() {
	const directory = await mkdtemp(join(tmpdir(), 'registry-publication-'));
	const wasm = Buffer.from('\0asm-candidate');
	const metadata = Buffer.from('{"name":"test"}');
	await writeFile(join(directory, 'plugin.wasm'), wasm);
	await writeFile(join(directory, 'metadata.json'), metadata);
	await writeFile(
		join(directory, 'SHA256SUMS'),
		`${checksum(wasm)}  plugin.wasm\n${checksum(metadata)}  metadata.json\n`,
	);
	return {
		directory,
		sha256: checksum(wasm),
		cleanup: () => rm(directory, { recursive: true, force: true }),
	};
}

async function server(
	handler: (
		request: IncomingMessage,
		response: ServerResponse,
	) => Promise<void> | void,
) {
	const listener = createServer((request, response) => {
		Promise.resolve(handler(request, response)).catch((error) => {
			response.writeHead(500);
			response.end(String(error));
		});
	});
	await new Promise<void>((done) => listener.listen(0, '127.0.0.1', done));
	const address = listener.address();
	if (!address || typeof address === 'string') {
		throw new Error('Missing test listener address');
	}
	return {
		origin: `http://127.0.0.1:${address.port}`,
		close: () => new Promise<void>((done, reject) => listener.close((error) => (error ? reject(error) : done()))),
	};
}

async function body(request: IncomingMessage): Promise<Buffer> {
	const chunks: Buffer[] = [];
	for await (const chunk of request) chunks.push(Buffer.from(chunk));
	return Buffer.concat(chunks);
}

function json(response: ServerResponse, value: unknown, status = 200) {
	response.writeHead(status, { 'content-type': 'application/json' });
	response.end(JSON.stringify(value));
}

test('modified sidecars and extra files stop package publication', async () => {
	// A passing candidate digest cannot excuse changed metadata or an unlisted upload. These
	// cases use real package bytes and require verification to reject the changed package.
	const sample = await fixture();
	try {
		const pkg = await verifyPackage(
			sample.directory,
			'plugin.wasm',
			sample.sha256,
		);
		assert.deepEqual(
			pkg.files.map((file) => file.name),
			['SHA256SUMS', 'metadata.json', 'plugin.wasm'],
		);
		await writeFile(join(sample.directory, 'metadata.json'), '{}');
		await assert.rejects(
			verifyPackage(sample.directory, 'plugin.wasm', sample.sha256),
			/checksum mismatch/,
		);
		await writeFile(join(sample.directory, 'extra'), 'unreviewed');
		await assert.rejects(
			verifyPackage(sample.directory, 'plugin.wasm', sample.sha256),
			/Every package file/,
		);
	} finally {
		await sample.cleanup();
	}
});

test('candidate receipt must describe the artifact being published and completed offline checks', async () => {
	// A receipt from another build or an incomplete rehearsal must stop before GitHub publication.
	// Mutate each release identity and required outcome while keeping the other successful evidence.
	const sample = await fixture();
	const path = join(sample.directory, 'receipt.json');
	const host = join(sample.directory, 'host');
	const behavior = join(sample.directory, 'installation.ts');
	await writeFile(host, 'tested host');
	await writeFile(behavior, 'tested fixture');
	const entry = {
		repository: 'owner/repo',
		tag: 'v1.0.0',
		asset: 'plugin.wasm',
		sha256: sample.sha256,
		kind: 'component',
	};
	const valid = {
		schema_version: 1,
		artifact: {
			sha256: sample.sha256,
			repository: 'https://github.com/owner/repo',
			plugin_version: '1.0.0',
			kind: 'component',
		},
		installation: { version: 'v1.0.0', sha256: sample.sha256 },
		host: { sha256: checksum('tested host') },
		fixture: { sha256: checksum('tested fixture'), checks: ['source following'] },
		offline: true,
		missing_cache_rejected: true,
	};
	try {
		await writeFile(path, JSON.stringify(valid));
		await verifyCandidateReceipt(path, { ...entry, kind: 'component' }, host, behavior);
		for (
			const invalid of [
				{ ...valid, offline: false },
				{ ...valid, missing_cache_rejected: false },
				{ ...valid, artifact: { ...valid.artifact, sha256: 'c'.repeat(64) } },
				{
					...valid,
					artifact: {
						...valid.artifact,
						repository: 'https://github.com/other/repo',
					},
				},
				{ ...valid, artifact: { ...valid.artifact, plugin_version: '1.0.1' } },
				{ ...valid, installation: { ...valid.installation, version: 'v0.9.0' } },
				{ ...valid, fixture: { ...valid.fixture, checks: [] } },
			]
		) {
			await writeFile(path, JSON.stringify(invalid));
			await assert.rejects(
				verifyCandidateReceipt(path, { ...entry, kind: 'component' }, host, behavior),
				/Candidate receipt/,
			);
		}
		await writeFile(path, JSON.stringify(valid));
		await writeFile(host, 'different host');
		await assert.rejects(verifyCandidateReceipt(path, { ...entry, kind: 'component' }, host, behavior), /host changed/);
		await writeFile(host, 'tested host');
		await writeFile(behavior, 'different fixture');
		await assert.rejects(
			verifyCandidateReceipt(path, { ...entry, kind: 'component' }, host, behavior),
			/fixture changed/,
		);
	} finally {
		await sample.cleanup();
	}
});

test('GitHub publication downloads every uploaded byte before making a draft public', async () => {
	// Exercise the actual HTTP publisher against a release service that stores upload bodies.
	// Corrupt one download and require the draft to stay private; matching bytes may publish once.
	for (const corrupt of [false, true]) {
		const sample = await fixture();
		const uploads = new Map<number, { name: string; bytes: Buffer }>();
		let draft = true;
		let created = false;
		let publishes = 0;
		let downloads = 0;
		const description = () => ({
			id: 7,
			draft,
			immutable: !draft,
			assets: [...uploads].map(([id, file]) => ({ id, name: file.name })),
		});
		const remote = await server(async (request, response) => {
			assert.equal(request.headers.authorization, 'Bearer publisher-token');
			const url = new URL(request.url ?? '/', 'http://test');
			if (url.pathname.includes('/git/ref/tags/')) {
				return json(response, { ref: 'refs/tags/v1.0.0' });
			}
			if (url.pathname.endsWith('/tags/v1.0.0')) {
				return json(
					response,
					created ? description() : {},
					created ? 200 : 404,
				);
			}
			if (
				url.pathname === '/repos/owner/repo/releases'
				&& request.method === 'POST'
			) {
				const value: unknown = JSON.parse((await body(request)).toString());
				assert.deepEqual(value, {
					tag_name: 'v1.0.0',
					name: 'v1.0.0',
					draft: true,
					prerelease: false,
					body: 'Fix source tracking.',
				});
				created = true;
				return json(response, description());
			}
			if (url.pathname.endsWith('/7/assets') && request.method === 'POST') {
				const name = url.searchParams.get('name');
				assert.ok(name);
				uploads.set(uploads.size + 1, { name, bytes: await body(request) });
				return json(response, {});
			}
			if (url.pathname.includes('/releases/assets/')) {
				const asset = uploads.get(Number(url.pathname.split('/').at(-1)));
				assert.ok(asset);
				downloads++;
				response.writeHead(200);
				response.end(
					corrupt && asset.name === 'plugin.wasm'
						? Buffer.alloc(asset.bytes.length)
						: asset.bytes,
				);
				return;
			}
			if (request.method === 'PATCH') {
				assert.equal(downloads, uploads.size);
				assert.deepEqual(JSON.parse((await body(request)).toString()), {
					draft: false,
				});
				publishes++;
				draft = false;
			}
			json(response, description());
		});
		try {
			const pkg = await verifyPackage(
				sample.directory,
				'plugin.wasm',
				sample.sha256,
			);
			const publisher = new GitHubPublisher(
				'publisher-token',
				new HTTPClient(),
				remote.origin,
				remote.origin,
			);
			if (corrupt) {
				await assert.rejects(
					publisher.publish(
						'owner/repo',
						'v1.0.0',
						pkg,
						'Fix source tracking.',
					),
					/asset differs/,
				);
				assert.equal(publishes, 0);
				assert.equal(draft, true);
			} else {
				await publisher.publish(
					'owner/repo',
					'v1.0.0',
					pkg,
					'Fix source tracking.',
				);
				assert.equal(publishes, 1);
				for (const file of uploads.values()) {
					assert.deepEqual(
						file.bytes,
						await readFile(join(sample.directory, file.name)),
					);
				}
				await publisher.publish(
					'owner/repo',
					'v1.0.0',
					pkg,
					'Fix source tracking.',
				);
				assert.equal(publishes, 1);
				assert.equal(uploads.size, pkg.files.length);
			}
		} finally {
			await remote.close();
			await sample.cleanup();
		}
	}
});

test('authentication failures never create a release and redirects never receive credentials', async () => {
	// GitHub auth failure is not a missing release. An authenticated redirect must also fail
	// before following its destination, otherwise an endpoint could disclose the publisher token.
	let mutations = 0;
	let captures = 0;
	const remote = await server((request, response) => {
		if (request.method !== 'GET') mutations++;
		if (request.url === '/capture') captures++;
		response.writeHead(request.url === '/redirect' ? 302 : 403, {
			location: '/capture',
		});
		response.end();
	});
	try {
		await assert.rejects(
			new GitHubPublisher('secret', new HTTPClient(), remote.origin).publish(
				'owner/repo',
				'v1',
				{
					files: [],
					asset: 'plugin.wasm',
					sha256: 'a'.repeat(64),
				},
				'',
			),
			(error) => error instanceof HTTPError && error.status === 403,
		);
		assert.equal(captures, 0);
		assert.equal(mutations, 0);
		await assert.rejects(
			new HTTPClient().json(`${remote.origin}/redirect`, {
				headers: { Authorization: 'Bearer secret' },
			}),
			/HTTP request failed/,
		);
		assert.equal(captures, 0);
	} finally {
		await remote.close();
	}
});

test('status polling survives HTTP throttling and exposes the validator while waiting', async () => {
	// A temporary API failure must not abandon a release that the registry is still processing.
	// Real HTTP responses exercise Retry-After, state transitions and the durable final receipt.
	let requests = 0;
	const remote = await server((_request, response) => {
		requests++;
		if (requests === 1) {
			response.writeHead(429, { 'retry-after': '12' });
			response.end();
			return;
		}
		json(response, {
			id: identifier,
			status: requests === 2 ? 'validating' : 'published',
			validator_run: 'https://github.com/owner/registry/actions/runs/42/attempts/1',
		});
	});
	const sample = await fixture();
	const receipt = join(sample.directory, 'publication.json');
	const delays: number[] = [];
	try {
		await waitForPublication(identifier, sample.sha256, receipt, {
			origin: remote.origin,
			sleep: async (delay) => {
				delays.push(delay);
			},
		});
		assert.deepEqual(delays, [12_000, 15_000]);
		const result: unknown = JSON.parse(await readFile(receipt, 'utf8'));
		assert.deepEqual(result, {
			id: identifier,
			sha256: sample.sha256,
			status_url: `${remote.origin}/v1/submissions/${identifier}`,
			status: 'published',
			response: {
				id: identifier,
				status: 'published',
				validator_run: 'https://github.com/owner/registry/actions/runs/42/attempts/1',
			},
		});
	} finally {
		await remote.close();
		await sample.cleanup();
	}
});

test('terminal states, wrong identity, and exhausted deadlines fail with useful receipts', async () => {
	// A green release requires published status for this submission. These server responses cover
	// rejection, mismatched identity and a stuck validator without spending real time on polling.
	const sample = await fixture();
	try {
		for (
			const status of [
				'rejected',
				'failed',
				'validating',
				'wrong-identity',
			]
		) {
			const remote = await server((_request, response) =>
				json(response, {
					id: status === 'wrong-identity' ? 'other' : identifier,
					status,
				})
			);
			try {
				const receipt = join(sample.directory, 'publication.json');
				await assert.rejects(
					waitForPublication(identifier, sample.sha256, receipt, {
						origin: remote.origin,
						timeoutMs: 0,
					}),
					status === 'wrong-identity'
						? /different submission/
						: new RegExp(status),
				);
				assert.ok((await readFile(receipt, 'utf8')).includes(identifier));
			} finally {
				await remote.close();
			}
		}
	} finally {
		await sample.cleanup();
	}
});
