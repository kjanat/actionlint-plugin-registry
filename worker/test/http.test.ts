import { expect, test } from 'bun:test';
import { preflight } from '#http';
import { body } from '#model';

const policy = {
	REGISTRY_ORIGIN: 'https://registry.actionlint.kjanat.dev',
	REGISTRY_REPOSITORY_ID: '1424600475',
	REGISTRY_WORKFLOW_REF: 'kjanat/actionlint-plugin-registry/.github/workflows/admission.yml@refs/heads/master',
};

test('The API root redirects visitors to the catalogue before durable-object access', () => {
	for (const method of ['GET', 'HEAD']) {
		const response = preflight(new Request(`${policy.REGISTRY_ORIGIN}/`, { method }), policy);
		expect(response?.status).toBe(302);
		expect(response?.headers.get('location')).toBe('https://plugins.actionlint.kjanat.dev/');
	}
	expect(preflight(new Request('https://evil.example/'), policy)?.status).toBe(400);
	expect(preflight(new Request(`${policy.REGISTRY_ORIGIN}/?target=https://evil.example`), policy)?.status)
		.toBe(400);
	expect(preflight(new Request(`${policy.REGISTRY_ORIGIN}/missing`), policy)?.status).toBe(404);
	expect(preflight(new Request(`${policy.REGISTRY_ORIGIN}/`, { method: 'POST' }), policy)?.status).toBe(404);
});

test('Go discovery resolves the module and CLI paths without redirecting', async () => {
	for (const path of ['/', '/cmd/registry']) {
		const response = preflight(new Request(`${policy.REGISTRY_ORIGIN}${path}?go-get=1`), policy);
		expect(response?.status).toBe(200);
		expect(response?.headers.get('content-type')).toBe('text/html; charset=utf-8');
		expect(response?.headers.has('location')).toBe(false);
		expect(await response?.text()).toContain(
			'name="go-import" content="registry.actionlint.kjanat.dev git https://github.com/kjanat/actionlint-plugin-registry"',
		);
	}
	const head = preflight(new Request(`${policy.REGISTRY_ORIGIN}/?go-get=1`, { method: 'HEAD' }), policy);
	expect(head?.status).toBe(200);
	expect(await head?.text()).toBe('');
	for (const query of ['?go-get=0', '?go-get=1&go-get=1', '?go-get=1&target=evil']) {
		expect(preflight(new Request(`${policy.REGISTRY_ORIGIN}/${query}`), policy)?.status).toBe(400);
	}
	expect(preflight(new Request(`${policy.REGISTRY_ORIGIN}/?go-get=1`, { method: 'POST' }), policy)?.status).toBe(400);
});

test('HTTP preflight denies unauthenticated writes before durable-object access', () => {
	for (
		const path of [
			'/v1/submissions',
			'/v1/claims',
			'/v1/claims/00000000-0000-4000-8000-000000000001/complete',
		]
	) {
		const response = preflight(
			new Request(`${policy.REGISTRY_ORIGIN}${path}`, { method: 'POST' }),
			policy,
		);
		expect(response?.status).toBe(401);
		expect(response?.headers.get('cache-control')).toBe('no-store');
	}
	expect(
		preflight(new Request(`${policy.REGISTRY_ORIGIN}/health`), policy)?.status,
	).toBe(200);
	expect(
		preflight(new Request(`${policy.REGISTRY_ORIGIN}/health`), {
			...policy,
			REGISTRY_REPOSITORY_ID: '',
		})?.status,
	).toBe(503);
	expect(
		preflight(new Request('https://evil.example/health'), policy)?.status,
	).toBe(400);
	expect(
		preflight(new Request(`${policy.REGISTRY_ORIGIN}/v1/claims`), policy)
			?.status,
	).toBe(404);
});

test('HTTP JSON decoding enforces content type, size and valid UTF-8', async () => {
	const url = `${policy.REGISTRY_ORIGIN}/v1/submissions`;
	await expect(
		body(new Request(url, { method: 'POST', body: '{}' })),
	).rejects.toThrow('Set the Content-Type header to application/json.');
	await expect(
		body(
			new Request(url, {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: ' '.repeat(16385),
			}),
		),
	).rejects.toThrow('The request body exceeds 16 KiB.');
	await expect(
		body(
			new Request(url, {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: new Uint8Array([0xff]),
			}),
		),
	).rejects.toThrow('The request body is not valid JSON.');
});
