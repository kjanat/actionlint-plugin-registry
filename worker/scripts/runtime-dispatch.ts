import assert from 'node:assert/strict';
import { generateKeyPairSync } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { Miniflare } from 'miniflare';
import type { Request as RuntimeRequest } from 'miniflare';

const bundle = await readFile('.dry-run/index.js', 'utf8');
const installationToken = `ghs_123_${'a'.repeat(600)}.test-payload.test_signature`;
const { privateKey } = generateKeyPairSync('rsa', {
	modulusLength: 2048,
	privateKeyEncoding: { format: 'pem', type: 'pkcs8' },
	publicKeyEncoding: { format: 'pem', type: 'spki' },
});
const probe = `
import { PublicationInbox } from "./index.js";
export class Probe extends PublicationInbox {
	completed = 0;
	async alarm() {
		await super.alarm();
		this.completed++;
	}
	async fetch(request) {
		const now = Math.floor(Date.now() / 1000);
		if (new URL(request.url).pathname === "/seed") {
			this.ctx.storage.sql.exec(
				"INSERT INTO submissions(id,publisher,version_key,body_hash,receipt,status,created) VALUES('test','test','test','test','{}','pending',?)",
				now,
			);
			this.ctx.storage.sql.exec(
				"INSERT INTO dispatch_outbox(submission_id,next_attempt,expires) VALUES('test',?,?)",
				now, now + 3600,
			);
			await this.scheduleAlarm(Date.now() + 50);
		}
		return Response.json({
			completed: this.completed,
			rows: this.ctx.storage.sql.exec("SELECT attempts FROM dispatch_outbox").toArray(),
		});
	}
}
export default {
	fetch(request, env) {
		return env.INBOX.getByName("test").fetch(request);
	},
};`;

// The actual deployment bundle executes in workerd. Every outbound request is
// intercepted; no GitHub credentials or external network access are needed.
for (const redirectAt of [0, 1, 2]) {
	const requests: string[] = [];
	const instance = new Miniflare({
		telemetry: { enabled: false },
		workers: [{
			config: {
				name: 'registry-dispatch-test',
				compatibilityDate: '2026-10-18',
				manifest: {
					mainModule: 'probe.js',
					modules: {
						'probe.js': { type: 'esm', contents: probe },
						'index.js': { type: 'esm', contents: bundle },
					},
				},
				exports: { Probe: { type: 'durable-object', storage: 'sqlite' } },
				env: {
					INBOX: { type: 'durable-object', worker: 'registry-dispatch-test', exportName: 'Probe' },
					GITHUB_APP_CLIENT_ID: { type: 'text', value: 'test-client' },
					GITHUB_APP_INSTALLATION_ID: { type: 'text', value: '123' },
					GITHUB_APP_PRIVATE_KEY: { type: 'text', value: privateKey },
					REGISTRY_REPOSITORY_ID: { type: 'text', value: '1424600475' },
				},
			},
			dev: {
				outboundService: {
					type: 'fetcher',
					handler: async (request: RuntimeRequest) => {
						requests.push(request.url);
						assert.equal(request.method, 'POST');
						if (requests.length === 1) {
							assert.equal(request.url, 'https://api.github.com/app/installations/123/access_tokens');
							assert.deepEqual(await request.json(), {
								repository_ids: [1424600475],
								permissions: { actions: 'write' },
							});
						} else {
							assert.equal(request.headers.get('authorization'), `Bearer ${installationToken}`);
							assert.equal(
								request.url,
								'https://api.github.com/repos/kjanat/actionlint-registry/actions/workflows/admission.yml/dispatches',
							);
							assert.deepEqual(await request.json(), { ref: 'master' });
						}
						if (requests.length === redirectAt) {
							return new Response(null, { status: 302, headers: { location: 'https://other.example/' } });
						}
						if (requests.length === 1) {
							return Response.json({
								token: installationToken,
								expires_at: new Date(Date.now() + 3600000).toISOString(),
								permissions: { actions: 'write', metadata: 'read' },
								repositories: [{ id: 1424600475, full_name: 'kjanat/actionlint-registry' }],
							}, { status: 201 });
						}
						return Response.json({ workflow_run_id: 123 });
					},
				},
			},
		}],
	});
	try {
		assert.equal((await instance.dispatchFetch('https://registry.test/seed')).status, 200);
		let state: unknown;
		for (let attempt = 0; attempt < 100; attempt++) {
			state = await (await instance.dispatchFetch('https://registry.test/state')).json();
			if (typeof state === 'object' && state !== null && 'completed' in state && state.completed === 1) break;
			await new Promise(resolve => setTimeout(resolve, 50));
		}
		assert.deepEqual(state, { completed: 1, rows: redirectAt === 0 ? [] : [{ attempts: 1 }] });
		assert.equal(requests.length, redirectAt === 1 ? 1 : 2);
	} finally {
		await instance.dispose();
	}
}
console.log('workerd dispatch and redirect rejection passed');
