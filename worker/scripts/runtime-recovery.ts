import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { exportJWK, generateKeyPair, SignJWT } from 'jose';
import { Miniflare, Response } from 'miniflare';
import type { Request as RuntimeRequest } from 'miniflare';

function object(value: unknown): Record<string, unknown> {
	assert.ok(value !== null && typeof value === 'object' && !Array.isArray(value));
	return Object.fromEntries(Object.entries(value));
}

function text(value: unknown): string {
	assert.equal(typeof value, 'string');
	if (typeof value !== 'string') throw new Error('Expected text');
	return value;
}

const origin = 'https://registry.actionlint.kjanat.dev';
const repository = 'kjanat/actionlint-registry';
const workflow = `${repository}/.github/workflows/admission.yml@refs/heads/master`;
const keys = await generateKeyPair('RS256', { extractable: true });
const jwk = { ...await exportJWK(keys.publicKey), kid: 'recovery', use: 'sig', alg: 'RS256' };
const bundle = await readFile('.dry-run/index.js', 'utf8');

// Exercise signed tokens through the deployed handler in workerd.
// The subclass advances retry time; authorization and lease checks still run.
const probe = `
import handler, { PublicationInbox } from "./index.js";
export class Probe extends PublicationInbox {
  async scheduleAlarm() {}
  async fetch(request) {
    if (new URL(request.url).pathname === '/advance') {
      this.ctx.storage.sql.exec("UPDATE submissions SET lease_until=0 WHERE status='pending'");
      return new Response(null, {status:204});
    }
    return super.fetch(request);
  }
}
export default {
  fetch(request, env) {
    if (new URL(request.url).pathname === '/advance') return env.INBOX.getByName('publication-inbox-v1').fetch(request);
    return handler.fetch(request, env);
  }
};`;

const runtime = new Miniflare({
	telemetry: { enabled: false },
	workers: [{
		config: {
			name: 'registry-recovery-test',
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
				INBOX: { type: 'durable-object', worker: 'registry-recovery-test', exportName: 'Probe' },
				REGISTRY_ORIGIN: { type: 'text', value: origin },
				REGISTRY_REPOSITORY_ID: { type: 'text', value: '1424600475' },
				REGISTRY_WORKFLOW_REF: { type: 'text', value: workflow },
			},
		},
		dev: {
			outboundService: {
				type: 'fetcher',
				handler: async (request: RuntimeRequest) => {
					assert.equal(request.url, 'https://token.actions.githubusercontent.com/.well-known/jwks');
					return Response.json({ keys: [jwk] });
				},
			},
		},
	}],
});

async function token(validator: boolean, run: string): Promise<string> {
	const repo = validator ? repository : 'acme/plugin';
	const ref = validator ? 'refs/heads/master' : 'refs/tags/v1.0.0';
	return new SignJWT({
		repository: repo,
		repository_id: validator ? '1424600475' : '10',
		repository_owner_id: '20',
		repository_visibility: 'public',
		ref,
		ref_type: validator ? 'branch' : 'tag',
		sha: 'a'.repeat(40),
		workflow_ref: validator ? workflow : `${repo}/.github/workflows/release.yml@${ref}`,
		run_id: run,
		run_attempt: '1',
		event_name: validator ? 'workflow_dispatch' : 'push',
	})
		.setProtectedHeader({ alg: 'RS256', kid: 'recovery' })
		.setIssuer('https://token.actions.githubusercontent.com')
		.setSubject(`repo:${repo}:ref:${ref}`)
		.setAudience(validator ? `${origin}/validator` : origin)
		.setJti(crypto.randomUUID()).setIssuedAt().setNotBefore('0s').setExpirationTime('5m')
		.sign(keys.privateKey);
}

async function post(
	path: string,
	credential: string,
	payload: unknown,
	expected = 200,
): Promise<Record<string, unknown>> {
	const response = await runtime.dispatchFetch(origin + path, {
		method: 'POST',
		headers: { authorization: `Bearer ${credential}`, 'content-type': 'application/json' },
		body: JSON.stringify(payload),
	});
	const result = object(await response.json());
	assert.equal(response.status, expected, JSON.stringify(result));
	return result;
}

try {
	const publisher = await token(false, '100');
	const first = await token(true, '200');
	const second = await token(true, '201');
	const submitted = await post('/v1/submissions', publisher, {
		schema_version: 1,
		provider: 'github',
		repository: 'acme/plugin',
		tag: 'v1.0.0',
		asset: 'plugin.wasm',
		sha256: 'a'.repeat(64),
		kind: 'core',
	}, 202);
	const id = text(submitted.id);
	const claim = object((await post('/v1/claims', first, {})).claim);
	const status = object(await (await runtime.dispatchFetch(`${origin}/v1/submissions/${id}`)).json());
	assert.equal(status.status, 'validating');
	assert.equal(status.validator_run, `https://github.com/${repository}/actions/runs/200/attempts/1`);
	await post(`/v1/claims/${id}/retry`, publisher, { lease_id: claim.lease_id }, 401);
	await post(`/v1/claims/${id}/retry`, second, { lease_id: claim.lease_id }, 403);
	const retried = await post(`/v1/claims/${id}/retry`, first, { lease_id: claim.lease_id });
	assert.equal(retried.status, 'pending');
	assert.ok(Date.parse(text(retried.retry_at)) > Date.now());
	assert.deepEqual(await post(`/v1/claims/${id}/retry`, first, { lease_id: claim.lease_id }), retried);
	assert.equal((await post('/v1/claims', second, {})).claim, null);
	assert.equal((await runtime.dispatchFetch(`${origin}/advance`)).status, 204);
	const next = object((await post('/v1/claims', second, {})).claim);
	assert.notEqual(next.lease_id, claim.lease_id);
	await post(`/v1/claims/${id}/retry`, first, { lease_id: claim.lease_id }, 403);
	await post(`/v1/claims/${id}/complete`, first, { lease_id: claim.lease_id, status: 'published' }, 403);
	assert.equal(
		(await post(`/v1/claims/${id}/complete`, second, { lease_id: next.lease_id, status: 'published' })).status,
		'published',
	);
	assert.equal((await post(`/v1/claims/${id}/retry`, second, { lease_id: next.lease_id })).status, 'published');
	console.log('workerd authenticated recovery and stale lease rejection passed');
} finally {
	await runtime.dispose();
}
